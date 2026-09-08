package desktop

import (
	"context"
	"github.com/jamie950315/executor/internal/livedesktop"
	"math"
	"testing"
)

type liveFakeNative struct {
	events      []livedesktop.InputEvent
	unavailable bool
	x, y        int
}

func TestLiveAvailabilityAndWheelUseNativeState(t *testing.T) {
	f := &liveFakeNative{}
	b := &liveBackend{native: f, keys: map[string]bool{}, buttons: map[int]bool{}}
	if !b.Available(context.Background()) {
		t.Fatal("fast native availability required without subprocess controller")
	}
	if err := b.Input(context.Background(), livedesktop.InputEvent{Type: "wheel", X: 1, Y: 1, ScrollY: 120}); err != nil {
		t.Fatal(err)
	}
	if f.x != 99 || f.y != 199 {
		t.Fatal("wheel did not use its own normalized position")
	}
}

func TestLiveNormalizedCenterUsesFullDisplayExtent(t *testing.T) {
	f := &liveFakeNative{}
	b := &liveBackend{native: f, keys: map[string]bool{}, buttons: map[int]bool{}}
	if err := b.Input(context.Background(), livedesktop.InputEvent{Type: "move", X: 0.5, Y: 0.5}); err != nil {
		t.Fatal(err)
	}
	if f.x != 50 || f.y != 100 {
		t.Fatalf("center mapped to %d,%d", f.x, f.y)
	}
}

func (f *liveFakeNative) available() bool { return !f.unavailable }
func (f *liveFakeNative) geometry() (livedesktop.Geometry, error) {
	return livedesktop.Geometry{Width: 100, Height: 200}, nil
}
func (f *liveFakeNative) event(e livedesktop.InputEvent, x, y int, _ map[string]bool, _ map[int]bool) error {
	f.events = append(f.events, e)
	f.x = x
	f.y = y
	return nil
}
func TestLiveInputReleaseAndBounds(t *testing.T) {
	f := &liveFakeNative{}
	b := &liveBackend{native: f, keys: map[string]bool{}, buttons: map[int]bool{}}
	ctx := context.Background()
	for _, e := range []livedesktop.InputEvent{{Type: "key", Code: "ShiftLeft", Down: true}, {Type: "button", Button: 2, Down: true, X: 1, Y: 1}} {
		if err := b.Input(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	if f.x != 99 || f.y != 199 {
		t.Fatal("normalized coordinates must stay inside primary display")
	}
	if err := b.Input(ctx, livedesktop.InputEvent{Type: "move", X: math.NaN()}); err == nil {
		t.Fatal("NaN accepted")
	}
	f.unavailable = true
	if err := b.Input(ctx, livedesktop.InputEvent{Type: "move"}); err == nil {
		t.Fatal("locked input accepted")
	}
	if err := b.Release(ctx); err != nil {
		t.Fatal(err)
	}
	if len(b.keys) != 0 || len(b.buttons) != 0 || len(f.events) != 4 {
		t.Fatal("held state not released")
	}
	if err := b.Release(ctx); err != nil {
		t.Fatal(err)
	}
	if len(f.events) != 4 {
		t.Fatal("release not idempotent")
	}
}
