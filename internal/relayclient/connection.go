package relayclient

import (
	"context"
	cryptorand "crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/jamie950315/executor/internal/config"
	localdashboard "github.com/jamie950315/executor/internal/dashboard"
	"github.com/jamie950315/executor/internal/relay"
	"github.com/jamie950315/executor/internal/secrets"
)

const (
	deviceChallengeLifetime      = 30 * time.Second
	defaultRelayHandshakeTimeout = 30 * time.Second
	defaultRelayRefreshTimeout   = 30 * time.Second
)

type relaySocket interface {
	Read(context.Context) ([]byte, error)
	Write(context.Context, []byte) error
	Close(int, string) error
	SetReadLimit(int64)
}

type DialFunc func(context.Context, string, *http.Client) (relaySocket, error)

type ClientOptions struct {
	ConfigPath        string
	ExecutorVersion   string
	HTTPClient        *http.Client
	Adapter           *Adapter
	HeartbeatInterval time.Duration
	HandshakeTimeout  time.Duration
	RefreshTimeout    time.Duration
	Now               func() time.Time
	Dial              DialFunc
	Sleep             func(context.Context, time.Duration) error
}

type Client struct {
	configPath        string
	executorVersion   string
	httpClient        *http.Client
	adapter           *Adapter
	heartbeatInterval time.Duration
	handshakeTimeout  time.Duration
	refreshTimeout    time.Duration
	now               func() time.Time
	dial              DialFunc
	sleep             func(context.Context, time.Duration) error
	statusMu          sync.RWMutex
	relayStatus       localdashboard.RelayStatus
}

