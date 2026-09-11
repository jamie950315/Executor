package dispatch

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/jamie950315/executor/internal/agent"
	"github.com/jamie950315/executor/internal/desktop"
	"github.com/jamie950315/executor/internal/mcp"
)

type Caller interface {
	Call(ctx context.Context, method string, params any, result any) error
}

type MCP struct {
	broker            Caller
	desktop           Caller
	desktopM          sync.Mutex
	captureM          sync.Mutex
	captures          map[string]captureState
	desktopGeneration uint64
}

type captureState struct {
	Epoch      uint64
	ID         string
	Width      int
	Height     int
	Generation uint64
}

type externalDesktopAction struct {
	Type    desktop.ActionKind  `json:"type"`
	X       *int                `json:"x,omitempty"`
	Y       *int                `json:"y,omitempty"`
	Button  desktop.MouseButton `json:"button,omitempty"`
	Text    *string             `json:"text,omitempty"`
	Keys    []string            `json:"keys,omitempty"`
	ScrollX *int                `json:"scrollX,omitempty"`
	ScrollY *int                `json:"scrollY,omitempty"`
	Path    []desktop.Point     `json:"path,omitempty"`
}

func NewMCP(broker, desktop Caller) *MCP {
	return &MCP{broker: broker, desktop: desktop, captures: map[string]captureState{}}
}

func (d *MCP) Dispatch(ctx context.Context, call mcp.ToolCall) (any, error) {
	switch call.Name {
	case "terminal", "terminal_output", "terminal_sessions", "filesystem_read", "filesystem_write":
		if raw, exists := call.Arguments["privilege"]; exists {
			privilege, ok := raw.(string)
			if !ok || (privilege != "owner" && privilege != "admin") {
				return nil, errors.New("privilege must be owner or admin")
			}
		}
	}
	switch call.Name {
	case "desktop_live":
		input := make(map[string]any, len(call.Arguments))
		for k, v := range call.Arguments {
			if k != "owner" {
				input[k] = v
			}
		}
		raw, err := json.Marshal(input)
		if err != nil {
			return nil, err
		}
		var request desktop.RPCLiveRequest
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&request); err != nil {
			return nil, errors.New("invalid live desktop arguments")
		}
		request.Owner = captureScope(ctx, call.SessionID)
		return d.call(ctx, d.desktop, "desktop.live", request)
	case "terminal":
		return d.terminal(ctx, call.Arguments)
	case "terminal_output":
		return d.terminalOutput(ctx, call.Arguments)
	case "terminal_sessions":
		return d.terminalSessions(ctx, call.Arguments)
	case "filesystem_read":
		return d.filesystemRead(ctx, call.Arguments)
	case "filesystem_write":
		return d.filesystemWrite(ctx, call.Arguments)
	case "desktop_observe":
		return d.desktopObserve(ctx, call.SessionID, call.Arguments)
	case "desktop_control":
		return d.desktopControl(ctx, call.SessionID, call.Arguments)
	case "device_status":
		return d.deviceStatus(ctx, call.Arguments)
	case "device_permissions":
		return d.devicePermissions(ctx, call.Arguments)
	default:
		return nil, fmt.Errorf("unsupported Executor tool %q", call.Name)
	}
}

func (d *MCP) desktopObserve(ctx context.Context, sessionID string, arguments map[string]any) (any, error) {
	action, err := requiredString(arguments, "action")
	if err != nil {
		return nil, err
	}
	method, ok := map[string]string{
		"screenshot":         "desktop.screenshot",
		"accessibility_tree": "desktop.accessibility",
		"windows":            "desktop.windows",
		"applications":       "desktop.applications",
	}[action]
	if !ok {
		return nil, fmt.Errorf("unsupported desktop observe action %q", action)
	}
	switch action {
	case "windows", "accessibility_tree":
		return d.call(ctx, d.desktop, method, struct{}{})
	case "screenshot":
		path, _ := arguments["path"].(string)
		includeImage, _ := arguments["includeImage"].(bool)
		if path != "" && !includeImage {
			return d.call(ctx, d.desktop, method, desktop.RPCDesktopScreenshotParams{Path: path})
		}
		return d.captureDesktop(ctx, sessionID)
	case "applications":
		windows, err := d.call(ctx, d.desktop, desktop.RPCMethodDesktopWindows, struct{}{})
		if err != nil {
			return nil, err
		}
		items, _ := windows.([]any)
		seen := map[string]bool{}
		applications := make([]string, 0)
		for _, item := range items {
			entry, _ := item.(map[string]any)
			name, _ := entry["app"].(string)
			if name != "" && !seen[name] {
				seen[name] = true
				applications = append(applications, name)
			}
		}
		return map[string]any{"applications": applications}, nil
	default:
		return nil, fmt.Errorf("unsupported desktop observe action %q", action)
	}
}

