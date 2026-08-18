package dispatch

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jamie950315/executor/internal/desktop"
	"github.com/jamie950315/executor/internal/mcp"
)

func TestMCPRoutesOwnerAndAdminTerminalToSeparateHelpers(t *testing.T) {
	t.Parallel()

	broker := &recordingCaller{responses: map[string]any{"terminal.start": map[string]any{"id": "admin-1"}}}
	desktop := &recordingCaller{responses: map[string]any{"terminal.start": map[string]any{"id": "owner-1"}}}
	dispatcher := NewMCP(broker, desktop)

	owner, err := dispatcher.Dispatch(context.Background(), mcp.ToolCall{Name: "terminal", Arguments: map[string]any{
		"action": "create", "privilege": "owner", "cwd": "/tmp", "command": "pwd",
	}})
	if err != nil {
		t.Fatalf("owner terminal: %v", err)
	}
	if owner.(map[string]any)["id"] != "owner-1" || len(desktop.calls) != 1 || len(broker.calls) != 0 {
		t.Fatalf("owner routing mismatch: owner=%#v desktop=%#v broker=%#v", owner, desktop.calls, broker.calls)
	}

	admin, err := dispatcher.Dispatch(context.Background(), mcp.ToolCall{Name: "terminal", Arguments: map[string]any{
		"action": "create", "privilege": "admin", "command": "whoami",
	}})
	if err != nil {
		t.Fatalf("admin terminal: %v", err)
	}
	if admin.(map[string]any)["id"] != "admin-1" || len(broker.calls) != 1 {
		t.Fatalf("admin routing mismatch: admin=%#v broker=%#v", admin, broker.calls)
	}
}

func TestMCPRoutesFilesystemAndDesktopActions(t *testing.T) {
	t.Parallel()

	broker := &recordingCaller{responses: map[string]any{"filesystem.read": []byte("root")}}
	desktop := &recordingCaller{responses: map[string]any{
		"filesystem.read": []byte("owner"),
		"desktop.windows": []map[string]any{{"app": "Finder", "title": "Desktop"}},
	}}
	dispatcher := NewMCP(broker, desktop)

	if _, err := dispatcher.Dispatch(context.Background(), mcp.ToolCall{Name: "filesystem_read", Arguments: map[string]any{
		"action": "read_file", "path": "/etc/hosts", "privilege": "admin",
	}}); err != nil {
		t.Fatalf("admin read: %v", err)
	}
	if got := broker.calls[0].method; got != "filesystem.read" {
		t.Fatalf("admin method = %q", got)
	}

	if _, err := dispatcher.Dispatch(context.Background(), mcp.ToolCall{Name: "desktop_observe", Arguments: map[string]any{
		"action": "windows",
	}}); err != nil {
		t.Fatalf("desktop windows: %v", err)
	}
	if got := desktop.calls[0].method; got != "desktop.windows" {
		t.Fatalf("desktop method = %q", got)
	}
}

func TestMCPDesktopScreenshotReturnsImageAndCaptureMetadata(t *testing.T) {
	t.Parallel()

	pngBytes := []byte("png-image-bytes")
	desktop := &recordingCaller{responses: map[string]any{
		"desktop.capture": map[string]any{
			"data":      pngBytes,
			"mime_type": "image/png",
			"width":     1440,
			"height":    900,
		},
	}}
	dispatcher := NewMCP(nil, desktop)

	result, err := dispatcher.Dispatch(context.Background(), mcp.ToolCall{
		SessionID: "mcp-session-1",
		Name:      "desktop_observe",
		Arguments: map[string]any{"action": "screenshot"},
	})
	if err != nil {
		t.Fatalf("desktop screenshot: %v", err)
	}

	toolResult, ok := result.(mcp.ToolResult)
	if !ok {
		t.Fatalf("desktop screenshot result = %T, want mcp.ToolResult", result)
	}
	metadata, ok := toolResult.StructuredContent.(map[string]any)
	if !ok {
		t.Fatalf("structured content = %T, want map", toolResult.StructuredContent)
	}
	if metadata["captureId"] == "" || metadata["width"] != 1440 || metadata["height"] != 900 || metadata["mimeType"] != "image/png" {
		t.Fatalf("unexpected capture metadata: %#v", metadata)
	}
	wantContent := []any{
		map[string]any{
			"type": "text",
			"text": "Screenshot captured. Use captureId " + metadata["captureId"].(string) + " for the next desktop_control batch. 1440x900 image/png.",
		},
		map[string]any{
			"type":     "image",
			"data":     base64.StdEncoding.EncodeToString(pngBytes),
			"mimeType": "image/png",
			"_meta":    map[string]any{"codex/imageDetail": "original"},
		},
	}
	if got, _ := json.Marshal(toolResult.Content); string(got) != mustJSON(t, wantContent) {
		t.Fatalf("image content = %s, want %s", got, mustJSON(t, wantContent))
	}
	if len(desktop.calls) != 1 || desktop.calls[0].method != "desktop.capture" {
		t.Fatalf("desktop calls = %#v, want one desktop.capture", desktop.calls)
	}
}