func NewClient(options ClientOptions) (*Client, error) {
	if options.ConfigPath == "" || strings.TrimSpace(options.ExecutorVersion) == "" {
		return nil, errors.New("invalid relay client options")
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.HeartbeatInterval <= 0 {
		options.HeartbeatInterval = 30 * time.Second
	}
	if options.HandshakeTimeout <= 0 {
		options.HandshakeTimeout = defaultRelayHandshakeTimeout
	}
	if options.RefreshTimeout <= 0 {
		options.RefreshTimeout = defaultRelayRefreshTimeout
	}
	if options.Adapter == nil {
		adapter, err := NewAdapter(AdapterOptions{ConfigPath: options.ConfigPath, Now: options.Now})
		if err != nil {
			return nil, err
		}
		options.Adapter = adapter
	}
	if options.Dial == nil {
		options.Dial = dialWebSocket
	}
	if options.Sleep == nil {
		options.Sleep = sleepContext
	}
	client := &Client{
		configPath: options.ConfigPath, executorVersion: options.ExecutorVersion, httpClient: options.HTTPClient,
		adapter: options.Adapter, heartbeatInterval: options.HeartbeatInterval,
		handshakeTimeout: options.HandshakeTimeout, refreshTimeout: options.RefreshTimeout, now: options.Now,
		dial: options.Dial, sleep: options.Sleep,
	}
	client.setRelayState("disconnected")
	return client, nil
}

func (c *Client) Status() localdashboard.RelayStatus {
	c.statusMu.RLock()
	defer c.statusMu.RUnlock()
	return c.relayStatus
}

func (c *Client) setRelayState(state string) {
	if state != "connected" {
		state = "disconnected"
	}
	c.statusMu.Lock()
	c.relayStatus = localdashboard.RelayStatus{State: state, UpdatedAt: c.now().UTC()}
	c.statusMu.Unlock()
}

func (c *Client) Run(ctx context.Context) error {
	if ctx == nil {
		return errors.New("relay context is required")
	}
	attempt := 0
	for {
		if ctx.Err() != nil {
			return nil
		}
		cfg, err := config.Load(c.configPath)
		if err != nil {
			return errors.New("relay configuration unavailable")
		}
		if cfg.UnifiedDashboard.URL == "" || !cfg.UnifiedDashboard.Enrolled {
			c.setRelayState("disconnected")
			if err := c.sleep(ctx, 100*time.Millisecond); err != nil {
				return nil
			}
			continue
		}
		if disabled(filepath.Join(cfg.StateDir, "disabled")) {
			c.setRelayState("disconnected")
			if err := c.sleep(ctx, 100*time.Millisecond); err != nil {
				return nil
			}
			continue
		}
		endpoint, err := relayEndpoint(cfg.UnifiedDashboard.URL, cfg.UnifiedDashboard.DeviceID)
		if err != nil {
			return errors.New("relay endpoint unavailable")
		}
		socket, err := c.dial(ctx, endpoint, c.httpClient)
		if err == nil {
			connection := newConnection(c, socket, cfg)
			_ = connection.run(ctx)
			if connection.authenticated {
				attempt = 0
			}
			_ = socket.Close(int(websocket.StatusNormalClosure), "relay reconnect")
		}
		c.setRelayState("disconnected")
		if ctx.Err() != nil {
			return nil
		}
		var sampleBytes [8]byte
		if _, randomErr := cryptorand.Read(sampleBytes[:]); randomErr != nil {
			return errors.New("relay reconnect unavailable")
		}
		delay := reconnectDelay(attempt, binary.BigEndian.Uint64(sampleBytes[:]))
		attempt++
		if err := c.sleep(ctx, delay); err != nil {
			return nil
		}
	}
}

type connection struct {
	client            *Client
	socket            relaySocket
	initialConfig     config.Config
	writeMu           sync.Mutex
	requests          *requestRegistry
	requestWG         sync.WaitGroup
	refreshMu         sync.Mutex
	refreshWaitMu     sync.Mutex
	refreshWait       *refreshWaiter
	lifecycleInFlight atomic.Int32
	authenticated     bool
}

type refreshWaiter struct {
	generation uint64
	result     chan uint64
	ctx        context.Context
}

func newConnection(client *Client, socket relaySocket, cfg config.Config) *connection {
	return &connection{client: client, socket: socket, initialConfig: cfg, requests: newRequestRegistry()}
}

func (c *connection) run(ctx context.Context) error {
	c.socket.SetReadLimit(maximumRelayMessageBytes)
	handshakeCtx, cancelHandshake := context.WithTimeout(ctx, c.client.handshakeTimeout)
	err := c.handshake(handshakeCtx)
	cancelHandshake()
	if err != nil {
		return err
	}
	if _, err := c.loadMatchingConfig(); err != nil {
		return err
	}
	c.authenticated = true
	c.client.setRelayState("connected")
	defer c.client.setRelayState("disconnected")
	connectionCtx, cancel := context.WithCancel(ctx)
	defer func() {
		cancel()
		c.requests.CancelAll()
		c.requestWG.Wait()
	}()
	heartbeatErrors := make(chan error, 1)
	go func() {
		heartbeatErrors <- c.heartbeat(connectionCtx)
		cancel()
	}()
	for {
		select {
		case err := <-heartbeatErrors:
			return err
		default:
		}
		message, err := c.socket.Read(connectionCtx)
		if err != nil {
			return err
		}
		if len(message) > maximumRelayMessageBytes {
			return errors.New("relay message too large")
		}
		if _, err := c.loadMatchingConfig(); err != nil {
			return err
		}
		if err := c.handleMessage(connectionCtx, message); err != nil {
			return err
		}
	}
}

func (c *connection) handshake(ctx context.Context) error {
	if err := c.negotiateVersion(ctx); err != nil {
		return err
	}
	message, err := c.socket.Read(ctx)
	if err != nil || len(message) > maximumRelayMessageBytes {
		return errors.New("device challenge unavailable")
	}
	challenge, err := parseDeviceChallenge(message)
	if err != nil || challenge.DeviceID != c.initialConfig.UnifiedDashboard.DeviceID ||
		c.client.now().Sub(time.Unix(challenge.IssuedAt, 0)).Abs() > deviceChallengeLifetime {
		return errors.New("invalid device challenge")
	}
	cfg, err := c.loadMatchingConfig()
	if err != nil {
		return err
	}
	values, identity, err := loadRelayIdentity(cfg.StateDir)
	if err != nil {
		return errors.New("relay identity unavailable")
	}
	signature, err := relay.SignDeviceChallenge(identity, challenge.DeviceID, challenge.Nonce, challenge.IssuedAt)
	if err != nil {
		return errors.New("device challenge failed")
	}
	response, _ := json.Marshal(deviceChallengeResponse{
		Version: relay.ProtocolVersion, Type: "device_challenge_response", Nonce: challenge.Nonce,
		IssuedAt: challenge.IssuedAt, Signature: signature,
	})
	if err := c.write(ctx, response); err != nil {
		return err
	}
	authenticated, err := c.socket.Read(ctx)
	if err != nil || !isDeviceAuthenticated(authenticated) {
		return errors.New("device authentication failed")
	}
	refresh, err := c.signedRefresh(cfg, values, identity)
	if err != nil {
		return err
	}
	if err := c.write(ctx, refresh); err != nil {
		return err
	}
	ack, err := c.socket.Read(ctx)
	if err != nil || !isDeviceRefreshed(ack, values.Generation) {
		return errors.New("device refresh failed")
	}
	return nil
}

func (c *connection) negotiateVersion(ctx context.Context) error {
	message, err := c.socket.Read(ctx)
	if err != nil || len(message) > maximumRelayMessageBytes {
		return errors.New("version negotiation failed")
	}
	offer, err := relay.DecodeEnvelope(message)
	if err != nil || offer.Type != relay.MessageTypeVersionNegotiation {
		return errors.New("version negotiation failed")
	}
	payload, err := relay.DecodePayload[relay.VersionNegotiationPayload](offer)
	if err != nil || len(payload.SupportedVersions) == 0 || len(payload.SupportedVersions) > 16 {
		return errors.New("version negotiation failed")
	}
	selected := false
	seen := make(map[uint16]struct{}, len(payload.SupportedVersions))
	for _, version := range payload.SupportedVersions {
		if version == 0 {
			return errors.New("version negotiation failed")
		}
		if _, duplicate := seen[version]; duplicate {
			return errors.New("version negotiation failed")
		}
		seen[version] = struct{}{}
		if version == relay.ProtocolVersion {
			selected = true
		}
	}
	if !selected {
		return errors.New("version negotiation failed")
	}
	response, err := relay.NewEnvelope(
		relay.MessageTypeVersionNegotiation,
		offer.MessageID,
		relay.VersionNegotiationPayload{SupportedVersions: []uint16{relay.ProtocolVersion}},
	)
	if err != nil {
		return errors.New("version negotiation failed")
	}
	if err := c.writeEnvelope(ctx, response); err != nil {
		return errors.New("version negotiation failed")
	}
	return nil
}

func (c *connection) heartbeat(ctx context.Context) error {
	ticker := time.NewTicker(c.client.heartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if c.lifecycleInFlight.Load() > 0 {
				continue
			}
			cfg, err := c.loadMatchingConfig()
			if err != nil {
				return err
			}
			if disabled(filepath.Join(cfg.StateDir, "disabled")) {
				return ErrExecutorDisabled
			}
			values, err := secrets.Load(cfg.StateDir)
			if err != nil {
				return errors.New("relay credentials unavailable")
			}
			messageID, err := randomID()
			if err != nil {
				return err
			}
			envelope, err := relay.NewEnvelope(relay.MessageTypeHeartbeat, messageID, relay.HeartbeatPayload{
				DeviceID: cfg.UnifiedDashboard.DeviceID, Generation: values.Generation, SentAt: c.client.now().Unix(),
			})
			if err != nil {
				return errors.New("heartbeat generation failed")
			}
			if err := c.writeEnvelope(ctx, envelope); err != nil {
				return err
			}
			c.client.setRelayState("connected")
		}
	}
}