func (d *MCP) desktopControl(ctx context.Context, sessionID string, arguments map[string]any) (any, error) {
	d.desktopM.Lock()
	defer d.desktopM.Unlock()
	action, err := requiredString(arguments, "action")
	if err != nil {
		return nil, err
	}
	if action == "batch" {
		return d.desktopBatch(ctx, sessionID, arguments)
	}
	method, ok := map[string]string{
		"mouse_move":   "desktop.mouse",
		"mouse_click":  "desktop.mouse",
		"key_press":    "desktop.keyboard",
		"type_text":    "desktop.keyboard",
		"window_focus": "desktop.app",
	}[action]
	if !ok {
		return nil, fmt.Errorf("unsupported desktop control action %q", action)
	}
	switch action {
	case "mouse_move", "mouse_click":
		button, _ := arguments["button"].(string)
		if button == "middle" {
			button = string(desktop.MouseButtonCenter)
		}
		actionType := desktop.MouseActionMove
		if action == "mouse_click" {
			actionType = desktop.MouseActionClick
		}
		d.invalidateDesktopCaptures()
		return d.call(ctx, d.desktop, method, desktop.RPCDesktopMouseParams{Action: desktop.MouseAction{
			Type: actionType, X: int(integer(arguments["x"])), Y: int(integer(arguments["y"])), Button: desktop.MouseButton(button),
		}})
	case "type_text":
		text, err := requiredString(arguments, "text")
		if err != nil {
			return nil, err
		}
		d.invalidateDesktopCaptures()
		return d.call(ctx, d.desktop, method, desktop.RPCDesktopKeyboardParams{Action: desktop.KeyboardAction{Text: text}})
	case "key_press":
		modifiers := stringSlice(arguments["modifiers"])
		d.invalidateDesktopCaptures()
		return d.call(ctx, d.desktop, method, desktop.RPCDesktopKeyboardParams{Action: desktop.KeyboardAction{
			KeyCode: int(integer(arguments["keyCode"])), Modifiers: modifiers,
		}})
	case "window_focus":
		name, err := requiredString(arguments, "name")
		if err != nil {
			return nil, err
		}
		d.invalidateDesktopCaptures()
		return d.call(ctx, d.desktop, method, desktop.RPCDesktopAppParams{Action: desktop.AppAction{Type: desktop.AppActionActivate, Name: name}})
	default:
		return nil, fmt.Errorf("unsupported desktop control action %q", action)
	}
}

const maxDesktopCaptureBytes = 48 << 20
const maxDesktopBatchActions = 64
const maxDesktopBatchWaits = 10

type desktopCapture struct {
	Epoch    uint64 `json:"epoch,omitempty"`
	Data     []byte `json:"data"`
	MimeType string `json:"mime_type"`
	Width    int    `json:"width"`
	Height   int    `json:"height"`
}

func (d *MCP) captureDesktop(ctx context.Context, sessionID string) (mcp.ToolResult, error) {
	d.desktopM.Lock()
	defer d.desktopM.Unlock()
	return d.captureDesktopLocked(ctx, sessionID)
}

func (d *MCP) captureDesktopLocked(ctx context.Context, sessionID string) (mcp.ToolResult, error) {
	if d.desktop == nil {
		return mcp.ToolResult{}, errors.New("Executor desktop helper is unavailable")
	}
	var capture desktopCapture
	if err := d.desktop.Call(ctx, "desktop.capture", struct{}{}, &capture); err != nil {
		return mcp.ToolResult{}, err
	}
	if len(capture.Data) == 0 || len(capture.Data) > maxDesktopCaptureBytes {
		return mcp.ToolResult{}, fmt.Errorf("desktop capture size must be between 1 and %d bytes", maxDesktopCaptureBytes)
	}
	if !strings.HasPrefix(capture.MimeType, "image/") || capture.Width < 1 || capture.Height < 1 {
		return mcp.ToolResult{}, errors.New("desktop capture metadata is invalid")
	}
	captureID, err := randomCaptureID()
	if err != nil {
		return mcp.ToolResult{}, err
	}
	d.captureM.Lock()
	d.captures[captureScope(ctx, sessionID)] = captureState{
		ID: captureID, Width: capture.Width, Height: capture.Height, Generation: d.desktopGeneration, Epoch: capture.Epoch,
	}
	d.captureM.Unlock()

	return mcp.ToolResult{
		StructuredContent: map[string]any{
			"captureId":  captureID,
			"width":      capture.Width,
			"height":     capture.Height,
			"mimeType":   capture.MimeType,
			"capturedAt": time.Now().UTC().Format(time.RFC3339Nano),
		},
		Content: []any{
			map[string]any{
				"type": "text",
				"text": fmt.Sprintf(
					"Screenshot captured. Use captureId %s for the next desktop_control batch. %dx%d %s.",
					captureID, capture.Width, capture.Height, capture.MimeType,
				),
			},
			map[string]any{
				"type":     "image",
				"data":     base64.StdEncoding.EncodeToString(capture.Data),
				"mimeType": capture.MimeType,
				"_meta":    map[string]any{"codex/imageDetail": "original"},
			},
		},
	}, nil
}