func TestMCPDesktopScreenshotAcceptsDetailedPNGAboveTenMiB(t *testing.T) {
	pngBytes := make([]byte, (11<<20)+1)
	desktopCaller := &recordingCaller{responses: map[string]any{
		"desktop.capture": map[string]any{
			"data": pngBytes, "mime_type": "image/png", "width": 3840, "height": 2160,
		},
	}}
	result, err := NewMCP(nil, desktopCaller).Dispatch(context.Background(), mcp.ToolCall{
		SessionID: "large-screen", Name: "desktop_observe", Arguments: map[string]any{"action": "screenshot"},
	})
	if err != nil {
		t.Fatalf("large desktop screenshot: %v", err)
	}
	content := result.(mcp.ToolResult).Content
	if len(content) != 2 || content[1].(map[string]any)["type"] != "image" {
		t.Fatalf("large desktop screenshot content = %#v, want text then image", content)
	}
}

func TestMCPDesktopBatchRejectsStaleCaptureBeforeIPC(t *testing.T) {
	t.Parallel()

	desktop := &recordingCaller{responses: map[string]any{
		"desktop.capture": map[string]any{
			"data":      []byte("png"),
			"mime_type": "image/png",
			"width":     100,
			"height":    50,
		},
	}}
	dispatcher := NewMCP(nil, desktop)

	first, err := dispatcher.Dispatch(context.Background(), mcp.ToolCall{
		SessionID: "mcp-session-1",
		Name:      "desktop_observe",
		Arguments: map[string]any{"action": "screenshot"},
	})
	if err != nil {
		t.Fatalf("first screenshot: %v", err)
	}
	staleCaptureID := first.(mcp.ToolResult).StructuredContent.(map[string]any)["captureId"].(string)
	if _, err := dispatcher.Dispatch(context.Background(), mcp.ToolCall{
		SessionID: "mcp-session-1",
		Name:      "desktop_observe",
		Arguments: map[string]any{"action": "screenshot"},
	}); err != nil {
		t.Fatalf("second screenshot: %v", err)
	}
	desktop.calls = nil

	_, err = dispatcher.Dispatch(context.Background(), mcp.ToolCall{
		SessionID: "mcp-session-1",
		Name:      "desktop_control",
		Arguments: map[string]any{
			"action":    "batch",
			"captureId": staleCaptureID,
			"actions": []any{
				map[string]any{"type": "click", "x": 10, "y": 20, "button": "left"},
			},
		},
	})
	if err == nil {
		t.Fatal("stale capture batch succeeded")
	}
	if len(desktop.calls) != 0 {
		t.Fatalf("stale capture reached desktop IPC: %#v", desktop.calls)
	}
}

