//go:build darwin && !cgo

package desktop

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

type defaultEventPoster struct{}

func mainDisplayDimensions(ctx context.Context) (int, int, error) {
	const source = `import CoreGraphics
let bounds = CGDisplayBounds(CGMainDisplayID())
print("\(Int(bounds.width)) \(Int(bounds.height))")`
	output, err := exec.CommandContext(ctx, "/usr/bin/swift", "-e", source).Output()
	if err != nil {
		return 0, 0, err
	}
	var width, height int
	if _, err := fmt.Sscanf(string(output), "%d %d", &width, &height); err != nil {
		return 0, 0, err
	}
	return width, height, nil
}

func (defaultEventPoster) PostMouse(action MouseAction) error {
	button, down, up, drag, err := swiftMouseTypes(action.Button)
	if err != nil {
		return err
	}
	modifiers, err := normalizeModifiers(action.Keys)
	if err != nil {
		return err
	}
	steps, err := expandMouseAction(action)
	if err != nil {
		return err
	}
	flagLines := "var flags = CGEventFlags()\n"
	for _, modifier := range modifiers {
		switch modifier {
		case keyShift:
			flagLines += "flags.insert(.maskShift)\n"
		case keyControl:
			flagLines += "flags.insert(.maskControl)\n"
		case keyAlt:
			flagLines += "flags.insert(.maskAlternate)\n"
		case keyMeta:
			flagLines += "flags.insert(.maskCommand)\n"
		}
	}
	var events strings.Builder
	for _, step := range steps {
		eventType := ".mouseMoved"
		switch step.Type {
		case mouseStepDown:
			eventType = down
		case mouseStepUp:
			eventType = up
		case mouseStepDrag:
			eventType = drag
		case mouseStepScroll:
			scrollX, scrollY := nativeWheelDeltas(step.ScrollX, step.ScrollY)
			fmt.Fprintf(&events, "postScroll(%d, %d, %d, %d)\n", step.X, step.Y, scrollX, scrollY)
			continue
		}
		fmt.Fprintf(&events, "postMouse(\"%s\", %d, %d, %d)\n", eventType, step.X, step.Y, step.Click)
	}
	source := fmt.Sprintf(`import CoreGraphics
let button = CGMouseButton(rawValue: %d)!
%s
func postMouse(_ name: String, _ x: Int, _ y: Int, _ clickState: Int64) {
  let types: [String: CGEventType] = [
    ".mouseMoved": .mouseMoved, ".leftMouseDown": .leftMouseDown, ".leftMouseUp": .leftMouseUp,
    ".rightMouseDown": .rightMouseDown, ".rightMouseUp": .rightMouseUp,
    ".otherMouseDown": .otherMouseDown, ".otherMouseUp": .otherMouseUp,
    ".leftMouseDragged": .leftMouseDragged, ".rightMouseDragged": .rightMouseDragged,
    ".otherMouseDragged": .otherMouseDragged]
  if let event = CGEvent(mouseEventSource: nil, mouseType: types[name]!, mouseCursorPosition: CGPoint(x: x, y: y), mouseButton: button) {
    event.flags = flags
    if clickState > 0 { event.setIntegerValueField(.mouseEventClickState, value: clickState) }
    event.post(tap: .cghidEventTap)
  }
}
func postScroll(_ x: Int, _ y: Int, _ scrollX: Int32, _ scrollY: Int32) {
  if let event = CGEvent(scrollWheelEvent2Source: nil, units: .pixel, wheelCount: 2, wheel1: scrollY, wheel2: scrollX, wheel3: 0) {
    event.location = CGPoint(x: x, y: y)
    event.flags = flags
    event.post(tap: .cghidEventTap)
  }
}
%s`, button, flagLines, events.String())
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

func swiftMouseTypes(button MouseButton) (code int, down, up, drag string, err error) {
	switch button {
	case "", MouseButtonLeft:
		return 0, ".leftMouseDown", ".leftMouseUp", ".leftMouseDragged", nil
	case MouseButtonRight:
		return 1, ".rightMouseDown", ".rightMouseUp", ".rightMouseDragged", nil
	case MouseButtonCenter, MouseButtonWheel:
		return 2, ".otherMouseDown", ".otherMouseUp", ".otherMouseDragged", nil
	default:
		return 0, "", "", "", fmt.Errorf("unsupported mouse button %q", button)
	}
}
