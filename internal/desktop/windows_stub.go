//go:build !windows

package desktop

import "context"

type windowsBackend struct{}

func (windowsBackend) Screenshot(ctx context.Context, path string) error {
	return &UnavailableError{Reason: "windows desktop control is unavailable on this platform build"}
}

func (windowsBackend) Windows(ctx context.Context) ([]Window, error) {
	return nil, &UnavailableError{Reason: "windows desktop control is unavailable on this platform build"}
}

func (windowsBackend) Accessibility(ctx context.Context) (AccessibilityTree, error) {
	return AccessibilityTree{}, &UnavailableError{Reason: "windows desktop control is unavailable on this platform build"}
}

func (windowsBackend) Mouse(ctx context.Context, action MouseAction) error {
	return &UnavailableError{Reason: "windows desktop control is unavailable on this platform build"}
}

func (windowsBackend) Keyboard(ctx context.Context, action KeyboardAction) error {
	return &UnavailableError{Reason: "windows desktop control is unavailable on this platform build"}
}

func (windowsBackend) App(ctx context.Context, action AppAction) error {
	return &UnavailableError{Reason: "windows desktop control is unavailable on this platform build"}
}

func (windowsBackend) Available(ctx context.Context) bool {
	return false
}
