package dispatch

import (
	"context"
	"testing"

	"github.com/jamie950315/executor/internal/desktop"
	"github.com/jamie950315/executor/internal/mcp"
	permissionmodel "github.com/jamie950315/executor/internal/permissions"
)

func TestMCPPermissionContractSeparatesDesktopPrerequisites(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name, platform, capture, input string
		states                         map[string]permissionmodel.State
		restart                        bool
	}{
		{"headless", "linux", "permissions_required", "permissions_required", map[string]permissionmodel.State{"desktop_session": permissionmodel.StateUnavailable, "screen_recording": permissionmodel.StateUnavailable, "keyboard_input": permissionmodel.StateUnavailable, "pointer_input": permissionmodel.StateUnavailable}, false},
		{"mac_capture_only", "macos", "permissions_ready", "permissions_required", map[string]permissionmodel.State{"screen_recording": permissionmodel.StateGranted, "accessibility": permissionmodel.StateDenied, "input_control": permissionmodel.StateDenied}, false},
		{"windows_locked", "windows", "permissions_required", "permissions_required", map[string]permissionmodel.State{"desktop_session": permissionmodel.StateUnavailable, "screen_recording": permissionmodel.StateNotRequired, "input_control": permissionmodel.StateNotRequired}, false},
		{"windows_unlocked", "windows", "permissions_ready", "permissions_ready", map[string]permissionmodel.State{"desktop_session": permissionmodel.StateGranted, "screen_recording": permissionmodel.StateNotRequired, "input_control": permissionmodel.StateNotRequired}, false},
		{"incomplete", "darwin", "not_checked", "not_checked", nil, false},
		{"restart", "darwin", "restart_required", "restart_required", map[string]permissionmodel.State{"screen_recording": permissionmodel.StateGranted, "accessibility": permissionmodel.StateGranted, "input_control": permissionmodel.StateGranted}, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			items := make([]permissionmodel.Item, 0, len(test.states))
			for id, state := range test.states {
				items = append(items, permissionmodel.Item{ID: id, Label: id, State: state, Required: true})
			}
			report := permissionmodel.NewReport(test.platform, false, items)
			report.RestartRequired = test.restart
			caller := &recordingCaller{responses: map[string]any{desktop.RPCMethodDesktopPermissions: report}}
			result, err := NewMCP(nil, caller).Dispatch(context.Background(), mcp.ToolCall{Name: "device_permissions", Arguments: map[string]any{"action": "status"}})
			if err != nil {
				t.Fatal(err)
			}
			value := result.(map[string]any)
			if value["scope"] != "desktop" || value["ready"] != report.Ready {
				t.Fatalf("permission scope or legacy readiness = %#v", value)
			}
			capabilities, ok := value["capabilities"].(map[string]any)
			if !ok {
				t.Fatalf("missing capability breakdown: %#v", value)
			}
			for key, want := range map[string]string{"filesystem": "not_checked", "terminal": "not_checked", "desktopCapture": test.capture, "desktopInput": test.input} {
				if capabilities[key] != want {
					t.Errorf("%s = %v, want %s", key, capabilities[key], want)
				}
			}
			if len(caller.calls) != 1 || caller.calls[0].method != desktop.RPCMethodDesktopPermissions {
				t.Fatalf("permission status ran unrelated probes: %#v", caller.calls)
			}
		})
	}
}

func TestMCPPermissionContractRejectsMalformedHelperReport(t *testing.T) {
	t.Parallel()
	for _, report := range []any{nil, true, map[string]any{"ok": true}, map[string]any{"platform": "linux", "requested": false, "ready": "false", "permissions": []any{}}} {
		caller := &recordingCaller{responses: map[string]any{desktop.RPCMethodDesktopPermissions: report}}
		if _, err := NewMCP(nil, caller).Dispatch(context.Background(), mcp.ToolCall{Name: "device_permissions", Arguments: map[string]any{"action": "status"}}); err == nil {
			t.Errorf("malformed report accepted: %#v", report)
		}
	}
}