func (c *connection) handleMessage(ctx context.Context, message []byte) error {
	if generation, ok := parseDeviceRefreshed(message); ok {
		c.refreshWaitMu.Lock()
		waiter := c.refreshWait
		c.refreshWaitMu.Unlock()
		if waiter == nil || waiter.generation != generation || waiter.ctx.Err() != nil {
			return nil
		}
		select {
		case waiter.result <- generation:
		default:
		}
		return nil
	}
	envelope, err := relay.DecodeEnvelope(message)
	if err != nil {
		return errors.New("invalid relay message")
	}
	switch envelope.Type {
	case relay.MessageTypeRequest:
		payload, err := relay.DecodePayload[relay.RequestPayload](envelope)
		if err != nil || payload.RequestID == "" || payload.Method == "" || len(payload.Arguments) == 0 {
			return errors.New("invalid relay request")
		}
		c.startRequest(ctx, payload.RequestID, payload.Method, payload.Arguments, nil)
		return nil
	case relay.MessageTypeRecoveryUnlock:
		payload, err := relay.DecodePayload[relay.RecoveryUnlockPayload](envelope)
		if err != nil {
			return errors.New("invalid recovery request")
		}
		c.startRequest(ctx, envelope.MessageID, "device.unlock", nil, &payload)
		return nil
	case relay.MessageTypeCancellation:
		payload, err := relay.DecodePayload[relay.CancellationPayload](envelope)
		if err != nil || payload.RequestID == "" {
			return errors.New("invalid relay cancellation")
		}
		c.requests.Cancel(payload.RequestID)
		return nil
	default:
		return errors.New("unexpected relay message type")
	}
}

