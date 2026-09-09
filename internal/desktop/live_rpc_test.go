package desktop

import (
	"context"
	"testing"

	"github.com/jamie950315/executor/internal/livedesktop"
)

func TestInputAuthorityInvalidatesSnapshotsAndExcludesModelControl(t *testing.T) {
	a := NewInputAuthority()
	initial := a.epoch
	a.SetLiveControl(true)
	if a.epoch == initial {
		t.Fatal("live takeover retained old capture epoch")
	}
	if err := a.check(initial, true); err == nil {
		t.Fatal("model controls allowed during live input")
	}
	a.SetLiveControl(false)
	if err := a.check(initial, true); err == nil {
		t.Fatal("pre-live snapshot remained valid after release")
	}
	if err := a.check(a.epoch, true); err != nil {
		t.Fatal(err)
	}
}

func TestInputAuthorityEpochDoesNotRepeatAcrossHelperRestarts(t *testing.T) {
	a, b := NewInputAuthority(), NewInputAuthority()
	if a.epoch == 0 || a.epoch == b.epoch {
		t.Fatal("helper restarts reused a capture epoch")
	}
}

type liveRPCFake struct{ owner, id string }

func (f *liveRPCFake) Connectivity(context.Context) (livedesktop.Connectivity, error) {
	return livedesktop.Connectivity{}, nil
}

func (f *liveRPCFake) Start(_ context.Context, owner, offer string, _ livedesktop.Options) (livedesktop.Session, error) {
	f.owner = owner
	return livedesktop.Session{SessionID: "s", Answer: offer}, nil
}
func (f *liveRPCFake) Renew(owner, id string) error { f.owner, f.id = owner, id; return nil }
func (f *liveRPCFake) Stop(owner, id string) error  { f.owner, f.id = owner, id; return nil }
func (f *liveRPCFake) Status(context.Context) (livedesktop.Status, error) {
	return livedesktop.Status{Supported: true}, nil
}

func TestLiveRPCValidationAndOwnerBinding(t *testing.T) {
	f := &liveRPCFake{}
	if _, err := handleLiveRPC(context.Background(), f, []byte(`{"action":"start","owner":"browser-one","offer":"sdp"}`)); err != nil {
		t.Fatal(err)
	}
	if f.owner != "browser-one" {
		t.Fatal("owner scope lost")
	}
	for _, raw := range []string{`{"action":"start","offer":"sdp"}`, `{"action":"renew","owner":"x"}`, `{"action":"unknown","owner":"x"}`, `{"action":"stop","owner":"x","sessionId":"s","untrusted":true}`} {
		if _, err := handleLiveRPC(context.Background(), f, []byte(raw)); err == nil {
			t.Fatalf("invalid request accepted: %s", raw)
		}
	}
}
