//go:build windows

package desktop

import (
	"errors"
	"github.com/jamie950315/executor/internal/livedesktop"
	"runtime"
	"strconv"
	"strings"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

var liveUser32 = windows.NewLazySystemDLL("user32.dll")
var liveSendInput = liveUser32.NewProc("SendInput")
var liveSetCursor = liveUser32.NewProc("SetCursorPos")
var liveGetCursor = liveUser32.NewProc("GetCursorPos")
var liveMetrics = liveUser32.NewProc("GetSystemMetrics")
var liveDPI = liveUser32.NewProc("SetThreadDpiAwarenessContext")
var liveOpenDesktop = liveUser32.NewProc("OpenInputDesktop")
var liveCloseDesktop = liveUser32.NewProc("CloseDesktop")
var liveDesktopInfo = liveUser32.NewProc("GetUserObjectInformationW")
var liveWTS = windows.NewLazySystemDLL("wtsapi32.dll")
var liveWTSQuery = liveWTS.NewProc("WTSQuerySessionInformationW")
var liveWTSFree = liveWTS.NewProc("WTSFreeMemory")

type liveMouseInput struct {
	dx, dy            int32
	data, flags, time uint32
	extra             uintptr
}
type liveWinInput struct {
	kind  uint32
	mouse liveMouseInput
}
type liveKeyboardInput struct {
	vk, scan    uint16
	flags, time uint32
	extra       uintptr
}
type liveWindows struct{}

func (liveWindows) available() bool {
	// A disconnected/logged-out session can still have a Default desktop object.
	// Require WTSActive independently before allowing capture or input.
	var buffer *uint32
	var bytes uint32
	queried, _, _ := liveWTSQuery.Call(0, uintptr(^uint32(0)), 8, uintptr(unsafe.Pointer(&buffer)), uintptr(unsafe.Pointer(&bytes)))
	if buffer != nil {
		defer liveWTSFree.Call(uintptr(unsafe.Pointer(buffer)))
	}
	if !liveWTSActive(queried, buffer, bytes) {
		return false
	}
	h, _, _ := liveOpenDesktop.Call(0, 0, 0x101)
	if h == 0 {
		return false
	}
	defer liveCloseDesktop.Call(h)
	var name [256]uint16
	var size uint32
	ok, _, _ := liveDesktopInfo.Call(h, 2, uintptr(unsafe.Pointer(&name[0])), uintptr(len(name)*2), uintptr(unsafe.Pointer(&size)))
	return ok != 0 && windows.UTF16ToString(name[:]) == "Default"
}

func liveWTSActive(ok uintptr, state *uint32, bytes uint32) bool {
	return ok != 0 && state != nil && bytes >= 4 && *state == 0
}

func newLiveNative() liveNative { return liveWindows{} }
func livePhysicalThread() (func(), error) {
	if err := liveDPI.Find(); err != nil {
		return nil, errors.New("physical-pixel DPI context is unavailable")
	}
	return livePhysicalThreadWith(func(v uintptr) uintptr { old, _, _ := liveDPI.Call(v); return old }, runtime.LockOSThread, runtime.UnlockOSThread)
}
func livePhysicalThreadWith(set func(uintptr) uintptr, lock, unlock func()) (func(), error) {
	lock()
	old := set(^uintptr(3))
	if old == 0 {
		unlock()
		return nil, errors.New("physical-pixel DPI context could not be established")
	}
	return func() {
		set(old)
		unlock()
	}, nil
}
func (liveWindows) geometry() (livedesktop.Geometry, error) {
	restore, err := livePhysicalThread()
	if err != nil {
		return livedesktop.Geometry{}, err
	}
	defer restore()
	w, _, _ := liveMetrics.Call(0)
	h, _, _ := liveMetrics.Call(1)
	if w == 0 || h == 0 {
		return livedesktop.Geometry{}, errors.New("primary display unavailable")
	}
	return livedesktop.Geometry{Width: int(w), Height: int(h)}, nil
}
func liveWinSend(in liveWinInput) error {
	n, _, _ := liveSendInput.Call(1, uintptr(unsafe.Pointer(&in)), unsafe.Sizeof(in))
	if n != 1 {
		return errors.New("native input rejected; check active desktop and application privilege")
	}
	return nil
}
func liveWinKey(vk, scan uint16, flags uint32) error {
	in := liveWinInput{kind: 1}
	k := (*liveKeyboardInput)(unsafe.Pointer(&in.mouse))
	k.vk = vk
	k.scan = scan
	k.flags = flags
	return liveWinSend(in)
}
func (liveWindows) event(e livedesktop.InputEvent, x, y int, keys map[string]bool, buttons map[int]bool) error {
	restore, err := livePhysicalThread()
	if err != nil {
		return err
	}
	defer restore()
	switch e.Type {
	case "move", "button", "release-button":
		if e.Type != "release-button" {
			n, _, _ := liveSetCursor.Call(uintptr(x), uintptr(y))
			var p struct{ x, y int32 }
			ok, _, _ := liveGetCursor.Call(uintptr(unsafe.Pointer(&p)))
			if n == 0 || ok == 0 || int(p.x) != x || int(p.y) != y {
				return errors.New("pointer target cannot be reached")
			}
			if e.Type == "move" {
				return nil
			}
		}
		flags := uint32(2)
		switch e.Button {
		case 1:
			flags = 0x20
		case 2:
			flags = 8
		}
		if !e.Down {
			flags *= 2
		}
		return liveWinSend(liveWinInput{mouse: liveMouseInput{flags: flags}})
	case "wheel":
		if e.ScrollY != 0 {
			if err := liveWinSend(liveWinInput{mouse: liveMouseInput{flags: 0x800, data: uint32(-e.ScrollY)}}); err != nil {
				return err
			}
		}
		if e.ScrollX != 0 {
			return liveWinSend(liveWinInput{mouse: liveMouseInput{flags: 0x1000, data: uint32(e.ScrollX)}})
		}
		return nil
	case "key":
		vk, extended, ok := liveWinCode(e.Code)
		if !ok {
			return errors.New("unsupported keyboard code")
		}
		var flags uint32
		if !e.Down {
			flags |= 2
		}
		if extended {
			flags |= 1
		}
		return liveWinKey(vk, 0, flags)
	case "text":
		for _, unit := range utf16.Encode([]rune(e.Text)) {
			if err := liveWinKey(0, unit, 4); err != nil {
				return err
			}
			if err := liveWinKey(0, unit, 6); err != nil {
				return err
			}
		}
		return nil
	}
	return errors.New("unsupported live input event")
}
func liveWinCode(code string) (uint16, bool, bool) {
	if len(code) == 4 && strings.HasPrefix(code, "Key") && code[3] >= 'A' && code[3] <= 'Z' {
		return uint16(code[3]), false, true
	}
	if len(code) == 6 && strings.HasPrefix(code, "Digit") && code[5] >= '0' && code[5] <= '9' {
		return uint16(code[5]), false, true
	}
	if len(code) == 7 && strings.HasPrefix(code, "Numpad") && code[6] >= '0' && code[6] <= '9' {
		return uint16(code[6] - '0' + 0x60), false, true
	}
	if strings.HasPrefix(code, "F") {
		if n, err := strconv.Atoi(code[1:]); err == nil && n >= 1 && n <= 24 {
			return uint16(0x6f + n), false, true
		}
	}
	v, ok := liveWinKeys[code]
	return v & 255, v&256 != 0, ok
}

var liveWinKeys = map[string]uint16{"Backspace": 8, "Tab": 9, "Enter": 13, "NumpadEnter": 269, "ShiftLeft": 0xa0, "ShiftRight": 0xa1, "ControlLeft": 0xa2, "ControlRight": 0x1a3, "AltLeft": 0xa4, "AltRight": 0x1a5, "Pause": 19, "CapsLock": 20, "Escape": 27, "Space": 32, "PageUp": 0x121, "PageDown": 0x122, "End": 0x123, "Home": 0x124, "ArrowLeft": 0x125, "ArrowUp": 0x126, "ArrowRight": 0x127, "ArrowDown": 0x128, "Insert": 0x12d, "Delete": 0x12e, "MetaLeft": 0x15b, "MetaRight": 0x15c, "ContextMenu": 0x15d, "NumpadMultiply": 106, "NumpadAdd": 107, "NumpadSubtract": 109, "NumpadDecimal": 110, "NumpadDivide": 0x16f, "NumLock": 0x190, "ScrollLock": 145, "Semicolon": 186, "Equal": 187, "Comma": 188, "Minus": 189, "Period": 190, "Slash": 191, "Backquote": 192, "BracketLeft": 219, "Backslash": 220, "BracketRight": 221, "Quote": 222}
