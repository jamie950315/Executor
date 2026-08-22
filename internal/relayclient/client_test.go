package relayclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/jamie950315/executor/internal/config"
	"github.com/jamie950315/executor/internal/control"
	"github.com/jamie950315/executor/internal/mcp"
	"github.com/jamie950315/executor/internal/relay"
	"github.com/jamie950315/executor/internal/secrets"
)

type cancellableDispatcher struct {
	started  chan struct{}
	canceled chan struct{}
}

type blockingRotatingLifecycle struct {
	inner   *rotatingLifecycle
	started chan struct{}
	release chan struct{}
}

func (l *blockingRotatingLifecycle) Kill(ctx context.Context) (control.Result, error) {
	close(l.started)
	select {
	case <-l.release:
		return l.inner.Kill(ctx)
	case <-ctx.Done():
		return control.Result{}, ctx.Err()
	}
}

func (l *blockingRotatingLifecycle) Resume(ctx context.Context) error { return l.inner.Resume(ctx) }

func (d *cancellableDispatcher) Dispatch(ctx context.Context, _ mcp.ToolCall) (any, error) {
	close(d.started)
	<-ctx.Done()
	close(d.canceled)
	return nil, ctx.Err()
}

func TestReconnectBackoffDoublesToThirtySecondsWithBoundedJitter(t *testing.T) {
	t.Parallel()
	wantBase := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 30 * time.Second, 30 * time.Second}
	for attempt, base := range wantBase {
		minimum := base * 80 / 100
		maximum := base * 120 / 100
		for _, sample := range []uint64{0, ^uint64(0) / 2, ^uint64(0)} {
			got := reconnectDelay(attempt, sample)
			if got < minimum || got > maximum {
				t.Fatalf("attempt %d sample %d delay = %v, want %v..%v", attempt, sample, got, minimum, maximum)
			}
		}
	}
}

func TestResultMessagesUseOneResponseOrOrderedLosslessChunksWithinBounds(t *testing.T) {
	t.Parallel()
	small := []byte(`{"ready":true}`)
	messages, err := resultMessages("request-small", small, 1024, 4096)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 || messages[0].Type != relay.MessageTypeResponse {
		t.Fatalf("small result messages = %#v", messages)
	}
	payload, err := relay.DecodePayload[relay.ResponsePayload](messages[0])
	if err != nil || !bytes.Equal(payload.Result, small) {
		t.Fatalf("small response payload = %s err=%v", payload.Result, err)
	}

	large := bytes.Repeat([]byte("0123456789abcdef"), 80)
	messages, err = resultMessages("request-large", large, 300, 16*1024)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) < 2 {
		t.Fatalf("large result used %d messages, want chunks", len(messages))
	}
	var rebuilt []byte
	for sequence, message := range messages {
		encoded, err := json.Marshal(message)
		if err != nil {
			t.Fatal(err)
		}
		if len(encoded) > 300 || message.Type != relay.MessageTypeStreamChunk {
			t.Fatalf("chunk %d size/type = %d/%s", sequence, len(encoded), message.Type)
		}
		chunk, err := relay.DecodePayload[relay.StreamChunkPayload](message)
		if err != nil {
			t.Fatal(err)
		}
		if chunk.Sequence != uint64(sequence) || chunk.Final != (sequence == len(messages)-1) {
			t.Fatalf("chunk %d metadata = %#v", sequence, chunk)
		}
		rebuilt = append(rebuilt, chunk.Data...)
	}
	if !bytes.Equal(rebuilt, large) {
		t.Fatal("chunking truncated or reordered the result")
	}
	if _, err := resultMessages("request-too-large", large, 300, len(large)); !errors.Is(err, ErrResultTooLarge) {
		t.Fatalf("oversized result error = %v", err)
	}
}

func TestResultMessageIDsStayWithinCrossLanguageLimitForMaximumRequestID(t *testing.T) {
	t.Parallel()
	requestID := strings.Repeat("r", 256)
	messages, err := resultMessages(requestID, []byte(`{"ok":true}`), 1024, 4096)
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range messages {
		if len(message.MessageID) > 256 {
			t.Fatalf("message ID length = %d, want <= 256", len(message.MessageID))
		}
	}
}

