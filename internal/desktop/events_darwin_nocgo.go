//go:build darwin && !cgo

package desktop

import (
	"fmt"
	"os/exec"
	"strings"
)

type defaultEventPoster struct{}

func (defaultEventPoster) PostMouse(action MouseAction) error {
	button, down, up, err := swiftMouseTypes(action.Button)
	if err != nil {
		return err
	}
	events := "post(\".mouseMoved\")"
	switch action.Type {
	case MouseActionMove:
	case MouseActionDown:
		events = fmt.Sprintf("post(\"%s\")", down)
	case MouseActionUp:
		events = fmt.Sprintf("post(\"%s\")", up)
	case MouseActionClick:
		events = fmt.Sprintf("post(\"%s\"); post(\"%s\")", down, up)
	default:
		return fmt.Errorf("unsupported mouse action %q", action.Type)
	}
	source := fmt.Sprintf(`import CoreGraphics
let point = CGPoint(x: %d, y: %d)
let button = CGMouseButton(rawValue: %d)!
func post(_ name: String) {
  let types: [String: CGEventType] = [
    ".mouseMoved": .mouseMoved, ".leftMouseDown": .leftMouseDown, ".leftMouseUp": .leftMouseUp,
    ".rightMouseDown": .rightMouseDown, ".rightMouseUp": .rightMouseUp,
    ".otherMouseDown": .otherMouseDown, ".otherMouseUp": .otherMouseUp]
  if let event = CGEvent(mouseEventSource: nil, mouseType: types[name]!, mouseCursorPosition: point, mouseButton: button) {
    event.post(tap: .cghidEventTap)
  }
}
%s`, action.X, action.Y, button, events)
	return exec.Command("/usr/bin/swift", "-e", source).Run()
}

func (defaultEventPoster) PostKeyboard(action KeyboardAction) error {
	if action.KeyCode < 0 {
		return fmt.Errorf("invalid key code %d", action.KeyCode)
	}
	modifiers := make([]string, 0, len(action.Modifiers))
	for _, modifier := range action.Modifiers {
		switch strings.ToLower(modifier) {
		case "shift":
			modifiers = append(modifiers, "shift down")
		case "control", "ctrl":
			modifiers = append(modifiers, "control down")
		case "option", "alt":
			modifiers = append(modifiers, "option down")
		case "command", "cmd", "meta":
			modifiers = append(modifiers, "command down")
		default:
			return fmt.Errorf("unsupported keyboard modifier %q", modifier)
		}
	}
	using := ""
	if len(modifiers) > 0 {
		using = " using {" + strings.Join(modifiers, ", ") + "}"
	}
	script := fmt.Sprintf(`tell application "System Events" to key code %d%s`, action.KeyCode, using)
	return exec.Command("/usr/bin/osascript", "-e", script).Run()
}

func swiftMouseTypes(button MouseButton) (code int, down, up string, err error) {
	switch button {
	case "", MouseButtonLeft:
		return 0, ".leftMouseDown", ".leftMouseUp", nil
	case MouseButtonRight:
		return 1, ".rightMouseDown", ".rightMouseUp", nil
	case MouseButtonCenter:
		return 2, ".otherMouseDown", ".otherMouseUp", nil
	default:
		return 0, "", "", fmt.Errorf("unsupported mouse button %q", button)
	}
}
