package relayclient

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/jamie950315/executor/internal/audit"
	"github.com/jamie950315/executor/internal/config"
	"github.com/jamie950315/executor/internal/control"
	localdashboard "github.com/jamie950315/executor/internal/dashboard"
	"github.com/jamie950315/executor/internal/dispatch"
	"github.com/jamie950315/executor/internal/ipc"
	"github.com/jamie950315/executor/internal/mcp"
	"github.com/jamie950315/executor/internal/relay"
	"github.com/jamie950315/executor/internal/secrets"
)

var (
	ErrRequestUnauthorized = errors.New("relay request unauthorized")
	ErrUnsupportedMethod   = errors.New("unsupported relay method")
	ErrExecutorDisabled    = errors.New("Executor is disabled")
	ErrRequestInvalid      = errors.New("invalid relay request")
)

type Dispatcher interface {
	Dispatch(context.Context, mcp.ToolCall) (any, error)
}

type Lifecycle interface {
	Kill(context.Context) (control.Result, error)
	Resume(context.Context) error
}

type remoteKillLifecycle interface {
	PrepareRemoteKill(context.Context) (control.Result, func(context.Context) error, error)
}

type AdapterOptions struct {
	ConfigPath        string
	Now               func() time.Time
	DispatcherFactory func(config.Config, secrets.Values) Dispatcher
	LifecycleFactory  func(string) (Lifecycle, error)
}

type HandleResult struct {
	Payload           json.RawMessage
	RefreshGeneration uint64
	CloseAfterWrite   bool
	Finalize          func(context.Context) error
}

type Adapter struct {
	configPath         string
	now                func() time.Time
	dispatcherFactory  func(config.Config, secrets.Values) Dispatcher
	lifecycleFactory   func(string) (Lifecycle, error)
	dispatcherMu       sync.Mutex
	dispatcher         Dispatcher
	dispatchGeneration uint64
	dispatchStateDir   string
	dispatchBroker     string
	dispatchDesktop    string
	lifecycleMu        sync.Mutex
}

type authorizationWire struct {
	Grant         string `json:"grant"`
	AccessSubject string `json:"access_subject"`
	BrowserID     string `json:"browser_id"`
}

type callArgumentsWire struct {
	Authorization authorizationWire `json:"authorization"`
	Input         json.RawMessage   `json:"input"`
}

type verifiedCall struct {
	config        config.Config
	values        secrets.Values
	authorization authorizationWire
	input         json.RawMessage
}

var hostToolMethods = map[string]struct{}{
	"terminal": {}, "terminal_output": {}, "terminal_sessions": {}, "filesystem_read": {},
	"filesystem_write": {}, "desktop_observe": {}, "desktop_control": {}, "device_status": {},
	"device_permissions": {},
}

var lifecycleMethods = map[string]struct{}{
	"control.status": {}, "control.audit": {}, "control.permissions": {}, "control.rotate": {},
	"control.kill": {}, "control.resume": {},
}

