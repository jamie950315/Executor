//go:build darwin && cgo

package desktop

/*
#cgo LDFLAGS: -framework ApplicationServices
#include <ApplicationServices/ApplicationServices.h>
static int liveAvailable(){
 CFDictionaryRef s=CGSessionCopyCurrentDictionary();if(!s)return 0;
 CFBooleanRef locked=(CFBooleanRef)CFDictionaryGetValue(s,CFSTR("CGSSessionScreenIsLocked"));
 CFBooleanRef console=(CFBooleanRef)CFDictionaryGetValue(s,kCGSessionOnConsoleKey);
 int ok=console==kCFBooleanTrue && locked!=kCFBooleanTrue;CFRelease(s);
 return ok && AXIsProcessTrusted() && CGPreflightScreenCaptureAccess();
}
static int liveWidth(){return (int)CGDisplayBounds(CGMainDisplayID()).size.width;}
static int liveHeight(){return (int)CGDisplayBounds(CGMainDisplayID()).size.height;}
static int livePost(int type,int button,int down,int x,int y,int key,int sx,int sy,CGEventFlags flags,const UniChar* text,int length){
 CGEventRef e=NULL;
 if(type==0||type==1){CGMouseButton b=button==2?kCGMouseButtonRight:(button==1?kCGMouseButtonCenter:kCGMouseButtonLeft);CGEventType t=kCGEventMouseMoved;
  if(type==1)t=down?(b==0?kCGEventLeftMouseDown:b==1?kCGEventRightMouseDown:kCGEventOtherMouseDown):(b==0?kCGEventLeftMouseUp:b==1?kCGEventRightMouseUp:kCGEventOtherMouseUp);
  else if(down)t=b==0?kCGEventLeftMouseDragged:b==1?kCGEventRightMouseDragged:kCGEventOtherMouseDragged;
  e=CGEventCreateMouseEvent(NULL,t,CGPointMake(x,y),b);
 }else if(type==2){e=CGEventCreateKeyboardEvent(NULL,key,down);}
 else if(type==3){e=CGEventCreateScrollWheelEvent(NULL,kCGScrollEventUnitPixel,2,-sy,-sx);}
 else {e=CGEventCreateKeyboardEvent(NULL,0,down);if(e)CGEventKeyboardSetUnicodeString(e,length,text);}
 if(!e)return 0;CGEventSetFlags(e,flags);CGEventPost(kCGHIDEventTap,e);CFRelease(e);return 1;
}
*/
import "C"
import (
	"errors"
	"github.com/jamie950315/executor/internal/livedesktop"
	"unicode/utf16"
	"unsafe"
)

type liveDarwin struct{}

func (liveDarwin) available() bool { return C.liveAvailable() != 0 }

func newLiveNative() liveNative { return liveDarwin{} }
func (liveDarwin) geometry() (livedesktop.Geometry, error) {
	return livedesktop.Geometry{Width: int(C.liveWidth()), Height: int(C.liveHeight())}, nil
}
func (liveDarwin) event(e livedesktop.InputEvent, x, y int, keys map[string]bool, buttons map[int]bool) error {
	var flags C.CGEventFlags
	for k := range keys {
		switch k {
		case "ShiftLeft", "ShiftRight":
			flags |= C.kCGEventFlagMaskShift
		case "ControlLeft", "ControlRight":
			flags |= C.kCGEventFlagMaskControl
		case "AltLeft", "AltRight":
			flags |= C.kCGEventFlagMaskAlternate
		case "MetaLeft", "MetaRight":
			flags |= C.kCGEventFlagMaskCommand
		}
	}
	typ, key, button, down := 0, 0, e.Button, 0
	if e.Down {
		down = 1
	}
	switch e.Type {
	case "move":
		for b := range buttons {
			button = b
			down = 1
			break
		}
	case "button", "release-button":
		typ = 1
	case "key":
		typ = 2
		var ok bool
		key, ok = liveMacKeys[e.Code]
		if !ok {
			return errors.New("unsupported keyboard code")
		}
	case "wheel":
		typ = 3
	case "text":
		typ = 4
	}
	var p *C.UniChar
	u := utf16.Encode([]rune(e.Text))
	if len(u) > 0 {
		p = (*C.UniChar)(unsafe.Pointer(&u[0]))
	}
	post := func(d int) bool {
		return C.livePost(C.int(typ), C.int(button), C.int(d), C.int(x), C.int(y), C.int(key), C.int(e.ScrollX), C.int(e.ScrollY), flags, p, C.int(len(u))) != 0
	}
	if typ == 4 {
		if !post(1) || !post(0) {
			return errors.New("native text input failed")
		}
	} else if !post(down) {
		return errors.New("native desktop input failed")
	}
	return nil
}

var liveMacKeys = map[string]int{"KeyA": 0, "KeyS": 1, "KeyD": 2, "KeyF": 3, "KeyH": 4, "KeyG": 5, "KeyZ": 6, "KeyX": 7, "KeyC": 8, "KeyV": 9, "KeyB": 11, "KeyQ": 12, "KeyW": 13, "KeyE": 14, "KeyR": 15, "KeyY": 16, "KeyT": 17, "Digit1": 18, "Digit2": 19, "Digit3": 20, "Digit4": 21, "Digit6": 22, "Digit5": 23, "Equal": 24, "Digit9": 25, "Digit7": 26, "Minus": 27, "Digit8": 28, "Digit0": 29, "BracketRight": 30, "KeyO": 31, "KeyU": 32, "BracketLeft": 33, "KeyI": 34, "KeyP": 35, "Enter": 36, "KeyL": 37, "KeyJ": 38, "Quote": 39, "KeyK": 40, "Semicolon": 41, "Backslash": 42, "Comma": 43, "Slash": 44, "KeyN": 45, "KeyM": 46, "Period": 47, "Tab": 48, "Space": 49, "Backquote": 50, "Backspace": 51, "Escape": 53, "MetaRight": 54, "MetaLeft": 55, "ShiftLeft": 56, "CapsLock": 57, "AltLeft": 58, "ControlLeft": 59, "ShiftRight": 60, "AltRight": 61, "ControlRight": 62, "F17": 64, "NumpadDecimal": 65, "NumpadMultiply": 67, "NumpadAdd": 69, "NumLock": 71, "NumpadDivide": 75, "NumpadEnter": 76, "NumpadSubtract": 78, "NumpadEqual": 81, "Numpad0": 82, "Numpad1": 83, "Numpad2": 84, "Numpad3": 85, "Numpad4": 86, "Numpad5": 87, "Numpad6": 88, "Numpad7": 89, "Numpad8": 91, "Numpad9": 92, "F5": 96, "F6": 97, "F7": 98, "F3": 99, "F8": 100, "F9": 101, "F11": 103, "F13": 105, "F16": 106, "F14": 107, "F10": 109, "F12": 111, "F15": 113, "Home": 115, "PageUp": 116, "Delete": 117, "F4": 118, "End": 119, "F2": 120, "PageDown": 121, "F1": 122, "ArrowLeft": 123, "ArrowRight": 124, "ArrowDown": 125, "ArrowUp": 126}