func TestMCPDesktopBatchExecutesActionsThenReturnsFreshCapture(t *testing.T) {
	t.Parallel()

	desktop := &recordingCaller{responses: map[string]any{
		"desktop.capture": map[string]any{
			"data":      []byte("png"),
			"mime_type": "image/png",
			"width":     1440,
			"height":    900,
		},
		"desktop.actions": map[string]any{"ok": true},
	}}
	dispatcher := NewMCP(nil, desktop)

	observed, err := dispatcher.Dispatch(context.Background(), mcp.ToolCall{
		SessionID: "mcp-session-1",
		Name:      "desktop_observe",
		Arguments: map[string]any{"action": "screenshot"},
	})
	if err != nil {
		t.Fatalf("desktop screenshot: %v", err)
	}
	originalCaptureID := observed.(mcp.ToolResult).StructuredContent.(map[string]any)["captureId"].(string)
	desktop.calls = nil

	result, err := dispatcher.Dispatch(context.Background(), mcp.ToolCall{
		SessionID: "mcp-session-1",
		Name:      "desktop_control",
		Arguments: map[string]any{
			"action":    "batch",
			"captureId": originalCaptureID,
			"actions": []any{
				map[string]any{"type": "click", "x": 10, "y": 20, "button": "left", "keys": []any{"CTRL"}},
				map[string]any{"type": "drag", "path": []any{map[string]any{"x": 1, "y": 2}, map[string]any{"x": 3, "y": 4}}},
				map[string]any{"type": "scroll", "x": 30, "y": 40, "scrollX": -50, "scrollY": 60},
				map[string]any{"type": "type", "text": "hello"},
				map[string]any{"type": "keypress", "keys": []any{"CTRL", "L"}},
				map[string]any{"type": "wait"},
			},
		},
	})
	if err != nil {
		t.Fatalf("desktop batch: %v", err)
	}
	toolResult, ok := result.(mcp.ToolResult)
	if !ok {
		t.Fatalf("desktop batch result = %T, want mcp.ToolResult", result)
	}
	freshCaptureID := toolResult.StructuredContent.(map[string]any)["captureId"].(string)
	if freshCaptureID == "" || freshCaptureID == originalCaptureID {
		t.Fatalf("fresh capture ID = %q, original = %q", freshCaptureID, originalCaptureID)
	}
	if len(toolResult.Content) != 2 {
		t.Fatalf("desktop batch content blocks = %d, want text then image", len(toolResult.Content))
	}
	if len(desktop.calls) != 2 || desktop.calls[0].method != "desktop.actions" || desktop.calls[1].method != "desktop.capture" {
		t.Fatalf("desktop calls = %#v, want actions then capture", desktop.calls)
	}
	gotActions, _ := desktop.calls[0].params["actions"].([]any)
	if len(gotActions) != 6 {
		t.Fatalf("desktop action count = %d, want 6", len(gotActions))
	}
}

func TestMCPDesktopBatchRejectsMalformedActionBeforeIPC(t *testing.T) {
	t.Parallel()

	desktop := &recordingCaller{responses: map[string]any{
		"desktop.capture": map[string]any{
			"data": []byte("png"), "mime_type": "image/png", "width": 100, "height": 50,
		},
	}}
	dispatcher := NewMCP(nil, desktop)
	observed, err := dispatcher.Dispatch(context.Background(), mcp.ToolCall{
		SessionID: "mcp-session-1", Name: "desktop_observe", Arguments: map[string]any{"action": "screenshot"},
	})
	if err != nil {
		t.Fatalf("desktop screenshot: %v", err)
	}
	captureID := observed.(mcp.ToolResult).StructuredContent.(map[string]any)["captureId"].(string)
	desktop.calls = nil

	_, err = dispatcher.Dispatch(context.Background(), mcp.ToolCall{
		SessionID: "mcp-session-1", Name: "desktop_control",
		Arguments: map[string]any{
			"action": "batch", "captureId": captureID,
			"actions": []any{
				map[string]any{"type": "move", "x": 10, "y": 20},
				map[string]any{"type": "drag", "path": []any{map[string]any{"x": 1, "y": 2}}},
			},
		},
	})
	if err == nil {
		t.Fatal("malformed desktop action succeeded")
	}
	if !strings.Contains(err.Error(), "drag") {
		t.Fatalf("malformed desktop action error = %q, want drag validation", err)
	}
	if len(desktop.calls) != 0 {
		t.Fatalf("malformed desktop batch reached IPC: %#v", desktop.calls)
	}
}

func TestMCPDesktopBatchRejectsWaitsBeyondIPCWindow(t *testing.T) {
	t.Parallel()

	desktopCaller := &recordingCaller{responses: map[string]any{
		"desktop.capture": map[string]any{"data": []byte("png"), "mime_type": "image/png", "width": 100, "height": 50},
	}}
	dispatcher := NewMCP(nil, desktopCaller)
	observed, err := dispatcher.Dispatch(context.Background(), mcp.ToolCall{
		SessionID: "session-one", Name: "desktop_observe", Arguments: map[string]any{"action": "screenshot"},
	})
	if err != nil {
		t.Fatalf("desktop screenshot: %v", err)
	}
	captureID := observed.(mcp.ToolResult).StructuredContent.(map[string]any)["captureId"].(string)
	desktopCaller.calls = nil
	actions := make([]any, 11)
	for index := range actions {
		actions[index] = map[string]any{"type": "wait"}
	}
	_, err = dispatcher.Dispatch(context.Background(), mcp.ToolCall{
		SessionID: "session-one", Name: "desktop_control",
		Arguments: map[string]any{"action": "batch", "captureId": captureID, "actions": actions},
	})
	if err == nil || !strings.Contains(err.Error(), "wait") {
		t.Fatalf("excessive wait batch error = %v, want wait limit", err)
	}
	if len(desktopCaller.calls) != 0 {
		t.Fatalf("excessive wait batch reached IPC: %#v", desktopCaller.calls)
	}
}