func NewAdapter(options AdapterOptions) (*Adapter, error) {
	if options.ConfigPath == "" {
		return nil, errors.New("relay adapter config path is required")
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.DispatcherFactory == nil {
		options.DispatcherFactory = func(cfg config.Config, values secrets.Values) Dispatcher {
			return dispatch.NewMCP(
				ipc.NewRPCClient(cfg.BrokerEndpoint, []byte(values.BrokerIPCKey)),
				ipc.NewRPCClient(cfg.DesktopEndpoint, []byte(values.DesktopIPCKey)),
			)
		}
	}
	if options.LifecycleFactory == nil {
		options.LifecycleFactory = func(path string) (Lifecycle, error) { return control.Load(path) }
	}
	return &Adapter{
		configPath: options.ConfigPath, now: options.Now, dispatcherFactory: options.DispatcherFactory,
		lifecycleFactory: options.LifecycleFactory,
	}, nil
}

func (a *Adapter) HandleRequest(ctx context.Context, requestID, method string, arguments json.RawMessage) (HandleResult, error) {
	if ctx == nil || requestID == "" || method == "" {
		return HandleResult{}, ErrRequestInvalid
	}
	call, err := a.verifyCall(arguments)
	if err != nil {
		return HandleResult{}, err
	}
	actor := actorHash(call.authorization.AccessSubject, call.authorization.BrowserID)
	auditedMethod := method
	if _, hostMethod := hostToolMethods[method]; !hostMethod {
		if _, lifecycleMethod := lifecycleMethods[method]; !lifecycleMethod {
			auditedMethod = "device.unsupported"
		}
	}
	if disabled(filepath.Join(call.config.StateDir, "disabled")) {
		a.appendAudit(call.config, actor, auditedMethod, "disabled")
		return HandleResult{}, ErrExecutorDisabled
	}
	if _, ok := hostToolMethods[method]; ok {
		var input map[string]any
		if err := decodeStrictJSON(call.input, &input); err != nil {
			a.appendAudit(call.config, actor, method, "rejected")
			return HandleResult{}, ErrRequestInvalid
		}
		dispatcher := a.dispatcherFor(call.config, call.values)
		result, dispatchErr := dispatcher.Dispatch(ctx, mcp.ToolCall{
			SessionID: stableSessionID(call.authorization.AccessSubject, call.authorization.BrowserID),
			Name:      method, Arguments: input,
		})
		if dispatchErr != nil {
			a.appendAudit(call.config, actor, method, "failed")
			return HandleResult{}, fixedDispatchError(dispatchErr)
		}
		payload, err := json.Marshal(result)
		if err != nil {
			a.appendAudit(call.config, actor, method, "failed")
			return HandleResult{}, errors.New("relay result serialization failed")
		}
		a.appendAudit(call.config, actor, method, "succeeded")
		return HandleResult{Payload: payload}, nil
	}
	if _, ok := lifecycleMethods[method]; !ok {
		a.appendAudit(call.config, actor, "device.unsupported", "rejected")
		return HandleResult{}, ErrUnsupportedMethod
	}
	return a.handleLifecycle(ctx, requestID, method, call, actor)
}

func (a *Adapter) HandleRecoveryUnlock(ctx context.Context, requestID string, payload relay.RecoveryUnlockPayload) (HandleResult, error) {
	if ctx == nil || requestID == "" {
		return HandleResult{}, ErrRequestInvalid
	}
	cfg, values, identity, err := a.loadCurrent()
	if err != nil {
		return HandleResult{}, ErrRequestUnauthorized
	}
	contextValue := relay.RecoveryContext{
		DeviceID: cfg.UnifiedDashboard.DeviceID, AccessSubject: payload.Envelope.AccessSubject,
		BrowserID: payload.Envelope.BrowserID, Generation: values.Generation,
	}
	actor := actorHash(contextValue.AccessSubject, contextValue.BrowserID)
	if disabled(filepath.Join(cfg.StateDir, "disabled")) {
		a.appendAudit(cfg, actor, "device.unlock", "disabled")
		return HandleResult{}, ErrExecutorDisabled
	}
	if payload.Envelope.DeviceID != contextValue.DeviceID || payload.Envelope.Generation != contextValue.Generation ||
		relay.VerifyRecoveryEnvelope(identity, payload.Envelope, contextValue, values.VerifyRecoveryKey) != nil {
		a.appendAudit(cfg, actor, "device.unlock", "rejected")
		return HandleResult{}, ErrRequestUnauthorized
	}
	jtiBytes := make([]byte, 32)
	if _, err := rand.Read(jtiBytes); err != nil {
		return HandleResult{}, errors.New("grant generation failed")
	}
	now := a.now().UTC()
	grant, err := relay.SignDeviceGrant(identity, relay.GrantClaims{
		Version: relay.ProtocolVersion, DeviceID: contextValue.DeviceID, AccessSubject: contextValue.AccessSubject,
		BrowserID: contextValue.BrowserID, Generation: contextValue.Generation, IssuedAt: now.Unix(),
		ExpiresAt: now.Add(relay.MaxGrantLifetime).Unix(), JTI: base64.RawURLEncoding.EncodeToString(jtiBytes),
	})
	clear(jtiBytes)
	if err != nil {
		return HandleResult{}, errors.New("grant generation failed")
	}
	encoded, _ := json.Marshal(struct {
		Grant string `json:"grant"`
	}{Grant: grant})
	a.appendAudit(cfg, actor, "device.unlock", "succeeded")
	return HandleResult{Payload: encoded}, nil
}

func (a *Adapter) verifyCall(arguments json.RawMessage) (verifiedCall, error) {
	var wire callArgumentsWire
	if err := decodeStrictJSON(arguments, &wire); err != nil || wire.Authorization.Grant == "" ||
		wire.Authorization.AccessSubject == "" || wire.Authorization.BrowserID == "" || len(wire.Input) == 0 {
		return verifiedCall{}, ErrRequestUnauthorized
	}
	cfg, values, identity, err := a.loadCurrent()
	if err != nil {
		return verifiedCall{}, ErrRequestUnauthorized
	}
	_, err = relay.VerifyDeviceGrant(identity.PublicJWK(), wire.Authorization.Grant, relay.GrantExpectation{
		DeviceID: cfg.UnifiedDashboard.DeviceID, AccessSubject: wire.Authorization.AccessSubject,
		BrowserID: wire.Authorization.BrowserID, Generation: values.Generation, Now: a.now(),
	})
	if err != nil {
		a.appendAudit(cfg, actorHash(wire.Authorization.AccessSubject, wire.Authorization.BrowserID), "device.authorize", "rejected")
		return verifiedCall{}, ErrRequestUnauthorized
	}
	return verifiedCall{config: cfg, values: values, authorization: wire.Authorization, input: wire.Input}, nil
}

func (a *Adapter) handleLifecycle(ctx context.Context, requestID, method string, call verifiedCall, actor string) (HandleResult, error) {
	a.lifecycleMu.Lock()
	defer a.lifecycleMu.Unlock()
	currentCall, err := a.revalidateCall(call)
	if err != nil {
		a.appendAudit(call.config, actor, method, "rejected")
		return HandleResult{}, ErrRequestUnauthorized
	}
	call = currentCall
	if disabled(filepath.Join(call.config.StateDir, "disabled")) {
		a.appendAudit(call.config, actor, method, "disabled")
		return HandleResult{}, ErrExecutorDisabled
	}
	lifecycle, err := a.lifecycleFactory(a.configPath)
	if err != nil {
		a.appendAudit(call.config, actor, method, "failed")
		return HandleResult{}, errors.New("lifecycle unavailable")
	}
	switch method {
	case "control.status":
		controller := localdashboard.NewRuntimeController(call.config, call.values, lifecycle)
		status, err := controller.Snapshot(ctx)
		if err != nil {
			a.appendAudit(call.config, actor, method, "failed")
			return HandleResult{}, errors.New("status unavailable")
		}
		encoded, _ := json.Marshal(status)
		a.appendAudit(call.config, actor, method, "succeeded")
		return HandleResult{Payload: encoded}, nil
	case "control.audit":
		return a.handleAudit(call, actor, method)
	case "control.permissions":
		var input map[string]any
		if err := decodeStrictJSON(call.input, &input); err != nil {
			return HandleResult{}, ErrRequestInvalid
		}
		result, err := a.dispatcherFor(call.config, call.values).Dispatch(ctx, mcp.ToolCall{
			SessionID: stableSessionID(call.authorization.AccessSubject, call.authorization.BrowserID),
			Name:      "device_permissions", Arguments: input,
		})
		if err != nil {
			a.appendAudit(call.config, actor, method, "failed")
			return HandleResult{}, fixedDispatchError(err)
		}
		encoded, _ := json.Marshal(result)
		a.appendAudit(call.config, actor, method, "succeeded")
		return HandleResult{Payload: encoded}, nil
	case "control.resume":
		if err := lifecycle.Resume(ctx); err != nil {
			a.appendAudit(call.config, actor, method, "failed")
			return HandleResult{}, errors.New("lifecycle action failed")
		}
		a.appendAudit(call.config, actor, method, "succeeded")
		return HandleResult{Payload: json.RawMessage(`{"resumed":true}`)}, nil
	case "control.rotate", "control.kill":
		return a.handleSensitiveLifecycle(ctx, requestID, method, call, actor, lifecycle)
	default:
		return HandleResult{}, ErrUnsupportedMethod
	}
}

func (a *Adapter) revalidateCall(call verifiedCall) (verifiedCall, error) {
	cfg, values, identity, err := a.loadCurrent()
	if err != nil {
		return verifiedCall{}, ErrRequestUnauthorized
	}
	_, err = relay.VerifyDeviceGrant(identity.PublicJWK(), call.authorization.Grant, relay.GrantExpectation{
		DeviceID: cfg.UnifiedDashboard.DeviceID, AccessSubject: call.authorization.AccessSubject,
		BrowserID: call.authorization.BrowserID, Generation: values.Generation, Now: a.now(),
	})
	if err != nil {
		return verifiedCall{}, ErrRequestUnauthorized
	}
	call.config = cfg
	call.values = values
	return call, nil
}

func (a *Adapter) handleSensitiveLifecycle(ctx context.Context, requestID, method string, call verifiedCall, actor string, lifecycle Lifecycle) (HandleResult, error) {
	var input map[string]json.RawMessage
	if err := decodeStrictJSON(call.input, &input); err != nil {
		return HandleResult{}, ErrRequestInvalid
	}
	keyJSON := input["response_public_key"]
	if len(keyJSON) == 0 {
		return HandleResult{}, ErrRequestInvalid
	}
	var browserKey relay.PublicKeyJWK
	if err := decodeStrictJSON(keyJSON, &browserKey); err != nil {
		return HandleResult{}, ErrRequestInvalid
	}
	if _, err := relay.ParsePublicKeyJWK(browserKey); err != nil {
		return HandleResult{}, ErrRequestInvalid
	}
	var finalize func(context.Context) error
	finalizeTransferred := false
	defer func() {
		if finalize != nil && !finalizeTransferred {
			runLifecycleFinalizer(finalize)
		}
	}()
	var result control.Result
	var killErr error
	if method == "control.kill" {
		if remoteLifecycle, ok := lifecycle.(remoteKillLifecycle); ok {
			result, finalize, killErr = remoteLifecycle.PrepareRemoteKill(ctx)
		} else {
			result, killErr = lifecycle.Kill(ctx)
		}
	} else {
		result, killErr = lifecycle.Kill(ctx)
	}
	if method == "control.rotate" && killErr == nil {
		killErr = lifecycle.Resume(ctx)
	}
	if result.RecoveryKey == "" || result.URLSecret == "" || result.Dashboard == "" {
		a.appendAudit(call.config, actor, method, "failed")
		return HandleResult{}, errors.New("lifecycle action failed")
	}
	currentCfg, currentValues, _, err := a.loadCurrent()
	if err != nil {
		return HandleResult{}, errors.New("lifecycle result unavailable")
	}
	plaintext, err := json.Marshal(struct {
		RecoveryKey string `json:"recovery_key"`
		URLSecret   string `json:"url_secret"`
		Dashboard   string `json:"dashboard"`
		Partial     bool   `json:"partial,omitempty"`
	}{RecoveryKey: result.RecoveryKey, URLSecret: result.URLSecret, Dashboard: result.Dashboard, Partial: killErr != nil})
	if err != nil {
		return HandleResult{}, errors.New("lifecycle result unavailable")
	}
	envelope, err := relay.SealSensitiveResultEnvelope(browserKey, relay.SensitiveResultContext{
		DeviceID: currentCfg.UnifiedDashboard.DeviceID, AccessSubject: call.authorization.AccessSubject,
		BrowserID: call.authorization.BrowserID, Generation: currentValues.Generation,
		RequestID: requestID, Method: method,
	}, plaintext)
	clear(plaintext)
	if err != nil {
		return HandleResult{}, errors.New("lifecycle result unavailable")
	}
	encoded, _ := json.Marshal(struct {
		SensitiveResult relay.SensitiveResultEnvelope `json:"sensitive_result"`
	}{SensitiveResult: envelope})
	outcome := "succeeded"
	if killErr != nil {
		outcome = "partial"
	}
	a.appendAudit(currentCfg, actor, method, outcome)
	handleResult := HandleResult{
		Payload: encoded, RefreshGeneration: currentValues.Generation,
		CloseAfterWrite: method == "control.kill" || killErr != nil,
		Finalize:        finalize,
	}
	finalizeTransferred = finalize != nil
	return handleResult, nil
}

const lifecycleFinalizeTimeout = 10 * time.Second

func runLifecycleFinalizer(finalize func(context.Context) error) {
	if finalize == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), lifecycleFinalizeTimeout)
	defer cancel()
	_ = finalize(ctx)
}