func (d *MCP) desktopBatch(ctx context.Context, sessionID string, arguments map[string]any) (any, error) {
	captureID, err := requiredString(arguments, "captureId")
	if err != nil {
		return nil, err
	}
	actions, err := parseDesktopActions(arguments["actions"])
	if err != nil {
		return nil, err
	}
	d.captureM.Lock()
	scope := captureScope(ctx, sessionID)
	latestCapture := d.captures[scope]
	if latestCapture.ID == "" || captureID != latestCapture.ID || latestCapture.Generation != d.desktopGeneration {
		d.captureM.Unlock()
		return nil, errors.New("desktop capture is stale; observe the desktop again before controlling it")
	}
	if err := validateDesktopActionBounds(actions, latestCapture.Width, latestCapture.Height); err != nil {
		d.captureM.Unlock()
		return nil, err
	}
	d.desktopGeneration++
	delete(d.captures, scope)
	d.captureM.Unlock()
	if _, err := d.call(ctx, d.desktop, desktop.RPCMethodDesktopActions, desktop.RPCDesktopActionsParams{Actions: actions, ExpectedEpoch: latestCapture.Epoch}); err != nil {
		return nil, err
	}
	return d.captureDesktopLocked(ctx, sessionID)
}

func (d *MCP) invalidateDesktopCaptures() {
	d.captureM.Lock()
	d.desktopGeneration++
	d.captureM.Unlock()
}

func validateDesktopActionBounds(actions []desktop.Action, width, height int) error {
	validPoint := func(point desktop.Point) bool {
		return point.X >= 0 && point.Y >= 0 && point.X < width && point.Y < height
	}
	for index, action := range actions {
		switch action.Type {
		case desktop.ActionClick, desktop.ActionDoubleClick, desktop.ActionMove, desktop.ActionScroll:
			if !validPoint(desktop.Point{X: action.X, Y: action.Y}) {
				return fmt.Errorf("desktop action %d (%s) coordinates are outside capture bounds %dx%d", index, action.Type, width, height)
			}
		case desktop.ActionDrag:
			for _, point := range action.Path {
				if !validPoint(point) {
					return fmt.Errorf("desktop action %d (drag) path is outside capture bounds %dx%d", index, width, height)
				}
			}
		}
	}
	return nil
}

func parseDesktopActions(value any) ([]desktop.Action, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, errors.New("desktop actions are invalid")
	}
	var external []externalDesktopAction
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&external); err != nil {
		return nil, fmt.Errorf("desktop actions are invalid: %w", err)
	}
	if len(external) == 0 {
		return nil, errors.New("desktop actions require at least one action")
	}
	if len(external) > maxDesktopBatchActions {
		return nil, fmt.Errorf("desktop actions exceed the limit of %d", maxDesktopBatchActions)
	}
	actions := make([]desktop.Action, 0, len(external))
	waits := 0
	for index, item := range external {
		action := desktop.Action{
			Type: item.Type, Button: item.Button, Text: optionalString(item.Text),
			Keys: append([]string(nil), item.Keys...), Path: append([]desktop.Point(nil), item.Path...),
		}
		switch item.Type {
		case desktop.ActionClick, desktop.ActionDoubleClick, desktop.ActionMove:
			if item.X == nil || item.Y == nil {
				return nil, fmt.Errorf("desktop action %d (%s) requires x and y", index, item.Type)
			}
			action.X, action.Y = *item.X, *item.Y
		case desktop.ActionScroll:
			if item.X == nil || item.Y == nil || item.ScrollX == nil || item.ScrollY == nil {
				return nil, fmt.Errorf("desktop action %d (scroll) requires x, y, scrollX, and scrollY", index)
			}
			action.X, action.Y, action.ScrollX, action.ScrollY = *item.X, *item.Y, *item.ScrollX, *item.ScrollY
		case desktop.ActionDrag:
			if len(item.Path) < 2 {
				return nil, fmt.Errorf("desktop action %d (drag) requires at least two path points", index)
			}
		case desktop.ActionType:
			if item.Text == nil || *item.Text == "" {
				return nil, fmt.Errorf("desktop action %d (type) requires text", index)
			}
		case desktop.ActionKeypress:
			if len(item.Keys) == 0 {
				return nil, fmt.Errorf("desktop action %d (keypress) requires keys", index)
			}
		case desktop.ActionWait, desktop.ActionScreenshot:
			if item.Type == desktop.ActionWait {
				waits++
				if waits > maxDesktopBatchWaits {
					return nil, fmt.Errorf("desktop actions exceed the wait limit of %d", maxDesktopBatchWaits)
				}
			}
		default:
			return nil, fmt.Errorf("unsupported desktop action %d type %q", index, item.Type)
		}
		if action.Button == "middle" {
			action.Button = desktop.MouseButtonCenter
		}
		if action.Button != "" && action.Button != desktop.MouseButtonLeft && action.Button != desktop.MouseButtonRight && action.Button != desktop.MouseButtonCenter {
			return nil, fmt.Errorf("desktop action %d has unsupported mouse button %q", index, action.Button)
		}
		actions = append(actions, action)
	}
	if err := desktop.ValidateActions(actions); err != nil {
		return nil, err
	}
	return actions, nil
}