func TestMCPDesktopBatchConsumesCaptureBeforeActionFailure(t *testing.T) {
	t.Parallel()

	actionErr := errors.New("desktop input failed")
	desktop := &recordingCaller{responses: map[string]any{
		"desktop.capture": map[string]any{
			"data": []byte("png"), "mime_type": "image/png", "width": 100, "height": 50,
		},
	}, errorsByMethod: map[string]error{"desktop.actions": actionErr}}
	dispatcher := NewMCP(nil, desktop)
	observed, err := dispatcher.Dispatch(context.Background(), mcp.ToolCall{
		SessionID: "mcp-session-1", Name: "desktop_observe", Arguments: map[string]any{"action": "screenshot"},
	})
	if err != nil {
		t.Fatalf("desktop screenshot: %v", err)
	}
	captureID := observed.(mcp.ToolResult).StructuredContent.(map[string]any)["captureId"].(string)
	arguments := map[string]any{
		"action": "batch", "captureId": captureID,
		"actions": []any{map[string]any{"type": "click", "x": 10, "y": 20}},
	}

	if _, err := dispatcher.Dispatch(context.Background(), mcp.ToolCall{
		SessionID: "mcp-session-1", Name: "desktop_control", Arguments: arguments,
	}); !errors.Is(err, actionErr) {
		t.Fatalf("first desktop batch error = %v, want %v", err, actionErr)
	}
	desktop.calls = nil
	if _, err := dispatcher.Dispatch(context.Background(), mcp.ToolCall{
		SessionID: "mcp-session-1", Name: "desktop_control", Arguments: arguments,
	}); err == nil {
		t.Fatal("consumed capture ID was reused")
	}
	if len(desktop.calls) != 0 {
		t.Fatalf("reused capture reached desktop IPC: %#v", desktop.calls)
	}
}

func TestMCPSerializesObservationWithInFlightDesktopBatch(t *testing.T) {
	t.Parallel()

	desktop := newBlockingBatchCaller()
	dispatcher := NewMCP(nil, desktop)
	observed, err := dispatcher.Dispatch(context.Background(), mcp.ToolCall{
		SessionID: "mcp-session-1", Name: "desktop_observe", Arguments: map[string]any{"action": "screenshot"},
	})
	if err != nil {
		t.Fatalf("desktop screenshot: %v", err)
	}
	<-desktop.captureCalled
	captureID := observed.(mcp.ToolResult).StructuredContent.(map[string]any)["captureId"].(string)

	batchDone := make(chan error, 1)
	go func() {
		_, err := dispatcher.Dispatch(context.Background(), mcp.ToolCall{
			SessionID: "mcp-session-1", Name: "desktop_control",
			Arguments: map[string]any{
				"action": "batch", "captureId": captureID,
				"actions": []any{map[string]any{"type": "click", "x": 10, "y": 20}},
			},
		})
		batchDone <- err
	}()
	<-desktop.actionsStarted

	observeDone := make(chan error, 1)
	go func() {
		_, err := dispatcher.Dispatch(context.Background(), mcp.ToolCall{
			SessionID: "mcp-session-1", Name: "desktop_observe", Arguments: map[string]any{"action": "screenshot"},
		})
		observeDone <- err
	}()
	select {
	case <-desktop.captureCalled:
		t.Fatal("desktop observation captured while a batch was still executing")
	case <-time.After(100 * time.Millisecond):
	}
	close(desktop.releaseActions)
	if err := <-batchDone; err != nil {
		t.Fatalf("desktop batch: %v", err)
	}
	if err := <-observeDone; err != nil {
		t.Fatalf("desktop observation: %v", err)
	}
}