func (a *Adapter) handleAudit(call verifiedCall, actor, method string) (HandleResult, error) {
	var input struct {
		Limit int `json:"limit"`
	}
	if err := decodeStrictJSON(call.input, &input); err != nil || input.Limit < 0 || input.Limit > 1000 {
		return HandleResult{}, ErrRequestInvalid
	}
	store, err := audit.Open(filepath.Join(call.config.StateDir, "audit.jsonl"), time.Duration(call.config.AuditRetentionH)*time.Hour)
	if err != nil {
		return HandleResult{}, errors.New("audit unavailable")
	}
	events, err := store.List(input.Limit)
	if err != nil {
		return HandleResult{}, errors.New("audit unavailable")
	}
	type publicEvent struct {
		Time    time.Time `json:"time"`
		Actor   string    `json:"actor,omitempty"`
		Method  string    `json:"method"`
		Outcome string    `json:"outcome,omitempty"`
	}
	public := make([]publicEvent, 0, len(events))
	for _, event := range events {
		public = append(public, publicEvent{Time: event.Time, Actor: event.Actor, Method: event.Tool, Outcome: event.Outcome})
	}
	encoded, _ := json.Marshal(map[string]any{"events": public})
	a.appendAudit(call.config, actor, method, "succeeded")
	return HandleResult{Payload: encoded}, nil
}

