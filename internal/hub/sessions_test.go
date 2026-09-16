package hub

import (
	"context"
	"errors"
	"testing"

	"github.com/jamie950315/executor/internal/mcp"
)

type sessionFixture struct {
	calls       int
	last        mcp.ToolCall
	fail        bool
	wrongResult bool
}

func (f *sessionFixture) Devices(context.Context) ([]Device, error) {
	return []Device{{ID: "mac", Online: true, Authorized: true}, {ID: "win", Online: true, Authorized: true}}, nil
}
func (f *sessionFixture) Call(_ context.Context, _ string, c mcp.ToolCall) (any, error) {
	f.calls++
	f.last = c
	if f.fail {
		return nil, errors.New("response lost")
	}
	if c.Name == "terminal" && c.Arguments["action"] == "create" {
		return map[string]any{"ID": "remote-id", "PID": 123}, nil
	}
	if c.Name == "terminal_sessions" {
		item := map[string]any{"Session": map[string]any{"ID": "remote-id"}, "Running": false, "exitCode": 0}
		if f.wrongResult {
			item["Session"] = map[string]any{"ID": "different-session"}
		}
		if c.Arguments["action"] == "list" {
			return []any{item, map[string]any{"Session": map[string]any{"ID": "someone-else"}}}, nil
		}
		return item, nil
	}
	return map[string]any{"ok": true}, nil
}

func TestHubRejectsMismatchedSessionInspection(t *testing.T) {
	f := &sessionFixture{}
	r := New(f)
	result, err := r.Dispatch(context.Background(), mcp.ToolCall{Name: "terminal", SessionID: "alice", Arguments: map[string]any{"deviceId": "mac", "action": "create"}})
	if err != nil {
		t.Fatal(err)
	}
	id := result.(map[string]any)["ID"].(string)
	f.wrongResult = true
	_, err = r.Dispatch(context.Background(), mcp.ToolCall{Name: "terminal_sessions", SessionID: "alice", Arguments: map[string]any{"deviceId": "mac", "action": "inspect", "sessionId": id}})
	if err == nil {
		t.Fatal("mismatched session response accepted")
	}
}

func TestHubSessionCannotCrossDeviceCallerOrPrivilege(t *testing.T) {
	f := &sessionFixture{}
	r := New(f)
	created, err := r.Dispatch(context.Background(), mcp.ToolCall{Name: "terminal", SessionID: "alice", Arguments: map[string]any{"deviceId": "mac", "action": "create"}})
	if err != nil {
		t.Fatal(err)
	}
	id := created.(map[string]any)["ID"].(string)
	if id == "remote-id" {
		t.Fatal("raw remote session exposed")
	}
	for _, test := range []struct{ device, caller, privilege, id string }{{"win", "alice", "owner", id}, {"mac", "bob", "owner", id}, {"mac", "alice", "admin", id}, {"mac", "alice", "owner", "remote-id"}} {
		before := f.calls
		_, err := r.Dispatch(context.Background(), mcp.ToolCall{Name: "terminal_output", SessionID: test.caller, Arguments: map[string]any{"deviceId": test.device, "privilege": test.privilege, "sessionId": test.id}})
		if err == nil || f.calls != before {
			t.Fatal("cross-scope session reached relay")
		}
	}
	_, err = r.Dispatch(context.Background(), mcp.ToolCall{Name: "terminal_output", SessionID: "alice", Arguments: map[string]any{"deviceId": "mac", "sessionId": id}})
	if err != nil || f.last.Arguments["sessionId"] != "remote-id" {
		t.Fatal("session translation failed")
	}
}

func TestHubSessionListingFiltersAndCloseRetainsUnconfirmedBinding(t *testing.T) {
	f := &sessionFixture{}
	r := New(f)
	result, err := r.Dispatch(context.Background(), mcp.ToolCall{Name: "terminal", SessionID: "alice", Arguments: map[string]any{"deviceId": "mac", "action": "create"}})
	if err != nil {
		t.Fatal(err)
	}
	id := result.(map[string]any)["ID"].(string)
	list, err := r.Dispatch(context.Background(), mcp.ToolCall{Name: "terminal_sessions", SessionID: "alice", Arguments: map[string]any{"deviceId": "mac", "action": "list"}})
	if err != nil || len(list.([]any)) != 1 || list.([]any)[0].(map[string]any)["Session"].(map[string]any)["ID"] != id {
		t.Fatal("session listing isolation failed")
	}
	closeCall := mcp.ToolCall{Name: "terminal", SessionID: "alice", Arguments: map[string]any{"deviceId": "mac", "action": "close", "sessionId": id}}
	f.fail = true
	if _, err := r.Dispatch(context.Background(), closeCall); !errors.Is(err, ErrUnconfirmed) {
		t.Fatal("expected unconfirmed close")
	}
	f.fail = false
	if _, err := r.Dispatch(context.Background(), closeCall); err != nil {
		t.Fatal("binding was discarded after unconfirmed close")
	}
	before := f.calls
	if _, err := r.Dispatch(context.Background(), closeCall); err == nil || f.calls != before {
		t.Fatal("closed binding still accepted")
	}
}
