package desktop

import (
	"context"
	"errors"
	"github.com/jamie950315/executor/internal/livedesktop"
	"math"
	"sync"
)

type liveNative interface {
	available() bool
	geometry() (livedesktop.Geometry, error)
	event(livedesktop.InputEvent, int, int, map[string]bool, map[int]bool) error
}
type liveBackend struct {
	native  liveNative
	mu      sync.Mutex
	keys    map[string]bool
	buttons map[int]bool
	x, y    int
}

func NewLiveBackend() livedesktop.Backend {
	return &liveBackend{native: newLiveNative(), keys: map[string]bool{}, buttons: map[int]bool{}}
}

func (b *liveBackend) LiveStatus(ctx context.Context) (supported, available bool, reason string) {
	if b.native == nil {
		return false, false, "Live desktop requires macOS with native support or Windows."
	}
	if _, err := liveFFmpeg(); err != nil {
		return false, false, err.Error()
	}
	if !b.Available(ctx) {
		return true, false, "Desktop is locked, unavailable, or screen recording and input permissions are missing."
	}
	return true, true, ""
}
func (b *liveBackend) Available(ctx context.Context) bool {
	return ctx.Err() == nil && b.native != nil && b.native.available()
}
func (b *liveBackend) Geometry(ctx context.Context) (livedesktop.Geometry, error) {
	if err := ctx.Err(); err != nil {
		return livedesktop.Geometry{}, err
	}
	if b.native == nil {
		return livedesktop.Geometry{}, errors.New("live desktop requires macOS with CGO or Windows")
	}
	return b.native.geometry()
}
func (b *liveBackend) Input(ctx context.Context, e livedesktop.InputEvent) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if b.native == nil || !b.native.available() {
		return errors.New("desktop is locked or unavailable")
	}
	switch e.Type {
	case "move", "button", "wheel":
		if math.IsNaN(e.X) || math.IsNaN(e.Y) || e.X < 0 || e.X > 1 || e.Y < 0 || e.Y > 1 {
			return errors.New("invalid pointer coordinates")
		}
		if e.Type == "button" && (e.Button < 0 || e.Button > 2) {
			return errors.New("invalid pointer button")
		}
		g, err := b.native.geometry()
		if err != nil {
			return err
		}
		b.x = min(g.Width-1, int(e.X*float64(g.Width)))
		b.y = min(g.Height-1, int(e.Y*float64(g.Height)))
		if e.Type == "wheel" {
			if e.ScrollX < -4096 || e.ScrollX > 4096 || e.ScrollY < -4096 || e.ScrollY > 4096 {
				return errors.New("wheel input exceeds limit")
			}
			if err := b.native.event(livedesktop.InputEvent{Type: "move"}, b.x, b.y, b.keys, b.buttons); err != nil {
				return err
			}
		}
	case "key":
		if e.Code == "" || len(e.Code) > 40 {
			return errors.New("invalid keyboard code")
		}
	case "text":
		if len(e.Text) > 4096 {
			return errors.New("text input exceeds limit")
		}
	default:
		return errors.New("unsupported live input event")
	}
	// Native posting uses the prospective modifier state, including key release.
	prior := b.keys[e.Code]
	if e.Type == "key" {
		if e.Down {
			b.keys[e.Code] = true
		} else {
			delete(b.keys, e.Code)
		}
	}
	if err := b.native.event(e, b.x, b.y, b.keys, b.buttons); err != nil {
		if e.Type == "key" {
			if prior {
				b.keys[e.Code] = true
			} else {
				delete(b.keys, e.Code)
			}
		}
		return err
	}
	if e.Type == "button" {
		if e.Down {
			b.buttons[e.Button] = true
		} else {
			delete(b.buttons, e.Button)
		}
	}
	return nil
}
func (b *liveBackend) Release(ctx context.Context) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.native == nil {
		return nil
	}
	var errs []error
	for k := range b.keys {
		delete(b.keys, k)
		if err := b.native.event(livedesktop.InputEvent{Type: "key", Code: k}, b.x, b.y, b.keys, b.buttons); err != nil {
			b.keys[k] = true
			errs = append(errs, err)
		}
	}
	for button := range b.buttons {
		if err := b.native.event(livedesktop.InputEvent{Type: "release-button", Button: button}, b.x, b.y, b.keys, b.buttons); err != nil {
			errs = append(errs, err)
		} else {
			delete(b.buttons, button)
		}
	}
	return errors.Join(errs...)
}
