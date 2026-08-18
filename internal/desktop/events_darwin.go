//go:build darwin && cgo

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

static void executorPostMouseMove(int x, int y, CGEventFlags flags) {
	CGEventRef move = CGEventCreateMouseEvent(NULL, kCGEventMouseMoved, CGPointMake(x, y), kCGMouseButtonLeft);
	CGEventSetFlags(move, flags);
	CGEventPost(kCGHIDEventTap, move);
	CFRelease(move);
}

static void executorPostMouseButton(CGEventType eventType, int x, int y, CGMouseButton button, CGEventFlags flags, int clickState) {
	CGEventRef event = CGEventCreateMouseEvent(NULL, eventType, CGPointMake(x, y), button);
	CGEventSetFlags(event, flags);
	if (clickState > 0) {
		CGEventSetIntegerValueField(event, kCGMouseEventClickState, clickState);
	}
	CGEventPost(kCGHIDEventTap, event);
	CFRelease(event);
}

static void executorPostScroll(int x, int y, int scrollX, int scrollY, CGEventFlags flags) {
	CGEventRef event = CGEventCreateScrollWheelEvent(NULL, kCGScrollEventUnitPixel, 2, scrollY, scrollX);
	CGEventSetLocation(event, CGPointMake(x, y));
	CGEventSetFlags(event, flags);
	CGEventPost(kCGHIDEventTap, event);
	CFRelease(event);
}

static void executorPostKey(int keyCode, CGEventFlags flags, int down) {
	CGEventRef event = CGEventCreateKeyboardEvent(NULL, (CGKeyCode)keyCode, down ? true : false);
	CGEventSetFlags(event, flags);
	CGEventPost(kCGHIDEventTap, event);
	CFRelease(event);
}

static int executorMainDisplayWidth(void) {
	return (int)CGDisplayBounds(CGMainDisplayID()).size.width;
}

static int executorMainDisplayHeight(void) {
	return (int)CGDisplayBounds(CGMainDisplayID()).size.height;
}
*/
import "C"

import (
	"context"
	"fmt"
	"strings"
)

type defaultEventPoster struct{}

func mainDisplayDimensions(context.Context) (int, int, error) {
	return int(C.executorMainDisplayWidth()), int(C.executorMainDisplayHeight()), nil
}

func (defaultEventPoster) PostMouse(action MouseAction) error {
	button, err := cgMouseButton(action.Button)
	if err != nil {
		return err
	}
	flags, err := cgEventFlags(action.Keys)
	if err != nil {
		return err
	}
	steps, err := expandMouseAction(action)
	if err != nil {
		return err
	}
	for _, step := range steps {
		switch step.Type {
		case mouseStepMove:
			C.executorPostMouseMove(C.int(step.X), C.int(step.Y), flags)
		case mouseStepDrag:
			C.executorPostMouseButton(C.executorMouseMoveEvent(button), C.int(step.X), C.int(step.Y), button, flags, 0)
		case mouseStepDown:
			C.executorPostMouseButton(C.executorMouseDownEvent(button), C.int(step.X), C.int(step.Y), button, flags, C.int(step.Click))
		case mouseStepUp:
			C.executorPostMouseButton(C.executorMouseUpEvent(button), C.int(step.X), C.int(step.Y), button, flags, C.int(step.Click))
		case mouseStepScroll:
			scrollX, scrollY := nativeWheelDeltas(step.ScrollX, step.ScrollY)
			C.executorPostScroll(C.int(step.X), C.int(step.Y), C.int(scrollX), C.int(scrollY), flags)
		}
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
	case MouseButtonCenter, MouseButtonWheel:
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
