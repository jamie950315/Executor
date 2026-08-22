//go:build darwin && cgo

package desktop

/*
#cgo LDFLAGS: -framework ApplicationServices -framework CoreGraphics
#include <ApplicationServices/ApplicationServices.h>
#include <CoreGraphics/CoreGraphics.h>

static int executorAccessibilityTrusted(int prompt) {
	if (!prompt) {
		return AXIsProcessTrusted() ? 1 : 0;
	}
	const void *keys[] = { kAXTrustedCheckOptionPrompt };
	const void *values[] = { kCFBooleanTrue };
	CFDictionaryRef options = CFDictionaryCreate(
		kCFAllocatorDefault,
		keys,
		values,
		1,
		&kCFCopyStringDictionaryKeyCallBacks,
		&kCFTypeDictionaryValueCallBacks
	);
	Boolean trusted = AXIsProcessTrustedWithOptions(options);
	CFRelease(options);
	return trusted ? 1 : 0;
}
*/
import "C"

type nativeDarwinPermissionProvider struct{}

func defaultDarwinPermissionProvider() darwinPermissionProvider {
	return nativeDarwinPermissionProvider{}
}

func (nativeDarwinPermissionProvider) Status() darwinPermissionState {
	return darwinPermissionState{
		NativeAvailable: true,
		ScreenRecording: bool(C.CGPreflightScreenCaptureAccess()),
		Accessibility:   C.executorAccessibilityTrusted(0) != 0,
		InputControl:    bool(C.CGPreflightPostEventAccess()),
	}
}

func (p nativeDarwinPermissionProvider) Request() darwinPermissionState {
	C.CGRequestScreenCaptureAccess()
	C.executorAccessibilityTrusted(1)
	C.CGRequestPostEventAccess()
	return p.Status()
}