func TestRequestRegistryCancelsExactRequestAndRejectsDuplicates(t *testing.T) {
	t.Parallel()
	registry := newRequestRegistry()
	ctx, cancel := context.WithCancel(context.Background())
	if !registry.Add("request-1", cancel) {
		t.Fatal("first request registration failed")
	}
	if registry.Add("request-1", func() {}) {
		t.Fatal("duplicate request registration succeeded")
	}
	if !registry.Cancel("request-1") {
		t.Fatal("known request was not cancelled")
	}
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("registered request context was not cancelled")
	}
	if registry.Cancel("request-1") {
		t.Fatal("cancelled request remained registered")
	}
}

func TestClientAuthenticatesRefreshesDispatchesHeartbeatsAndStopsOnContext(t *testing.T) {
	configPath, cfg, values := connectedRelayFixture(t)
	now := time.Unix(1_700_000_000, 0)
	dispatcher := &recordingDispatcher{}
	adapter, err := NewAdapter(AdapterOptions{
		ConfigPath: configPath, Now: func() time.Time { return now },
		DispatcherFactory: func(config.Config, secrets.Values) Dispatcher { return dispatcher },
	})
	if err != nil {
		t.Fatal(err)
	}
	socket := newFakeSocket()
	var dialURL string
	client, err := NewClient(ClientOptions{
		ConfigPath: configPath, ExecutorVersion: "test-version", Adapter: adapter,
		HeartbeatInterval: 10 * time.Millisecond, Now: func() time.Time { return now },
		Dial: func(_ context.Context, endpoint string, _ *http.Client) (relaySocket, error) {
			dialURL = endpoint
			return socket, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- client.Run(ctx) }()

	completeFakeHandshake(t, socket, cfg, values, now)
	if want := "wss://dashboard.example.test/api/device/connect/" + url.PathEscape(cfg.UnifiedDashboard.DeviceID); dialURL != want {
		t.Fatalf("dial URL = %q, want %q", dialURL, want)
	}

	heartbeat := decodeEnvelopeForTest(t, nextFakeWrite(t, socket))
	if heartbeat.Type != relay.MessageTypeHeartbeat {
		t.Fatalf("heartbeat type = %s", heartbeat.Type)
	}
	grant := signGrantForTest(t, values, cfg, "access-1", "browser-1", now)
	request, err := relay.NewEnvelope(relay.MessageTypeRequest, "message-1", relay.RequestPayload{
		RequestID: "request-1", Method: "device_status",
		Arguments: callArgumentsForTest(t, grant, "access-1", "browser-1", map[string]any{"action": "summary"}),
	})
	if err != nil {
		t.Fatal(err)
	}
	socket.reads <- mustJSON(t, request)
	response := nextEnvelopeOfType(t, socket, relay.MessageTypeResponse)
	payload, err := relay.DecodePayload[relay.ResponsePayload](response)
	if err != nil || payload.RequestID != "request-1" || string(payload.Result) != `{"marker":"SENSITIVE_RESULT_MARKER","ok":true}` {
		t.Fatalf("response payload = %#v err=%v", payload, err)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("client did not stop on context cancellation")
	}
	select {
	case <-socket.closed:
	case <-time.After(time.Second):
		t.Fatal("client did not close the WebSocket on shutdown")
	}
}

func TestClientUsesRealTLSWebSocketTransportForHandshakeRefreshAndHeartbeat(t *testing.T) {
	configPath, cfg, values := connectedRelayFixture(t)
	now := time.Now().UTC().Truncate(time.Second)
	heartbeatReceived := make(chan struct{})
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		connection, err := websocket.Accept(writer, request, nil)
		if err != nil {
			return
		}
		defer connection.CloseNow()
		nonce := "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
		challenge := []byte(`{"version":1,"type":"device_challenge","device_id":"` + cfg.UnifiedDashboard.DeviceID + `","nonce":"` + nonce + `","issued_at":` + strconv.FormatInt(now.Unix(), 10) + `}`)
		if err := connection.Write(request.Context(), websocket.MessageText, challenge); err != nil {
			return
		}
		if _, _, err := connection.Read(request.Context()); err != nil {
			return
		}
		if err := connection.Write(request.Context(), websocket.MessageText, []byte(`{"version":1,"type":"device_authenticated"}`)); err != nil {
			return
		}
		if _, _, err := connection.Read(request.Context()); err != nil {
			return
		}
		ack := []byte(`{"version":1,"type":"device_refreshed","generation":` + strconv.FormatUint(values.Generation, 10) + `}`)
		if err := connection.Write(request.Context(), websocket.MessageText, ack); err != nil {
			return
		}
		_, heartbeat, err := connection.Read(request.Context())
		if err == nil && decodeEnvelopeForTest(t, heartbeat).Type == relay.MessageTypeHeartbeat {
			close(heartbeatReceived)
		}
	}))
	defer server.Close()
	cfg.UnifiedDashboard.URL = server.URL
	if err := config.Save(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	client, err := NewClient(ClientOptions{
		ConfigPath: configPath, ExecutorVersion: "test-version", HTTPClient: server.Client(),
		HeartbeatInterval: 5 * time.Millisecond, Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- client.Run(ctx) }()
	select {
	case <-heartbeatReceived:
	case <-time.After(3 * time.Second):
		cancel()
		t.Fatal("real WebSocket transport did not complete the relay handshake")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("real WebSocket client did not stop")
	}
}

func TestClientCancellationCancelsOnlyTheMatchingDispatch(t *testing.T) {
	configPath, cfg, values := connectedRelayFixture(t)
	now := time.Unix(1_700_000_000, 0)
	dispatcher := &cancellableDispatcher{started: make(chan struct{}), canceled: make(chan struct{})}
	adapter, err := NewAdapter(AdapterOptions{
		ConfigPath: configPath, Now: func() time.Time { return now },
		DispatcherFactory: func(config.Config, secrets.Values) Dispatcher { return dispatcher },
	})
	if err != nil {
		t.Fatal(err)
	}
	socket := newFakeSocket()
	client, err := NewClient(ClientOptions{
		ConfigPath: configPath, ExecutorVersion: "test-version", Adapter: adapter,
		HeartbeatInterval: time.Hour, Now: func() time.Time { return now },
		Dial: func(context.Context, string, *http.Client) (relaySocket, error) { return socket, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- client.Run(ctx) }()
	completeFakeHandshake(t, socket, cfg, values, now)

	grant := signGrantForTest(t, values, cfg, "access-1", "browser-1", now)
	request, err := relay.NewEnvelope(relay.MessageTypeRequest, "message-cancel", relay.RequestPayload{
		RequestID: "request-cancel", Method: "device_status",
		Arguments: callArgumentsForTest(t, grant, "access-1", "browser-1", map[string]any{"action": "summary"}),
	})
	if err != nil {
		t.Fatal(err)
	}
	socket.reads <- mustJSON(t, request)
	select {
	case <-dispatcher.started:
	case <-time.After(time.Second):
		t.Fatal("request did not reach dispatcher")
	}
	cancellation, _ := relay.NewEnvelope(relay.MessageTypeCancellation, "cancel-message", relay.CancellationPayload{RequestID: "request-cancel"})
	socket.reads <- mustJSON(t, cancellation)
	select {
	case <-dispatcher.canceled:
	case <-time.After(time.Second):
		t.Fatal("cancellation did not reach matching request context")
	}
	response := nextEnvelopeOfType(t, socket, relay.MessageTypeResponse)
	payload, err := relay.DecodePayload[relay.ResponsePayload](response)
	if err != nil || payload.Failure == nil || payload.Failure.Code != "cancelled" {
		t.Fatalf("cancel response = %#v err=%v", payload, err)
	}
}

func TestClientRefreshesBeforeEncryptedRotateOrKillResponseAndKillDisconnects(t *testing.T) {
	for _, method := range []string{"control.rotate", "control.kill"} {
		t.Run(method, func(t *testing.T) {
			configPath, cfg, values := connectedRelayFixture(t)
			now := time.Unix(1_700_000_000, 0)
			lifecycle := &blockingRotatingLifecycle{
				inner: &rotatingLifecycle{stateDir: cfg.StateDir}, started: make(chan struct{}), release: make(chan struct{}),
			}
			adapter, err := NewAdapter(AdapterOptions{
				ConfigPath: configPath, Now: func() time.Time { return now },
				LifecycleFactory: func(string) (Lifecycle, error) { return lifecycle, nil },
			})
			if err != nil {
				t.Fatal(err)
			}
			socket := newFakeSocket()
			client, err := NewClient(ClientOptions{
				ConfigPath: configPath, ExecutorVersion: "test-version", Adapter: adapter,
				HeartbeatInterval: 5 * time.Millisecond, Now: func() time.Time { return now },
				Dial: func(context.Context, string, *http.Client) (relaySocket, error) { return socket, nil },
			})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			go func() { _ = client.Run(ctx) }()
			completeFakeHandshake(t, socket, cfg, values, now)

			browser, err := relay.GenerateDeviceIdentity()
			if err != nil {
				t.Fatal(err)
			}
			grant := signGrantForTest(t, values, cfg, "access-1", "browser-1", now)
			request, err := relay.NewEnvelope(relay.MessageTypeRequest, "message-lifecycle", relay.RequestPayload{
				RequestID: "request-lifecycle", Method: method,
				Arguments: callArgumentsForTest(t, grant, "access-1", "browser-1", map[string]any{"response_public_key": browser.PublicJWK()}),
			})
			if err != nil {
				t.Fatal(err)
			}
			socket.reads <- mustJSON(t, request)
			select {
			case <-lifecycle.started:
			case <-time.After(time.Second):
				t.Fatal("lifecycle action did not start")
			}
			select {
			case unexpected := <-socket.writes:
				t.Fatalf("relay wrote during lifecycle transition: %s", unexpected)
			case <-time.After(20 * time.Millisecond):
			}
			close(lifecycle.release)
			refreshWire := nextFakeWrite(t, socket)
			var refreshMessage map[string]any
			if err := json.Unmarshal(refreshWire, &refreshMessage); err != nil || refreshMessage["type"] != "device_refresh" || refreshMessage["generation"] != float64(values.Generation+1) {
				t.Fatalf("first lifecycle write was not generation refresh: %s err=%v", refreshWire, err)
			}
			socket.reads <- []byte(`{"version":1,"type":"device_refreshed","generation":2}`)
			response := nextEnvelopeOfType(t, socket, relay.MessageTypeResponse)
			payload, err := relay.DecodePayload[relay.ResponsePayload](response)
			if err != nil || bytes.Contains(payload.Result, []byte("recovery_key")) || !bytes.Contains(payload.Result, []byte("ciphertext")) {
				t.Fatalf("lifecycle response = %s err=%v", payload.Result, err)
			}
			if method == "control.kill" {
				select {
				case <-socket.closed:
				case <-time.After(time.Second):
					t.Fatal("Kill response was not followed by relay disconnect")
				}
			} else {
				select {
				case <-socket.closed:
					t.Fatal("Rotate disconnected the healthy relay")
				case <-time.After(20 * time.Millisecond):
				}
			}
		})
	}
}

func TestClientDoesNotReconnectWhileDisabled(t *testing.T) {
	configPath, cfg, _ := connectedRelayFixture(t)
	if err := os.WriteFile(filepath.Join(cfg.StateDir, "disabled"), []byte("quiesced\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var dials atomic.Int32
	client, err := NewClient(ClientOptions{
		ConfigPath: configPath, ExecutorVersion: "test-version", HeartbeatInterval: time.Hour,
		Dial: func(context.Context, string, *http.Client) (relaySocket, error) {
			dials.Add(1)
			return nil, errors.New("unexpected dial")
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := client.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if dials.Load() != 0 {
		t.Fatalf("disabled client dialed %d times", dials.Load())
	}
}

func TestClientRejectsProtocolMismatchBeforeSendingDeviceProof(t *testing.T) {
	configPath, _, _ := connectedRelayFixture(t)
	socket := newFakeSocket()
	client, err := NewClient(ClientOptions{
		ConfigPath: configPath, ExecutorVersion: "test-version", HeartbeatInterval: time.Hour,
		Dial: func(context.Context, string, *http.Client) (relaySocket, error) { return socket, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = client.Run(ctx) }()
	socket.reads <- []byte(`{"version":2,"type":"device_challenge","device_id":"wrong","nonce":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA","issued_at":1700000000}`)
	select {
	case message := <-socket.writes:
		t.Fatalf("protocol mismatch received a device proof: %s", message)
	case <-socket.closed:
	case <-time.After(time.Second):
		t.Fatal("protocol mismatch did not close the relay")
	}
}

func TestConnectedClientClosesWhenLocalDisabledMarkerAppears(t *testing.T) {
	configPath, cfg, values := connectedRelayFixture(t)
	now := time.Unix(1_700_000_000, 0)
	socket := newFakeSocket()
	client, err := NewClient(ClientOptions{
		ConfigPath: configPath, ExecutorVersion: "test-version", HeartbeatInterval: 5 * time.Millisecond,
		Now:  func() time.Time { return now },
		Dial: func(context.Context, string, *http.Client) (relaySocket, error) { return socket, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = client.Run(ctx) }()
	completeFakeHandshake(t, socket, cfg, values, now)
	if err := os.WriteFile(filepath.Join(cfg.StateDir, "disabled"), []byte("quiesced\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case <-socket.closed:
	case <-time.After(time.Second):
		t.Fatal("connected relay stayed open after Executor was disabled")
	}
}

type fakeSocket struct {
	reads  chan []byte
	writes chan []byte
	closed chan struct{}
	once   sync.Once
}

func newFakeSocket() *fakeSocket {
	return &fakeSocket{reads: make(chan []byte, 16), writes: make(chan []byte, 32), closed: make(chan struct{})}
}

func (s *fakeSocket) Read(ctx context.Context) ([]byte, error) {
	select {
	case message := <-s.reads:
		return message, nil
	case <-s.closed:
		return nil, errors.New("closed")
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (s *fakeSocket) Write(ctx context.Context, message []byte) error {
	select {
	case s.writes <- append([]byte(nil), message...):
		return nil
	case <-s.closed:
		return errors.New("closed")
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *fakeSocket) Close(int, string) error {
	s.once.Do(func() { close(s.closed) })
	return nil
}

func (s *fakeSocket) SetReadLimit(int64) {}

func connectedRelayFixture(t *testing.T) (string, config.Config, secrets.Values) {
	t.Helper()
	configPath, cfg, values := relayFixture(t)
	cfg.UnifiedDashboard.URL = "https://dashboard.example.test"
	cfg.UnifiedDashboard.Enrolled = true
	if err := config.Save(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	return configPath, cfg, values
}

func completeFakeHandshake(t *testing.T, socket *fakeSocket, cfg config.Config, values secrets.Values, now time.Time) {
	t.Helper()
	nonce := "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	socket.reads <- []byte(`{"version":1,"type":"device_challenge","device_id":"` + cfg.UnifiedDashboard.DeviceID + `","nonce":"` + nonce + `","issued_at":` + strconv.FormatInt(now.Unix(), 10) + `}`)
	var challengeResponse struct {
		Version   uint16 `json:"version"`
		Type      string `json:"type"`
		Nonce     string `json:"nonce"`
		IssuedAt  int64  `json:"issued_at"`
		Signature string `json:"signature"`
	}
	if err := json.Unmarshal(nextFakeWrite(t, socket), &challengeResponse); err != nil {
		t.Fatal(err)
	}
	identity, err := relay.ParseDeviceIdentity(values.RelayPrivateJWK)
	if err != nil {
		t.Fatal(err)
	}
	if challengeResponse.Version != 1 || challengeResponse.Type != "device_challenge_response" || challengeResponse.Nonce != nonce ||
		challengeResponse.IssuedAt != now.Unix() || !relay.VerifyDeviceChallenge(identity.PublicJWK(), cfg.UnifiedDashboard.DeviceID, nonce, now.Unix(), challengeResponse.Signature) {
		t.Fatalf("challenge response = %#v", challengeResponse)
	}
	socket.reads <- []byte(`{"version":1,"type":"device_authenticated"}`)
	var refresh struct {
		Version uint16 `json:"version"`
		Type    string `json:"type"`
		relay.DeviceRefresh
		Signature string `json:"signature"`
	}
	if err := json.Unmarshal(nextFakeWrite(t, socket), &refresh); err != nil {
		t.Fatal(err)
	}
	if refresh.Version != 1 || refresh.Type != "device_refresh" || refresh.DeviceID != cfg.UnifiedDashboard.DeviceID ||
		refresh.Generation != values.Generation || !relay.VerifyDeviceRefresh(identity.PublicJWK(), refresh.DeviceRefresh, refresh.Signature) {
		t.Fatalf("device refresh = %#v", refresh)
	}
	socket.reads <- []byte(`{"version":1,"type":"device_refreshed","generation":` + strconv.FormatUint(values.Generation, 10) + `}`)
}

func nextFakeWrite(t *testing.T, socket *fakeSocket) []byte {
	t.Helper()
	select {
	case message := <-socket.writes:
		return message
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for WebSocket write")
		return nil
	}
}

func decodeEnvelopeForTest(t *testing.T, message []byte) relay.Envelope {
	t.Helper()
	envelope, err := relay.DecodeEnvelope(message)
	if err != nil {
		t.Fatalf("DecodeEnvelope(%s): %v", message, err)
	}
	return envelope
}

func nextEnvelopeOfType(t *testing.T, socket *fakeSocket, messageType relay.MessageType) relay.Envelope {
	t.Helper()
	for attempts := 0; attempts < 20; attempts++ {
		envelope := decodeEnvelopeForTest(t, nextFakeWrite(t, socket))
		if envelope.Type == messageType {
			return envelope
		}
	}
	t.Fatalf("no %s envelope received", messageType)
	return relay.Envelope{}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}
