package desktop

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"sync"

	"github.com/jamie950315/executor/internal/livedesktop"
)

// InputAuthority serializes physical control across snapshot and live transports.
// Epochs prevent snapshots captured by another process from surviving live input.
type InputAuthority struct {
	mu    sync.Mutex
	epoch uint64
	live  bool
}

func NewInputAuthority() *InputAuthority {
	var seed [8]byte
	if _, err := rand.Read(seed[:]); err != nil {
		panic("cannot initialize desktop capture epoch")
	}
	return &InputAuthority{epoch: (binary.LittleEndian.Uint64(seed[:]) & ((1 << 63) - 1)) + 1}
}
func (a *InputAuthority) SetLiveControl(enabled bool) {
	a.mu.Lock()
	a.live = enabled
	a.epoch++
	a.mu.Unlock()
}
func (a *InputAuthority) check(epoch uint64, mutation bool) error {
	if a.live {
		return errors.New("live remote desktop owns the display; release control before snapshot operations")
	}
	if mutation && epoch != 0 && epoch != a.epoch {
		return errors.New("desktop capture is stale; observe again before controlling")
	}
	return nil
}

type guardedLiveBackend struct {
	livedesktop.Backend
	authority *InputAuthority
}

func GuardLiveBackend(backend livedesktop.Backend, a *InputAuthority) livedesktop.Backend {
	return &guardedLiveBackend{backend, a}
}
func (b *guardedLiveBackend) Input(ctx context.Context, event livedesktop.InputEvent) error {
	b.authority.mu.Lock()
	defer b.authority.mu.Unlock()
	if !b.authority.live {
		return errors.New("live control is not enabled")
	}
	b.authority.epoch++
	return b.Backend.Input(ctx, event)
}
func (b *guardedLiveBackend) Release(ctx context.Context) error {
	b.authority.mu.Lock()
	defer b.authority.mu.Unlock()
	b.authority.epoch++
	return b.Backend.Release(ctx)
}
func (b *guardedLiveBackend) LiveStatus(ctx context.Context) (bool, bool, string) {
	if provider, ok := b.Backend.(interface {
		LiveStatus(context.Context) (bool, bool, string)
	}); ok {
		return provider.LiveStatus(ctx)
	}
	return true, b.Backend.Available(ctx), ""
}

type LiveDesktop interface {
	Start(context.Context, string, string, livedesktop.Options) (livedesktop.Session, error)
	Renew(string, string) error
	Stop(string, string) error
	Status(context.Context) (livedesktop.Status, error)
	Connectivity(context.Context) (livedesktop.Connectivity, error)
}
type HelperOptions struct {
	Live      LiveDesktop
	Authority *InputAuthority
}
type RPCLiveRequest struct {
	Action    string `json:"action"`
	Owner     string `json:"owner,omitempty"`
	SessionID string `json:"sessionId,omitempty"`
	Offer     string `json:"offer,omitempty"`
	MaxWidth  int    `json:"maxWidth,omitempty"`
	FPS       int    `json:"fps,omitempty"`
	Bitrate   int    `json:"bitrate,omitempty"`
}

func handleLiveRPC(ctx context.Context, live LiveDesktop, raw []byte) (any, error) {
	if live == nil {
		return nil, errors.New("live remote desktop is unavailable in this helper")
	}
	var r RPCLiveRequest
	if err := decodeStrictParams("desktop.live", raw, &r); err != nil {
		return nil, err
	}
	if r.Owner == "" || len(r.Owner) > 1024 {
		return nil, errors.New("live desktop owner is required")
	}
	switch r.Action {
	case "ice":
		return live.Connectivity(ctx)
	case "status":
		return live.Status(ctx)
	case "start":
		if r.Offer == "" || len(r.Offer) > 65536 {
			return nil, errors.New("live desktop offer is invalid")
		}
		return live.Start(ctx, r.Owner, r.Offer, livedesktop.Options{MaxWidth: r.MaxWidth, FPS: r.FPS, Bitrate: r.Bitrate})
	case "renew", "stop":
		if r.SessionID == "" || len(r.SessionID) > 128 {
			return nil, errors.New("live desktop session is required")
		}
		var err error
		if r.Action == "renew" {
			err = live.Renew(r.Owner, r.SessionID)
		} else {
			err = live.Stop(r.Owner, r.SessionID)
		}
		return map[string]bool{"ok": err == nil}, err
	default:
		return nil, errors.New("unsupported live desktop action")
	}
}