func (c *connection) startRequest(ctx context.Context, requestID, method string, arguments json.RawMessage, unlock *relay.RecoveryUnlockPayload) {
	requestCtx, cancel := context.WithCancel(ctx)
	if !c.requests.Add(requestID, cancel) {
		_ = c.writeFailure(ctx, requestID, "duplicate")
		return
	}
	if method == "control.kill" || method == "control.rotate" {
		c.lifecycleInFlight.Add(1)
	}
	c.requestWG.Add(1)
	go func() {
		defer func() {
			c.requests.Remove(requestID)
			cancel()
			if method == "control.kill" || method == "control.rotate" {
				c.lifecycleInFlight.Add(-1)
			}
			c.requestWG.Done()
		}()
		var result HandleResult
		var err error
		if unlock != nil {
			result, err = c.client.adapter.HandleRecoveryUnlock(requestCtx, requestID, *unlock)
		} else {
			result, err = c.client.adapter.HandleRequest(requestCtx, requestID, method, arguments)
		}
		if err != nil {
			code := failureCode(err, requestCtx)
			_ = c.writeFailure(ctx, requestID, code)
			return
		}
		if result.Finalize != nil {
			defer runLifecycleFinalizer(result.Finalize)
		}
		if result.RefreshGeneration > 0 {
			if err := c.refresh(requestCtx, result.RefreshGeneration); err != nil {
				if requestCtx.Err() != nil {
					_ = c.writeFailure(ctx, requestID, "cancelled")
				} else {
					// The host-side lifecycle action already rotated the recovery
					// material. Preserve that one-time encrypted result even when the
					// relay refresh acknowledgement is lost, then reconnect so the new
					// generation is synchronized before more requests are accepted.
					_ = c.writeResult(ctx, requestID, result.Payload)
				}
				_ = c.socket.Close(int(websocket.StatusNormalClosure), "relay refresh incomplete")
				return
			}
		}
		if err := c.writeResult(ctx, requestID, result.Payload); err != nil {
			return
		}
		if result.CloseAfterWrite {
			_ = c.socket.Close(int(websocket.StatusNormalClosure), "Executor disabled")
		}
	}()
}

func (c *connection) refresh(ctx context.Context, expectedGeneration uint64) error {
	c.refreshMu.Lock()
	defer c.refreshMu.Unlock()
	refreshCtx, cancelRefresh := context.WithTimeout(ctx, c.client.refreshTimeout)
	defer cancelRefresh()
	cfg, err := c.loadMatchingConfig()
	if err != nil {
		return errors.New("device refresh failed")
	}
	values, identity, err := loadRelayIdentity(cfg.StateDir)
	if err != nil || values.Generation != expectedGeneration {
		return errors.New("device refresh failed")
	}
	message, err := c.signedRefresh(cfg, values, identity)
	if err != nil {
		return err
	}
	waiter := &refreshWaiter{generation: expectedGeneration, result: make(chan uint64, 1), ctx: refreshCtx}
	c.refreshWaitMu.Lock()
	c.refreshWait = waiter
	c.refreshWaitMu.Unlock()
	defer func() {
		c.refreshWaitMu.Lock()
		c.refreshWait = nil
		c.refreshWaitMu.Unlock()
	}()
	if err := c.write(refreshCtx, message); err != nil {
		return err
	}
	select {
	case generation := <-waiter.result:
		if refreshCtx.Err() != nil {
			return refreshCtx.Err()
		}
		if generation != expectedGeneration {
			return errors.New("device refresh failed")
		}
		return nil
	case <-refreshCtx.Done():
		return refreshCtx.Err()
	}
}

