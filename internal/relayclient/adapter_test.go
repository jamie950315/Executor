package relayclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jamie950315/executor/internal/config"
	"github.com/jamie950315/executor/internal/control"
	"github.com/jamie950315/executor/internal/mcp"
	"github.com/jamie950315/executor/internal/relay"
	"github.com/jamie950315/executor/internal/secrets"
)

type recordingDispatcher struct {
	calls []mcp.ToolCall
}

func (d *recordingDispatcher) Dispatch(_ context.Context, call mcp.ToolCall) (any, error) {
	d.calls = append(d.calls, call)
	return map[string]any{"marker": "SENSITIVE_RESULT_MARKER", "ok": true}, nil
}

type rotatingLifecycle struct {
	stateDir string
	kills    int
	resumes  int
}

func (l *rotatingLifecycle) Kill(context.Context) (control.Result, error) {
	l.kills++
	values, err := secrets.Rotate(l.stateDir)
	if err != nil {
		return control.Result{}, err
	}
	return control.Result{
		RecoveryKey: values.RecoveryKey,
		URLSecret:   values.URLSecret,
		Dashboard:   "http://127.0.0.1:8788/?token=" + values.DashboardKey,
	}, nil
}

func (l *rotatingLifecycle) Resume(context.Context) error {
	l.resumes++
	return nil
}

