package hub

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jamie950315/executor/internal/mcp"
)

func TestDashboardRelayUsesMachineAuthAndExplicitDeviceRoute(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-machine-token" || r.Header.Get("Cookie") != "" {
			t.Error("wrong machine authentication")
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/hub/devices":
			json.NewEncoder(w).Encode(map[string]any{"devices": []Device{{ID: "mac", Online: true, Authorized: true}, {ID: "win", Online: true, Authorized: true}}})
		case "/api/hub/devices/win/call":
			calls++
			var body struct {
				Method    string         `json:"method"`
				Arguments map[string]any `json:"arguments"`
				SessionID string         `json:"session_id"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if body.Method != "filesystem_write" || body.SessionID != "caller-1" || body.Arguments["deviceId"] != nil {
				t.Error("wrong relay envelope")
			}
			json.NewEncoder(w).Encode(map[string]any{"ok": true, "writtenBytes": 3})
		default:
			t.Error("unexpected path")
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	relay, err := NewDashboardRelay(server.URL, "test-machine-token", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	result, err := New(relay).Dispatch(context.Background(), mcp.ToolCall{Name: "filesystem_write", SessionID: "caller-1", Arguments: map[string]any{"deviceId": "win", "content": "abc"}})
	if err != nil || calls != 1 || result.(map[string]any)["writtenBytes"].(float64) != 3 {
		t.Fatalf("relay round trip failed: %v", err)
	}
}

func TestDashboardRelayNeverFollowsRedirectOrLeaksDiagnosticBody(t *testing.T) {
	reached := false
	destination := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached = true }))
	defer destination.Close()
	for _, status := range []int{302, 307, 401, 503} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Location", destination.URL)
			w.WriteHeader(status)
			w.Write([]byte("private diagnostic secret"))
		}))
		relay, err := NewDashboardRelay(server.URL, "test-machine-token", server.Client())
		if err != nil {
			t.Fatal(err)
		}
		_, err = relay.Call(context.Background(), "mac", mcp.ToolCall{Name: "filesystem_write"})
		if err == nil || strings.Contains(err.Error(), "secret") || reached {
			t.Fatal("redirect/error boundary failed")
		}
		server.Close()
	}
}

func TestDashboardRelayRejectsUnsafeOriginsAndMissingCredential(t *testing.T) {
	for _, origin := range []string{"http://example.com", "https://u:p@example.com", "https://example.com?token=x", "https://example.com/path", "https://example.com#x"} {
		if _, err := NewDashboardRelay(origin, "test-token", nil); err == nil {
			t.Fatalf("accepted %s", origin)
		}
	}
	if _, err := NewDashboardRelay("https://dashboard.example.com", "", nil); err == nil {
		t.Fatal("accepted missing credential")
	}
}