func (c *connection) signedRefresh(cfg config.Config, values secrets.Values, identity *relay.DeviceIdentity) ([]byte, error) {
	name, err := os.Hostname()
	if err != nil || strings.TrimSpace(name) == "" {
		name = "Executor Device"
	}
	refresh := relay.DeviceRefresh{
		DeviceID: cfg.UnifiedDashboard.DeviceID, Generation: values.Generation, Name: name,
		Platform: runtime.GOOS, Arch: runtime.GOARCH, ExecutorVersion: c.client.executorVersion,
		MCPURL: configuredMCPURL(cfg.Domain), IssuedAt: c.client.now().Unix(),
	}
	signature, err := relay.SignDeviceRefresh(identity, refresh)
	if err != nil {
		return nil, errors.New("device refresh failed")
	}
	return json.Marshal(signedDeviceRefresh{Version: relay.ProtocolVersion, Type: "device_refresh", DeviceRefresh: refresh, Signature: signature})
}

func (c *connection) loadMatchingConfig() (config.Config, error) {
	cfg, err := config.Load(c.client.configPath)
	if err != nil {
		return config.Config{}, errors.New("relay configuration unavailable")
	}
	initial := c.initialConfig
	if !cfg.UnifiedDashboard.Enrolled ||
		cfg.UnifiedDashboard.URL != initial.UnifiedDashboard.URL ||
		cfg.UnifiedDashboard.DeviceID != initial.UnifiedDashboard.DeviceID ||
		cfg.StateDir != initial.StateDir ||
		cfg.Domain != initial.Domain ||
		cfg.BrokerEndpoint != initial.BrokerEndpoint ||
		cfg.DesktopEndpoint != initial.DesktopEndpoint {
		return config.Config{}, errors.New("relay configuration changed")
	}
	return cfg, nil
}

func (c *connection) writeResult(ctx context.Context, requestID string, result json.RawMessage) error {
	messages, err := resultMessages(requestID, result, maximumRelayMessageBytes, maximumRelayResultBytes)
	if err != nil {
		if errors.Is(err, ErrResultTooLarge) {
			return c.writeFailure(ctx, requestID, "result_too_large")
		}
		return err
	}
	for _, message := range messages {
		if err := c.writeEnvelope(ctx, message); err != nil {
			return err
		}
	}
	return nil
}

func (c *connection) writeFailure(ctx context.Context, requestID, code string) error {
	messageID, err := randomID()
	if err != nil {
		return err
	}
	envelope, err := relay.NewEnvelope(relay.MessageTypeResponse, messageID, relay.ResponsePayload{
		RequestID: requestID, Failure: &relay.ResponseFailure{Code: code},
	})
	if err != nil {
		return errors.New("relay failure serialization failed")
	}
	return c.writeEnvelope(ctx, envelope)
}

func (c *connection) writeEnvelope(ctx context.Context, envelope relay.Envelope) error {
	encoded, err := json.Marshal(envelope)
	if err != nil || len(encoded) > maximumRelayMessageBytes {
		return errors.New("relay message serialization failed")
	}
	return c.write(ctx, encoded)
}