func TestAdapterRevalidatesGrantBeforeDispatchAndCachesByGeneration(t *testing.T) {
	t.Parallel()
	configPath, cfg, values := relayFixture(t)
	dispatchers := []*recordingDispatcher{}
	adapter, err := NewAdapter(AdapterOptions{
		ConfigPath: configPath,
		Now:        func() time.Time { return time.Unix(1_700_000_000, 0) },
		DispatcherFactory: func(config.Config, secrets.Values) Dispatcher {
			dispatcher := &recordingDispatcher{}
			dispatchers = append(dispatchers, dispatcher)
			return dispatcher
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	grant := signGrantForTest(t, values, cfg, "access-1", "browser-1", time.Unix(1_700_000_000, 0))
	arguments := callArgumentsForTest(t, grant, "access-1", "browser-1", map[string]any{
		"action": "summary", "marker": "SENSITIVE_ARGUMENT_MARKER",
	})

	first, err := adapter.HandleRequest(context.Background(), "request-1", "device_status", arguments)
	if err != nil {
		t.Fatalf("HandleRequest: %v", err)
	}
	if string(first.Payload) != `{"marker":"SENSITIVE_RESULT_MARKER","ok":true}` {
		t.Fatalf("payload = %s", first.Payload)
	}
	if len(dispatchers) != 1 || len(dispatchers[0].calls) != 1 {
		t.Fatalf("dispatchers = %#v", dispatchers)
	}
	call := dispatchers[0].calls[0]
	if call.Name != "device_status" || call.SessionID != stableSessionID("access-1", "browser-1") || call.Arguments["action"] != "summary" {
		t.Fatalf("dispatch call = %#v", call)
	}
	if _, err := adapter.HandleRequest(context.Background(), "request-2", "device_status", arguments); err != nil {
		t.Fatal(err)
	}
	if len(dispatchers) != 1 || len(dispatchers[0].calls) != 2 {
		t.Fatal("same generation did not reuse the dispatcher and capture state")
	}
	auditBytes, err := os.ReadFile(filepath.Join(cfg.StateDir, "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"SENSITIVE_ARGUMENT_MARKER", "SENSITIVE_RESULT_MARKER", grant, "access-1", "browser-1"} {
		if bytes.Contains(auditBytes, []byte(forbidden)) {
			t.Fatalf("dispatch audit exposed sensitive value: %s", auditBytes)
		}
	}

	if _, err := secrets.Rotate(cfg.StateDir); err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.HandleRequest(context.Background(), "request-old", "device_status", arguments); !errors.Is(err, ErrRequestUnauthorized) {
		t.Fatalf("old grant error = %v", err)
	}
	if len(dispatchers) != 1 || len(dispatchers[0].calls) != 2 {
		t.Fatal("revoked grant reached host dispatch")
	}
	current, err := secrets.Load(cfg.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	newGrant := signGrantForTest(t, current, cfg, "access-1", "browser-1", time.Unix(1_700_000_000, 0))
	newArguments := callArgumentsForTest(t, newGrant, "access-1", "browser-1", map[string]any{"action": "summary"})
	if _, err := adapter.HandleRequest(context.Background(), "request-new", "device_status", newArguments); err != nil {
		t.Fatal(err)
	}
	if len(dispatchers) != 2 || len(dispatchers[1].calls) != 1 {
		t.Fatal("new generation did not rebuild the dispatcher")
	}
}

func TestAdapterRejectsWrongContextAndUnsupportedMethodBeforeAction(t *testing.T) {
	t.Parallel()
	configPath, cfg, values := relayFixture(t)
	dispatcher := &recordingDispatcher{}
	adapter, err := NewAdapter(AdapterOptions{
		ConfigPath:        configPath,
		Now:               func() time.Time { return time.Unix(1_700_000_000, 0) },
		DispatcherFactory: func(config.Config, secrets.Values) Dispatcher { return dispatcher },
	})
	if err != nil {
		t.Fatal(err)
	}
	grant := signGrantForTest(t, values, cfg, "access-1", "browser-1", time.Unix(1_700_000_000, 0))
	wrongContext := callArgumentsForTest(t, grant, "access-2", "browser-1", map[string]any{"path": "/"})
	if _, err := adapter.HandleRequest(context.Background(), "request-1", "filesystem_read", wrongContext); !errors.Is(err, ErrRequestUnauthorized) {
		t.Fatalf("wrong context error = %v", err)
	}
	valid := callArgumentsForTest(t, grant, "access-1", "browser-1", map[string]any{})
	if _, err := adapter.HandleRequest(context.Background(), "request-2", "format_disk", valid); !errors.Is(err, ErrUnsupportedMethod) {
		t.Fatalf("unsupported method error = %v", err)
	}
	if len(dispatcher.calls) != 0 {
		t.Fatalf("rejected requests reached dispatcher: %#v", dispatcher.calls)
	}
}

func TestAdapterRecoveryUnlockVerifiesLocalKeyAndReturnsBoundThirtyDayGrant(t *testing.T) {
	t.Parallel()
	configPath, cfg, values := relayFixture(t)
	now := time.Unix(1_700_000_000, 0)
	adapter, err := NewAdapter(AdapterOptions{ConfigPath: configPath, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	identity, err := relay.ParseDeviceIdentity(values.RelayPrivateJWK)
	if err != nil {
		t.Fatal(err)
	}
	contextValue := relay.RecoveryContext{
		DeviceID: cfg.UnifiedDashboard.DeviceID, AccessSubject: "access-1", BrowserID: "browser-1", Generation: values.Generation,
	}
	envelope, err := relay.SealRecoveryEnvelope(identity.PublicJWK(), contextValue, values.RecoveryKey)
	if err != nil {
		t.Fatal(err)
	}
	result, err := adapter.HandleRecoveryUnlock(context.Background(), "unlock-1", relay.RecoveryUnlockPayload{Envelope: envelope})
	if err != nil {
		t.Fatalf("HandleRecoveryUnlock: %v", err)
	}
	var payload struct {
		Grant string `json:"grant"`
	}
	if err := json.Unmarshal(result.Payload, &payload); err != nil || payload.Grant == "" {
		t.Fatalf("unlock payload = %s err=%v", result.Payload, err)
	}
	claims, err := relay.VerifyDeviceGrant(identity.PublicJWK(), payload.Grant, relay.GrantExpectation{
		DeviceID: cfg.UnifiedDashboard.DeviceID, AccessSubject: "access-1", BrowserID: "browser-1",
		Generation: values.Generation, Now: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if claims.ExpiresAt-claims.IssuedAt != int64(relay.MaxGrantLifetime/time.Second) || claims.JTI == "" {
		t.Fatalf("grant claims = %#v", claims)
	}

	wrong, err := relay.SealRecoveryEnvelope(identity.PublicJWK(), contextValue, "wrong-recovery-key")
	if err != nil {
		t.Fatal(err)
	}
	if rejected, err := adapter.HandleRecoveryUnlock(context.Background(), "unlock-2", relay.RecoveryUnlockPayload{Envelope: wrong}); !errors.Is(err, ErrRequestUnauthorized) || len(rejected.Payload) != 0 {
		t.Fatalf("wrong recovery result = %#v error=%v", rejected, err)
	}
}

func TestAdapterEncryptsRotateAndKillMaterialForBrowserAndAuditsOnlyMetadata(t *testing.T) {
	t.Parallel()
	for _, method := range []string{"control.rotate", "control.kill"} {
		t.Run(method, func(t *testing.T) {
			configPath, cfg, values := relayFixture(t)
			now := time.Unix(1_700_000_000, 0)
			lifecycle := &rotatingLifecycle{stateDir: cfg.StateDir}
			adapter, err := NewAdapter(AdapterOptions{
				ConfigPath: configPath, Now: func() time.Time { return now },
				LifecycleFactory: func(string) (Lifecycle, error) { return lifecycle, nil },
			})
			if err != nil {
				t.Fatal(err)
			}
			browser, err := relay.GenerateDeviceIdentity()
			if err != nil {
				t.Fatal(err)
			}
			grant := signGrantForTest(t, values, cfg, "access-1", "browser-1", now)
			arguments := callArgumentsForTest(t, grant, "access-1", "browser-1", map[string]any{
				"response_public_key": browser.PublicJWK(), "marker": "SENSITIVE_LIFECYCLE_ARGUMENT_MARKER",
			})
			result, err := adapter.HandleRequest(context.Background(), "lifecycle-request", method, arguments)
			if err != nil {
				t.Fatalf("HandleRequest: %v", err)
			}
			if result.RefreshGeneration != values.Generation+1 || result.CloseAfterWrite != (method == "control.kill") {
				t.Fatalf("lifecycle result = %#v", result)
			}
			var encrypted struct {
				SensitiveResult relay.SensitiveResultEnvelope `json:"sensitive_result"`
			}
			if err := json.Unmarshal(result.Payload, &encrypted); err != nil {
				t.Fatal(err)
			}
			plaintext, err := relay.OpenSensitiveResultEnvelope(browser, encrypted.SensitiveResult, relay.SensitiveResultContext{
				DeviceID: cfg.UnifiedDashboard.DeviceID, AccessSubject: "access-1", BrowserID: "browser-1",
				Generation: values.Generation + 1, RequestID: "lifecycle-request", Method: method,
			})
			if err != nil {
				t.Fatal(err)
			}
			var credentials struct {
				RecoveryKey string `json:"recovery_key"`
				URLSecret   string `json:"url_secret"`
				Dashboard   string `json:"dashboard"`
			}
			if err := json.Unmarshal(plaintext, &credentials); err != nil || credentials.RecoveryKey == "" || credentials.URLSecret == "" || credentials.Dashboard == "" {
				t.Fatalf("decrypted credentials = %#v err=%v", credentials, err)
			}
			if method == "control.rotate" && lifecycle.resumes != 1 {
				t.Fatal("rotate did not resume before returning")
			}
			if method == "control.kill" && lifecycle.resumes != 0 {
				t.Fatal("kill unexpectedly resumed")
			}
			auditBytes, err := os.ReadFile(filepath.Join(cfg.StateDir, "audit.jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			for _, forbidden := range []string{
				"SENSITIVE_LIFECYCLE_ARGUMENT_MARKER", credentials.RecoveryKey, credentials.URLSecret,
				credentials.Dashboard, grant, "access-1", "browser-1",
			} {
				if forbidden != "" && bytes.Contains(auditBytes, []byte(forbidden)) {
					t.Fatalf("audit exposed sensitive value: %s", auditBytes)
				}
			}
		})
	}
}

func signGrantForTest(t *testing.T, values secrets.Values, cfg config.Config, accessSubject, browserID string, now time.Time) string {
	t.Helper()
	identity, err := relay.ParseDeviceIdentity(values.RelayPrivateJWK)
	if err != nil {
		t.Fatal(err)
	}
	grant, err := relay.SignDeviceGrant(identity, relay.GrantClaims{
		Version: relay.ProtocolVersion, DeviceID: cfg.UnifiedDashboard.DeviceID, AccessSubject: accessSubject,
		BrowserID: browserID, Generation: values.Generation, IssuedAt: now.Unix(),
		ExpiresAt: now.Add(time.Hour).Unix(), JTI: "test-grant-jti",
	})
	if err != nil {
		t.Fatal(err)
	}
	return grant
}

func callArgumentsForTest(t *testing.T, grant, accessSubject, browserID string, input any) json.RawMessage {
	t.Helper()
	encoded, err := json.Marshal(map[string]any{
		"authorization": map[string]any{"grant": grant, "access_subject": accessSubject, "browser_id": browserID},
		"input":         input,
	})
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}