func optionalString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func randomCaptureID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("generate desktop capture ID: %w", err)
	}
	return hex.EncodeToString(value[:]), nil
}

func captureScope(ctx context.Context, sessionID string) string {
	if actor, ok := agent.ActorFromContext(ctx); ok {
		return "actor\x00" + actor.Method + "\x00" + actor.Subject + "\x00" + actor.ClientID
	}
	if sessionID == "" {
		return "local"
	}
	return "session\x00" + sessionID
}

func (d *MCP) deviceStatus(ctx context.Context, arguments map[string]any) (any, error) {
	action, err := requiredString(arguments, "action")
	if err != nil {
		return nil, err
	}
	switch action {
	case "desktop":
		return d.call(ctx, d.desktop, desktop.RPCMethodDeviceStatus, struct{}{})
	case "terminals":
		return diagnosticComponents(ctx, "terminal.list", diagnosticTarget{"owner", d.desktop}, diagnosticTarget{"admin", d.broker})
	case "summary":
		return diagnosticComponents(ctx, desktop.RPCMethodDeviceStatus, diagnosticTarget{"broker", d.broker}, diagnosticTarget{"desktop", d.desktop})
	default:
		return nil, fmt.Errorf("unsupported device status action %q", action)
	}
}

func (d *MCP) devicePermissions(ctx context.Context, arguments map[string]any) (any, error) {
	action, err := requiredString(arguments, "action")
	if err != nil {
		return nil, err
	}
	request := false
	switch action {
	case "status":
	case "request_all":
		request = true
	default:
		return nil, fmt.Errorf("unsupported device permissions action %q", action)
	}
	result, err := d.call(ctx, d.desktop, desktop.RPCMethodDesktopPermissions, desktop.RPCDesktopPermissionsParams{Request: request})
	if err != nil {
		return nil, err
	}
	return desktopPermissionResult(result)
}

func (d *MCP) privilegedCaller(arguments map[string]any) Caller {
	privilege, _ := arguments["privilege"].(string)
	if privilege == "admin" {
		return d.broker
	}
	return d.desktop
}

func (d *MCP) call(ctx context.Context, caller Caller, method string, arguments any) (any, error) {
	if caller == nil {
		return nil, errors.New("Executor helper is unavailable")
	}
	var raw json.RawMessage
	if err := caller.Call(ctx, method, arguments, &raw); err != nil {
		return nil, err
	}
	if len(raw) == 0 || string(raw) == "null" {
		return map[string]any{"ok": true}, nil
	}
	var result any
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, err
	}
	return result, nil
}

func integer(value any) int64 {
	switch typed := value.(type) {
	case int:
		return int64(typed)
	case int64:
		return typed
	case float64:
		return int64(typed)
	default:
		return 0
	}
}

func stringSlice(value any) []string {
	items, _ := value.([]any)
	result := make([]string, 0, len(items))
	for _, item := range items {
		if text, ok := item.(string); ok {
			result = append(result, text)
		}
	}
	return result
}

func anySlice(value any) []any {
	items, _ := value.([]any)
	return items
}

func requiredString(arguments map[string]any, name string) (string, error) {
	value, _ := arguments[name].(string)
	if value == "" {
		return "", fmt.Errorf("%s is required", name)
	}
	return value, nil
}
