//go:build windows

package desktop

import (
	"testing"
	"unsafe"
)

func TestLiveWindowsInputLayout(t *testing.T) {
	if unsafe.Sizeof(uintptr(0)) != 8 {
		t.Skip("release Windows targets are 64-bit")
	}
	if unsafe.Sizeof(liveWinInput{}) != 40 || unsafe.Offsetof(liveWinInput{}.mouse) != 8 || unsafe.Sizeof(liveMouseInput{}) != 32 || unsafe.Sizeof(liveKeyboardInput{}) != 24 {
		t.Fatal("native INPUT layout does not match the Windows 64-bit ABI")
	}
}

func TestLivePhysicalThreadFailsClosed(t *testing.T) {
	locks, unlocks := 0, 0
	cleanup, err := livePhysicalThreadWith(func(uintptr) uintptr { return 0 }, func() { locks++ }, func() { unlocks++ })
	if err == nil || cleanup != nil || locks != 1 || unlocks != 1 {
		t.Fatal("DPI initialization failure did not release thread and reject operation")
	}
}

func TestLivePhysicalThreadRestoresPriorContext(t *testing.T) {
	var calls []uintptr
	locks, unlocks := 0, 0
	cleanup, err := livePhysicalThreadWith(func(v uintptr) uintptr { calls = append(calls, v); return 123 }, func() { locks++ }, func() { unlocks++ })
	if err != nil || cleanup == nil || locks != 1 || unlocks != 0 {
		t.Fatal("DPI setup failed")
	}
	cleanup()
	if len(calls) != 2 || calls[0] != ^uintptr(3) || calls[1] != 123 || unlocks != 1 {
		t.Fatal("prior DPI context was not restored before releasing thread")
	}
}

func TestLiveWTSSessionMustBeActive(t *testing.T) {
	for _, state := range []uint32{1, 2, 3, 4, 5, 6, 7, 8, 9} {
		if liveWTSActive(1, &state, 4) {
			t.Fatal("inactive WTS session accepted")
		}
	}
	active := uint32(0)
	if !liveWTSActive(1, &active, 4) || liveWTSActive(0, &active, 4) || liveWTSActive(1, nil, 4) || liveWTSActive(1, &active, 3) {
		t.Fatal("WTS response validation failed")
	}
}