func TestMCPDesktopControlInvalidatesCapturesAcrossSessions(t *testing.T) {
	t.Parallel()

	desktopCaller := &recordingCaller{responses: map[string]any{
		"desktop.capture": map[string]any{"data": []byte("png"), "mime_type": "image/png", "width": 100, "height": 50},
		"desktop.mouse":   map[string]any{"ok": true},
		"desktop.actions": map[string]any{"ok": true},
	}}
	dispatcher := NewMCP(nil, desktopCaller)
	observed, err := dispatcher.Dispatch(context.Background(), mcp.ToolCall{
		SessionID: "session-one", Name: "desktop_observe", Arguments: map[string]any{"action": "screenshot"},
	})
	if err != nil {
		t.Fatalf("desktop screenshot: %v", err)
	}
	captureID := observed.(mcp.ToolResult).StructuredContent.(map[string]any)["captureId"].(string)
	if _, err := dispatcher.Dispatch(context.Background(), mcp.ToolCall{
		SessionID: "session-two", Name: "desktop_control",
		Arguments: map[string]any{"action": "mouse_move", "x": 1, "y": 2},
	}); err != nil {
		t.Fatalf("legacy desktop control: %v", err)
	}
	desktopCaller.calls = nil
	_, err = dispatcher.Dispatch(context.Background(), mcp.ToolCall{
		SessionID: "session-one", Name: "desktop_control",
		Arguments: map[string]any{
			"action": "batch", "captureId": captureID,
			"actions": []any{map[string]any{"type": "click", "x": 10, "y": 20}},
		},
	})
	if err == nil {
		t.Fatal("capture remained valid after another session changed the desktop")
	}
	if len(desktopCaller.calls) != 0 {
		t.Fatalf("stale cross-session batch reached IPC: %#v", desktopCaller.calls)
	}
}

