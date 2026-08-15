//go:build !linux

package desktop

import "context"

type backendKind string

const (
	backendX11         backendKind = "x11"
	backendWayland     backendKind = "wayland"
	backendUnavailable backendKind = "unavailable"
)

type linuxBackend struct{}

func newLinuxBackend(runner commandRunner, env map[string]string) linuxBackend {
	return linuxBackend{}
}

func detectLinuxBackend(env map[string]string) backendKind {
	switch {
	case env["WAYLAND_DISPLAY"] != "":
		return backendWayland
	case env["DISPLAY"] != "":
		return backendX11
	default:
		return backendUnavailable
	}
}

func (linuxBackend) Screenshot(ctx context.Context, path string) error {
	return &UnavailableError{Reason: "linux desktop control is unavailable on this platform build"}
}

func (linuxBackend) Windows(ctx context.Context) ([]Window, error) {
	return nil, &UnavailableError{Reason: "linux desktop control is unavailable on this platform build"}
}

func (linuxBackend) Accessibility(ctx context.Context) (AccessibilityTree, error) {
	return AccessibilityTree{}, &UnavailableError{Reason: "linux desktop control is unavailable on this platform build"}
}

func (linuxBackend) Mouse(ctx context.Context, action MouseAction) error {
	return &UnavailableError{Reason: "linux desktop control is unavailable on this platform build"}
}

func (linuxBackend) Keyboard(ctx context.Context, action KeyboardAction) error {
	return &UnavailableError{Reason: "linux desktop control is unavailable on this platform build"}
}

func (linuxBackend) App(ctx context.Context, action AppAction) error {
	return &UnavailableError{Reason: "linux desktop control is unavailable on this platform build"}
}

func (linuxBackend) Available(ctx context.Context) bool {
	return false
}