func (c *connection) write(ctx context.Context, message []byte) error {
	if len(message) > maximumRelayMessageBytes {
		return errors.New("relay message too large")
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return c.socket.Write(ctx, message)
}

type deviceChallengeMessage struct {
	Version  uint16 `json:"version"`
	Type     string `json:"type"`
	DeviceID string `json:"device_id"`
	Nonce    string `json:"nonce"`
	IssuedAt int64  `json:"issued_at"`
}

type deviceChallengeResponse struct {
	Version   uint16 `json:"version"`
	Type      string `json:"type"`
	Nonce     string `json:"nonce"`
	IssuedAt  int64  `json:"issued_at"`
	Signature string `json:"signature"`
}

type signedDeviceRefresh struct {
	Version uint16 `json:"version"`
	Type    string `json:"type"`
	relay.DeviceRefresh
	Signature string `json:"signature"`
}

func parseDeviceChallenge(message []byte) (deviceChallengeMessage, error) {
	var challenge deviceChallengeMessage
	if err := decodeStrictJSON(message, &challenge); err != nil || challenge.Version != relay.ProtocolVersion ||
		challenge.Type != "device_challenge" || challenge.DeviceID == "" || challenge.IssuedAt <= 0 {
		return deviceChallengeMessage{}, errors.New("invalid device challenge")
	}
	nonce, err := base64.RawURLEncoding.Strict().DecodeString(challenge.Nonce)
	if err != nil || len(nonce) != 32 {
		return deviceChallengeMessage{}, errors.New("invalid device challenge")
	}
	return challenge, nil
}

func isDeviceAuthenticated(message []byte) bool {
	var value struct {
		Version uint16 `json:"version"`
		Type    string `json:"type"`
	}
	return decodeStrictJSON(message, &value) == nil && value.Version == relay.ProtocolVersion && value.Type == "device_authenticated"
}

func parseDeviceRefreshed(message []byte) (uint64, bool) {
	var value struct {
		Version    uint16 `json:"version"`
		Type       string `json:"type"`
		Generation uint64 `json:"generation"`
	}
	if decodeStrictJSON(message, &value) != nil || value.Version != relay.ProtocolVersion || value.Type != "device_refreshed" || value.Generation == 0 {
		return 0, false
	}
	return value.Generation, true
}

func isDeviceRefreshed(message []byte, generation uint64) bool {
	got, ok := parseDeviceRefreshed(message)
	return ok && got == generation
}

func loadRelayIdentity(stateDir string) (secrets.Values, *relay.DeviceIdentity, error) {
	values, err := secrets.Load(stateDir)
	if err != nil {
		return secrets.Values{}, nil, err
	}
	identity, err := relay.ParseDeviceIdentity(values.RelayPrivateJWK)
	if err != nil {
		return secrets.Values{}, nil, err
	}
	return values, identity, nil
}

func relayEndpoint(base, deviceID string) (string, error) {
	parsed, err := url.Parse(base)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || deviceID == "" {
		return "", errors.New("invalid relay endpoint")
	}
	parsed.Scheme = "wss"
	parsed.Path = "/api/device/connect/" + url.PathEscape(deviceID)
	return parsed.String(), nil
}

func randomID() (string, error) {
	var value [16]byte
	if _, err := cryptorand.Read(value[:]); err != nil {
		return "", errors.New("relay identifier generation failed")
	}
	return hex.EncodeToString(value[:]), nil
}

func failureCode(err error, ctx context.Context) string {
	if ctx.Err() != nil || errors.Is(err, context.Canceled) {
		return "cancelled"
	}
	switch {
	case errors.Is(err, ErrRequestUnauthorized):
		return "unauthorized"
	case errors.Is(err, ErrUnsupportedMethod):
		return "unsupported_method"
	case errors.Is(err, ErrExecutorDisabled):
		return "disabled"
	case errors.Is(err, ErrRequestInvalid):
		return "invalid_request"
	default:
		return "failed"
	}
}

func sleepContext(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type coderSocket struct {
	connection *websocket.Conn
}

func dialWebSocket(ctx context.Context, endpoint string, client *http.Client) (relaySocket, error) {
	connection, _, err := websocket.Dial(ctx, endpoint, &websocket.DialOptions{HTTPClient: client})
	if err != nil {
		return nil, errors.New("relay connection failed")
	}
	return &coderSocket{connection: connection}, nil
}

func (s *coderSocket) Read(ctx context.Context) ([]byte, error) {
	messageType, message, err := s.connection.Read(ctx)
	if err != nil {
		return nil, err
	}
	if messageType != websocket.MessageText {
		return nil, errors.New("binary relay message rejected")
	}
	return message, nil
}

func (s *coderSocket) Write(ctx context.Context, message []byte) error {
	return s.connection.Write(ctx, websocket.MessageText, message)
}

func (s *coderSocket) Close(code int, reason string) error {
	return s.connection.Close(websocket.StatusCode(code), reason)
}

func (s *coderSocket) SetReadLimit(limit int64) {
	s.connection.SetReadLimit(limit)
}
