package hub

import (
	"context"
	"errors"
	"testing"

	"github.com/jamie950315/executor/internal/mcp"
)

type fixture struct {
	devices        []Device
	lookups, calls int
	target         string
	call           mcp.ToolCall
	failure        error
}

func (f *fixture) Devices(context.Context) ([]Device, error) { f.lookups++; return f.devices, nil }
func (f *fixture) Call(_ context.Context, id string, call mcp.ToolCall) (any, error) {
	f.calls++
	f.target = id
	f.call = call
	return "ok", f.failure
}

func TestRelayOnlyExplicitRouting(t *testing.T) {
	f := &fixture{devices: []Device{{ID: "mac", Online: true, Authorized: true}, {ID: "windows", Online: true, Authorized: true}}}
	r := New(f)
	args := map[string]any{"deviceId": "windows", "action": "write_file", "path": "fixture.txt", "content": "test"}
	result, err := r.Dispatch(context.Background(), mcp.ToolCall{Name: "filesystem_write", Arguments: args, SessionID: "caller"})
	if err != nil || result != "ok" || f.target != "windows" || f.calls != 1 {
		t.Fatal("explicit routing failed")
	}
	if _, ok := f.call.Arguments["deviceId"]; ok {
		t.Fatal("routing metadata leaked upstream")
	}
	if args["deviceId"] != "windows" {
		t.Fatal("input mutated")
	}
}

func TestRejectsMissingUnknownOfflineAndUnauthorizedTargets(t *testing.T) {
	for _, id := range []any{nil, "", "unknown", "offline", "locked", 123} {
		f := &fixture{devices: []Device{{ID: "offline", Authorized: true}, {ID: "locked", Online: true}}}
		_, err := New(f).Dispatch(context.Background(), mcp.ToolCall{Name: "filesystem_write", Arguments: map[string]any{"deviceId": id}})
		if err == nil || f.calls != 0 {
			t.Fatalf("unsafe dispatch for %v", id)
		}
	}
}

func TestRefreshesDirectoryAndNeverRetries(t *testing.T) {
	f := &fixture{devices: []Device{{ID: "mac", Online: true, Authorized: true}}, failure: errors.New("connection lost")}
	r := New(f)
	call := mcp.ToolCall{Name: "filesystem_write", Arguments: map[string]any{"deviceId": "mac"}}
	if _, err := r.Dispatch(context.Background(), call); !errors.Is(err, ErrUnconfirmed) {
		t.Fatal("lost response must be unconfirmed")
	}
	if f.calls != 1 {
		t.Fatal("operation retried")
	}
	f.devices = nil
	if _, err := r.Dispatch(context.Background(), call); err == nil || f.calls != 1 || f.lookups != 2 {
		t.Fatal("stale directory used")
	}
}

func TestToolsRequireDeviceAndPreserveWriteAnnotations(t *testing.T) {
	for _, tool := range Tools() {
		if tool.Name == "devices_list" {
			continue
		}
		required := tool.InputSchema["required"].([]string)
		found := false
		for _, name := range required {
			found = found || name == "deviceId"
		}
		if !found {
			t.Fatalf("%s missing deviceId", tool.Name)
		}
		if tool.Name == "filesystem_write" && tool.Annotations.ReadOnlyHint {
			t.Fatal("write relabeled as read")
		}
	}
}
