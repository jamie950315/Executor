//go:build darwin

package desktop

/*
#cgo LDFLAGS: -framework ApplicationServices
#include <ApplicationServices/ApplicationServices.h>

static CGEventType executorMouseDownEvent(CGMouseButton button) {
	switch (button) {
	case kCGMouseButtonRight:
		return kCGEventRightMouseDown;
	case kCGMouseButtonCenter:
		return kCGEventOtherMouseDown;
	default:
		return kCGEventLeftMouseDown;
	}
}

static CGEventType executorMouseUpEvent(CGMouseButton button) {
	switch (button) {
	case kCGMouseButtonRight:
		return kCGEventRightMouseUp;
	case kCGMouseButtonCenter:
		return kCGEventOtherMouseUp;
	default:
		return kCGEventLeftMouseUp;
	}
}

static CGEventType executorMouseMoveEvent(CGMouseButton button) {
	switch (button) {
	case kCGMouseButtonRight:
		return kCGEventRightMouseDragged;
	case kCGMouseButtonCenter:
		return kCGEventOtherMouseDragged;
	default:
		return kCGEventLeftMouseDragged;
	}
}

static void executorPostMouseMove(int x, int y) {
	CGEventRef move = CGEventCreateMouseEvent(NULL, kCGEventMouseMoved, CGPointMake(x, y), kCGMouseButtonLeft);
	CGEventPost(kCGHIDEventTap, move);
	CFRelease(move);
}

static void executorPostMouseButton(CGEventType eventType, int x, int y, CGMouseButton button) {
	CGEventRef event = CGEventCreateMouseEvent(NULL, eventType, CGPointMake(x, y), button);
	CGEventPost(kCGHIDEventTap, event);
	CFRelease(event);
}

static void executorPostKey(int keyCode, CGEventFlags flags, int down) {
	CGEventRef event = CGEventCreateKeyboardEvent(NULL, (CGKeyCode)keyCode, down ? true : false);
	CGEventSetFlags(event, flags);
	CGEventPost(kCGHIDEventTap, event);
	CFRelease(event);
}
*/
import "C"

import (
	"fmt"
	"strings"
)

type defaultEventPoster struct{}

func (defaultEventPoster) PostMouse(action MouseAction) error {
	button, err := cgMouseButton(action.Button)
	if err != nil {
		return err
	}
	switch action.Type {
	case MouseActionMove:
		C.executorPostMouseMove(C.int(action.X), C.int(action.Y))
	case MouseActionDown:
		C.executorPostMouseButton(C.executorMouseDownEvent(button), C.int(action.X), C.int(action.Y), button)
	case MouseActionUp:
		C.executorPostMouseButton(C.executorMouseUpEvent(button), C.int(action.X), C.int(action.Y), button)
	case MouseActionClick:
		C.executorPostMouseMove(C.int(action.X), C.int(action.Y))
		C.executorPostMouseButton(C.executorMouseDownEvent(button), C.int(action.X), C.int(action.Y), button)
		C.executorPostMouseButton(C.executorMouseUpEvent(button), C.int(action.X), C.int(action.Y), button)
	default:
		return fmt.Errorf("unsupported mouse action %q", action.Type)
	}
	return nil
}

func (defaultEventPoster) PostKeyboard(action KeyboardAction) error {
	if action.KeyCode < 0 {
		return fmt.Errorf("invalid key code %d", action.KeyCode)
	}
	flags, err := cgEventFlags(action.Modifiers)
	if err != nil {
		return err
	}
	C.executorPostKey(C.int(action.KeyCode), flags, 1)
	C.executorPostKey(C.int(action.KeyCode), flags, 0)
	return nil
}

func cgMouseButton(button MouseButton) (C.CGMouseButton, error) {
	switch button {
	case "", MouseButtonLeft:
		return C.kCGMouseButtonLeft, nil
	case MouseButtonRight:
		return C.kCGMouseButtonRight, nil
	case MouseButtonCenter:
		return C.kCGMouseButtonCenter, nil
	default:
		return C.kCGMouseButtonLeft, fmt.Errorf("unsupported mouse button %q", button)
	}
}

func cgEventFlags(modifiers []string) (C.CGEventFlags, error) {
	var flags C.CGEventFlags
	for _, modifier := range modifiers {
		switch strings.ToLower(modifier) {
		case "shift":
			flags |= C.kCGEventFlagMaskShift
		case "control", "ctrl":
			flags |= C.kCGEventFlagMaskControl
		case "option", "alt":
			flags |= C.kCGEventFlagMaskAlternate
		case "command", "cmd", "meta":
			flags |= C.kCGEventFlagMaskCommand
		case "":
		default:
			return 0, fmt.Errorf("unsupported keyboard modifier %q", modifier)
		}
	}
	return flags, nil
}
