package daemon

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jamie950315/executor/internal/agent"
	"github.com/jamie950315/executor/internal/ipc"
	"github.com/jamie950315/executor/internal/mcp"
)

func captureDiagnostics(t *testing.T) *bytes.Buffer {
	t.Helper()
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&output, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return &output
}

func TestOAuthAuditFailuresAreDiagnosedWithoutEventData(t *testing.T) {
	for _, openFailure := range []bool{true, false} {
		output := captureDiagnostics(t)
		_, cfg, _ := daemonFixture(t)
		if openFailure {
			cfg.AuditRetentionH = 0
		}
		recorder := newOAuthAuditRecorder(cfg)
		if !openFailure {
			if err := os.Remove(filepath.Join(cfg.StateDir, "audit.jsonl")); err != nil {
				t.Fatal(err)
			}
			recorder(agent.OAuthEvent{Stage: "token", Reason: "SECRET-EVENT-DATA"})
		}
		if !strings.Contains(output.String(), "oauth_audit") {
			t.Errorf("audit failure was silent (open=%v)", openFailure)
		}
		if strings.Contains(output.String(), "SECRET-EVENT-DATA") || strings.Contains(output.String(), cfg.StateDir) {
			t.Fatal("diagnostic disclosed event data or state path")
		}
	}
}

func TestAuditOutcomeFailurePreservesToolResultAndIsDiagnosed(t *testing.T) {
	output := captureDiagnostics(t)
	_, cfg, values := daemonFixture(t)
	dispatcher, err := newAuditedDispatcher(cfg, values)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server := ipc.NewRPCServer(cfg.DesktopEndpoint, []byte(values.DesktopIPCKey), func(_ context.Context, method string, _ []byte) (any, error) {
		if method == "ready" {
			return nil, nil
		}
		if err := os.Remove(filepath.Join(cfg.StateDir, "audit.jsonl")); err != nil {
			return nil, err
		}
		return map[string]any{"completed": true}, nil
	})
	stopped := startDaemon(t, func() error { return server.Serve(ctx) })
	client := ipc.NewRPCClient(cfg.DesktopEndpoint, []byte(values.DesktopIPCKey))
	waitFor(t, func() error { return client.Call(ctx, "ready", nil, nil) })
	result, err := dispatcher(ctx, mcp.ToolCall{Name: "terminal_sessions", Arguments: map[string]any{"action": "list", "privilege": "owner"}})
	cancel()
	assertDaemonStopped(t, stopped)
	if err != nil || result == nil {
		t.Fatalf("completed result was replaced by audit failure: result=%v err=%v", result, err)
	}
	if !strings.Contains(output.String(), "tool_audit_outcome") {
		t.Fatal("audit outcome failure was silent")
	}
	if strings.Contains(output.String(), cfg.StateDir) || strings.Contains(output.String(), "completed") {
		t.Fatal("diagnostic disclosed state path or tool output")
	}
}