func TestMCPDesktopBatchRejectsCoordinatesOutsideCapturedImage(t *testing.T) {
	t.Parallel()

	desktopCaller := &recordingCaller{responses: map[string]any{
		"desktop.capture": map[string]any{"data": []byte("png"), "mime_type": "image/png", "width": 100, "height": 50},
	}}
	dispatcher := NewMCP(nil, desktopCaller)
	observed, err := dispatcher.Dispatch(context.Background(), mcp.ToolCall{
		SessionID: "session-one", Name: "desktop_observe", Arguments: map[string]any{"action": "screenshot"},
	})
	if err != nil {
		t.Fatalf("desktop screenshot: %v", err)
	}
	captureID := observed.(mcp.ToolResult).StructuredContent.(map[string]any)["captureId"].(string)
	desktopCaller.calls = nil
	_, err = dispatcher.Dispatch(context.Background(), mcp.ToolCall{
		SessionID: "session-one", Name: "desktop_control",
		Arguments: map[string]any{
			"action": "batch", "captureId": captureID,
			"actions": []any{
				map[string]any{
					"type": "drag",
					"path": []any{
						map[string]any{"x": 10, "y": 20},
						map[string]any{"x": 100, "y": 49},
					},
				},
			},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "bounds") {
		t.Fatalf("out-of-bounds batch error = %v, want bounds error", err)
	}
	if len(desktopCaller.calls) != 0 {
		t.Fatalf("out-of-bounds batch reached IPC: %#v", desktopCaller.calls)
	}
}

func TestMCPRejectsUnknownActionsBeforeIPC(t *testing.T) {
	t.Parallel()

	broker := &recordingCaller{}
	desktop := &recordingCaller{}
	dispatcher := NewMCP(broker, desktop)
	_, err := dispatcher.Dispatch(context.Background(), mcp.ToolCall{Name: "filesystem_write", Arguments: map[string]any{
		"action": "format_disk", "path": "/dev/disk0",
	}})
	if err == nil {
		t.Fatal("unknown action succeeded")
	}
	if len(broker.calls)+len(desktop.calls) != 0 {
		t.Fatalf("unknown action reached IPC: broker=%#v desktop=%#v", broker.calls, desktop.calls)
	}
}

func TestMCPTerminalInspectFiltersListedSessionsLocally(t *testing.T) {
	t.Parallel()
	desktop := &recordingCaller{responses: map[string]any{
		"terminal.list": []map[string]any{
			{"Session": map[string]any{"ID": "one"}, "Running": true},
			{"Session": map[string]any{"ID": "two"}, "Running": false},
		},
	}}
	result, err := NewMCP(nil, desktop).Dispatch(context.Background(), mcp.ToolCall{Name: "terminal_sessions", Arguments: map[string]any{
		"action": "inspect", "sessionId": "two", "privilege": "owner",
	}})
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if result.(map[string]any)["Running"] != false || desktop.calls[0].method != "terminal.list" {
		t.Fatalf("inspect result=%#v calls=%#v", result, desktop.calls)
	}
}

func TestMCPTerminalSignalPreservesRequestedSignal(t *testing.T) {
	t.Parallel()
	for _, signal := range []string{"interrupt", "terminate", "kill"} {
		desktop := &recordingCaller{responses: map[string]any{"terminal.signal": map[string]any{"ok": true}}}
		_, err := NewMCP(nil, desktop).Dispatch(context.Background(), mcp.ToolCall{Name: "terminal", Arguments: map[string]any{
			"action": "signal", "sessionId": "session-1", "signal": signal,
		}})
		if err != nil {
			t.Fatalf("signal %s: %v", signal, err)
		}
		if got := desktop.calls[0].params["signal"]; got != signal {
			t.Fatalf("signal %s became %#v", signal, got)
		}
	}
}

func TestMCPTerminalResizeRoutesDimensions(t *testing.T) {
	t.Parallel()
	desktop := &recordingCaller{responses: map[string]any{"terminal.resize": map[string]any{"ok": true}}}
	_, err := NewMCP(nil, desktop).Dispatch(context.Background(), mcp.ToolCall{Name: "terminal", Arguments: map[string]any{
		"action": "resize", "sessionId": "session-1", "columns": 132, "rows": 43,
	}})
	if err != nil {
		t.Fatalf("resize: %v", err)
	}
	call := desktop.calls[0]
	if call.method != "terminal.resize" || call.params["columns"] != float64(132) || call.params["rows"] != float64(43) {
		t.Fatalf("resize call = %#v", call)
	}
}

func TestMCPTerminalResizeRejectsInvalidDimensionsBeforeIPC(t *testing.T) {
	t.Parallel()
	desktop := &recordingCaller{}
	_, err := NewMCP(nil, desktop).Dispatch(context.Background(), mcp.ToolCall{Name: "terminal", Arguments: map[string]any{
		"action": "resize", "sessionId": "session-1", "columns": 0, "rows": 24,
	}})
	if err == nil {
		t.Fatal("zero-width resize succeeded")
	}
	if len(desktop.calls) != 0 {
		t.Fatalf("invalid resize reached IPC: %#v", desktop.calls)
	}
}

func TestMCPTerminalResizeRejectsOverflowingDimensionsBeforeIPC(t *testing.T) {
	t.Parallel()
	desktop := &recordingCaller{}
	_, err := NewMCP(nil, desktop).Dispatch(context.Background(), mcp.ToolCall{Name: "terminal", Arguments: map[string]any{
		"action": "resize", "sessionId": "session-1", "columns": 32768, "rows": 24,
	}})
	if err == nil {
		t.Fatal("overflowing resize succeeded")
	}
	if len(desktop.calls) != 0 {
		t.Fatalf("invalid resize reached IPC: %#v", desktop.calls)
	}
}

type recordingCaller struct {
	calls          []recordedCall
	responses      map[string]any
	err            error
	errorsByMethod map[string]error
}

type blockingBatchCaller struct {
	actionsStarted chan struct{}
	releaseActions chan struct{}
	captureCalled  chan struct{}
	once           sync.Once
}

func newBlockingBatchCaller() *blockingBatchCaller {
	return &blockingBatchCaller{
		actionsStarted: make(chan struct{}),
		releaseActions: make(chan struct{}),
		captureCalled:  make(chan struct{}, 3),
	}
}

func (c *blockingBatchCaller) Call(_ context.Context, method string, _ any, result any) error {
	var response any
	switch method {
	case desktop.RPCMethodDesktopCapture:
		c.captureCalled <- struct{}{}
		response = map[string]any{"data": []byte("png"), "mime_type": "image/png", "width": 100, "height": 50}
	case desktop.RPCMethodDesktopActions:
		c.once.Do(func() { close(c.actionsStarted) })
		<-c.releaseActions
		response = map[string]any{"ok": true}
	default:
		return errors.New("unexpected desktop method")
	}
	encoded, _ := json.Marshal(response)
	return json.Unmarshal(encoded, result)
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	return string(encoded)
}

type recordedCall struct {
	method string
	params map[string]any
}

func (c *recordingCaller) Call(_ context.Context, method string, params any, result any) error {
	encoded, _ := json.Marshal(params)
	decoded := map[string]any{}
	_ = json.Unmarshal(encoded, &decoded)
	c.calls = append(c.calls, recordedCall{method: method, params: decoded})
	if err := c.errorsByMethod[method]; err != nil {
		return err
	}
	if c.err != nil {
		return c.err
	}
	response, ok := c.responses[method]
	if !ok {
		return errors.New("missing fake response")
	}
	encoded, _ = json.Marshal(response)
	return json.Unmarshal(encoded, result)
}