func (a *Adapter) dispatcherFor(cfg config.Config, values secrets.Values) Dispatcher {
	a.dispatcherMu.Lock()
	defer a.dispatcherMu.Unlock()
	if a.dispatcher == nil ||
		a.dispatchGeneration != values.Generation ||
		a.dispatchStateDir != cfg.StateDir ||
		a.dispatchBroker != cfg.BrokerEndpoint ||
		a.dispatchDesktop != cfg.DesktopEndpoint {
		a.dispatcher = a.dispatcherFactory(cfg, values)
		a.dispatchGeneration = values.Generation
		a.dispatchStateDir = cfg.StateDir
		a.dispatchBroker = cfg.BrokerEndpoint
		a.dispatchDesktop = cfg.DesktopEndpoint
	}
	return a.dispatcher
}

func (a *Adapter) loadCurrent() (config.Config, secrets.Values, *relay.DeviceIdentity, error) {
	cfg, err := config.Load(a.configPath)
	if err != nil || cfg.StateDir == "" || cfg.UnifiedDashboard.DeviceID == "" {
		return config.Config{}, secrets.Values{}, nil, errors.New("runtime unavailable")
	}
	values, err := secrets.Load(cfg.StateDir)
	if err != nil {
		return config.Config{}, secrets.Values{}, nil, errors.New("runtime unavailable")
	}
	identity, err := relay.ParseDeviceIdentity(values.RelayPrivateJWK)
	if err != nil {
		return config.Config{}, secrets.Values{}, nil, errors.New("runtime unavailable")
	}
	return cfg, values, identity, nil
}

func (a *Adapter) appendAudit(cfg config.Config, actor, method, outcome string) {
	store, err := audit.Open(filepath.Join(cfg.StateDir, "audit.jsonl"), time.Duration(cfg.AuditRetentionH)*time.Hour)
	if err != nil {
		return
	}
	_ = store.Append(audit.Event{Actor: actor, Tool: method, Outcome: outcome})
}

func stableSessionID(accessSubject, browserID string) string {
	digest := sha256.Sum256([]byte(accessSubject + "\x00" + browserID))
	return hex.EncodeToString(digest[:])
}

func actorHash(accessSubject, browserID string) string {
	return "relay:" + stableSessionID(accessSubject, browserID)
}

func decodeStrictJSON(data []byte, destination any) error {
	if len(data) == 0 || bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return ErrRequestInvalid
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return ErrRequestInvalid
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return ErrRequestInvalid
	}
	return nil
}

func disabled(path string) bool {
	_, err := os.Stat(path)
	return err == nil || !errors.Is(err, os.ErrNotExist)
}

func fixedDispatchError(_ error) error {
	return errors.New("host action failed")
}
