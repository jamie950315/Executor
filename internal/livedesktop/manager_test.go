package livedesktop

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"
)

func TestRejectInvalidOffers(t *testing.T) {
	m := NewManager(Config{})
	defer m.Close()
	if _, err := m.Start(context.Background(), "", "bad", Options{}); err == nil {
		t.Fatal("accepted missing owner")
	}
	if _, err := m.Start(context.Background(), "owner", "bad", Options{}); err == nil {
		t.Fatal("accepted unsupported backend")
	}
	if err := m.Renew("owner", "missing"); err == nil {
		t.Fatal("renewed unknown session")
	}
}

type fakeBackend struct {
	unavailable atomic.Bool
	resized     atomic.Bool
	opens       atomic.Int32
	releases    atomic.Int32
	closed      atomic.Int32
	events      chan InputEvent
}

func (b *fakeBackend) Geometry(context.Context) (Geometry, error) {
	if b.resized.Load() {
		return Geometry{1280, 720}, nil
	}
	return Geometry{1920, 1080}, nil
}
func (b *fakeBackend) Available(context.Context) bool { return !b.unavailable.Load() }
func (b *fakeBackend) OpenVideo(ctx context.Context, _ Options) (VideoSource, error) {
	b.opens.Add(1)
	return &fakeVideo{ctx: ctx, b: b, done: make(chan struct{})}, nil
}
func (b *fakeBackend) Input(_ context.Context, e InputEvent) error { b.events <- e; return nil }
func (b *fakeBackend) Release(context.Context) error               { b.releases.Add(1); return nil }

type fakeVideo struct {
	ctx  context.Context
	b    *fakeBackend
	once sync.Once
	done chan struct{}
}

func (v *fakeVideo) Close() error { v.once.Do(func() { close(v.done); v.b.closed.Add(1) }); return nil }
func (v *fakeVideo) Read(ctx context.Context) (Sample, error) {
	select {
	case <-ctx.Done():
		return Sample{}, ctx.Err()
	case <-v.done:
		return Sample{}, context.Canceled
	case <-time.After(10 * time.Millisecond):
		return Sample{Data: []byte{0, 0, 0, 1, 0x65, 0x88, 0x84}, Duration: time.Second / 30}, nil
	}
}

func connectPeer(t *testing.T, m *Manager) (Session, *webrtc.PeerConnection, *webrtc.DataChannel, chan map[string]any, chan struct{}) {
	return connectPeerWithConfig(t, m, webrtc.Configuration{})
}

func connectPeerWithConfig(t *testing.T, m *Manager, config webrtc.Configuration) (Session, *webrtc.PeerConnection, *webrtc.DataChannel, chan map[string]any, chan struct{}) {
	t.Helper()
	pc, err := webrtc.NewPeerConnection(config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pc.Close() })
	if _, err = pc.AddTransceiverFromKind(webrtc.RTPCodecTypeVideo, webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionRecvonly}); err != nil {
		t.Fatal(err)
	}
	dc, err := pc.CreateDataChannel("executor-input", nil)
	if err != nil {
		t.Fatal(err)
	}
	messages := make(chan map[string]any, 32)
	dc.OnMessage(func(msg webrtc.DataChannelMessage) {
		var v map[string]any
		if json.Unmarshal(msg.Data, &v) == nil {
			messages <- v
		}
	})
	video := make(chan struct{}, 1)
	pc.OnTrack(func(track *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		if _, _, e := track.ReadRTP(); e == nil {
			video <- struct{}{}
		}
	})
	offer, err := pc.CreateOffer(nil)
	if err != nil {
		t.Fatal(err)
	}
	gathered := webrtc.GatheringCompletePromise(pc)
	if err = pc.SetLocalDescription(offer); err != nil {
		t.Fatal(err)
	}
	select {
	case <-gathered:
	case <-time.After(5 * time.Second):
		local := pc.LocalDescription()
		if local != nil {
			t.Fatalf("client gather timed out (relay candidates: %d)", strings.Count(local.SDP, " typ relay"))
		}
		t.Fatal("client gather timed out without a local description")
	}
	signaling, cancelSignaling := context.WithCancel(context.Background())
	s, err := m.Start(signaling, "owner", pc.LocalDescription().SDP, Options{})
	cancelSignaling() // Signaling RPC completion must not cancel the leased video.
	if err != nil {
		t.Fatal(err)
	}
	if err = pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: s.Answer}); err != nil {
		t.Fatal(err)
	}
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for {
		select {
		case message := <-messages:
			if message["type"] == "ready" {
				return s, pc, dc, messages, video
			}
		case <-timer.C:
			t.Fatalf("missing ready: peer=%s ice=%s", pc.ConnectionState(), pc.ICEConnectionState())
		}
	}
}
func waitMessage(t *testing.T, ch chan map[string]any, kind string) map[string]any {
	t.Helper()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for {
		select {
		case v := <-ch:
			if v["type"] == kind {
				return v
			}
		case <-timer.C:
			t.Fatalf("missing %s", kind)
			return nil
		}
	}
}
func await(t *testing.T, f func() bool) {
	t.Helper()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		if f() {
			return
		}
		select {
		case <-timer.C:
			t.Fatal("condition timed out")
		case <-tick.C:
		}
	}
}
func TestLoopbackVideoInputAuthorizationAndCleanup(t *testing.T) {
	b := &fakeBackend{events: make(chan InputEvent, 32)}
	var ownership atomic.Bool
	var m *Manager
	m = NewManager(Config{Backend: b, ICE: []string{}, OnControl: func(enabled bool) {
		_ = m.ActiveControl() // Callback must not hold manager/session locks.
		ownership.Store(enabled)
	}})
	defer m.Close()
	s, pc, dc, messages, video := connectPeer(t, m)
	select {
	case <-video:
	case <-time.After(5 * time.Second):
		t.Fatal("no real RTP video received")
	}
	if err := m.Renew("intruder", s.SessionID); err == nil {
		t.Fatal("cross owner renewal")
	}
	if err := m.Stop("intruder", s.SessionID); err == nil {
		t.Fatal("cross owner stop")
	}
	if _, err := m.Start(context.Background(), "owner", "bad", Options{}); err == nil {
		t.Fatal("parallel session accepted")
	}
	send := func(e InputEvent) {
		raw, _ := json.Marshal(e)
		if err := dc.SendText(string(raw)); err != nil {
			t.Fatal(err)
		}
	}
	send(InputEvent{Type: "key", Seq: 1, Code: "KeyA", Down: true})
	if v := waitMessage(t, messages, "error"); v["code"] != "read_only" {
		t.Fatal(v)
	}
	send(InputEvent{Type: "control", Seq: 2, Enabled: true})
	waitMessage(t, messages, "state")
	if !m.ActiveControl() {
		t.Fatal("control inactive")
	}
	if !ownership.Load() {
		t.Fatal("state acknowledged before acquiring input ownership")
	}
	send(InputEvent{Type: "key", Seq: 3, Code: "KeyA", Down: true})
	select {
	case e := <-b.events:
		if e.Code != "KeyA" {
			t.Fatal(e)
		}
	case <-time.After(time.Second):
		t.Fatal("missing input")
	}
	send(InputEvent{Type: "key", Seq: 3, Code: "KeyB", Down: true})
	send(InputEvent{Type: "release", Seq: 4})
	waitMessage(t, messages, "state")
	if m.ActiveControl() {
		t.Fatal("release retained control")
	}
	if ownership.Load() {
		t.Fatal("release retained input ownership")
	}
	if len(b.events) != 0 {
		t.Fatal("replayed input accepted")
	}
	if err := m.Renew("owner", s.SessionID); err != nil {
		t.Fatal(err)
	}
	_ = pc.Close()
	// ICE disconnect detection is intentionally fail closed; explicit channel close
	// can arrive sooner, while the bounded lease covers abrupt network loss.
	if err := m.Stop("owner", s.SessionID); err != nil {
		// Peer-close may cancel the context before asynchronous cleanup clears
		// current. Require cleanup to complete, not an instantaneous snapshot.
		await(t, func() bool { st, _ := m.Status(context.Background()); return !st.Active })
	}
	await(t, func() bool { return b.closed.Load() == 1 && b.releases.Load() >= 2 })
	st, _ := m.Status(context.Background())
	if st.Active {
		t.Fatal("session survived stop")
	}
}
func TestLoopbackLeaseAndLock(t *testing.T) {
	for _, reason := range []string{"lease", "lock", "revocation"} {
		t.Run(reason, func(t *testing.T) {
			b := &fakeBackend{events: make(chan InputEvent, 4)}
			var revoked atomic.Bool
			m := NewManager(Config{Backend: b, ICE: []string{}, Lease: 1500 * time.Millisecond, Revoked: revoked.Load})
			defer m.Close()
			_, _, _, _, video := connectPeer(t, m)
			select {
			case <-video:
			case <-time.After(time.Second):
				t.Fatal("missing video")
			}
			if reason == "lock" {
				b.unavailable.Store(true)
			}
			if reason == "revocation" {
				revoked.Store(true)
			}
			await(t, func() bool { st, _ := m.Status(context.Background()); return !st.Active })
			if b.closed.Load() != 1 || b.releases.Load() == 0 {
				t.Fatal("resources not released")
			}
		})
	}
}

func TestMalformedChannelInputFailsClosed(t *testing.T) {
	for _, raw := range []string{`{"type":"key","seq":1,"code":"KeyA","unknown":true}`, `{"type":"control","seq":1} {}`, `{"type":"button","seq":1,"button":9}`, `{"type":"wheel","seq":1,"scrollY":2001}`, `{"type":"key","seq":1,"code":"exec"}`} {
		t.Run(raw, func(t *testing.T) {
			b := &fakeBackend{events: make(chan InputEvent, 4)}
			m := NewManager(Config{Backend: b, ICE: []string{}})
			ctx, cancel := context.WithCancel(context.Background())
			s := &liveSession{manager: m, ctx: ctx, cancel: cancel, done: make(chan struct{}), control: true, expires: time.Now().Add(time.Second)}
			m.current = s
			s.message(webrtc.DataChannelMessage{IsString: true, Data: []byte(raw)})
			select {
			case <-s.done:
			case <-time.After(time.Second):
				t.Fatal("malformed channel remained active")
			}
			if len(b.events) != 0 || b.releases.Load() != 1 {
				t.Fatal("invalid input executed or input not released")
			}
		})
	}
}

func TestGeometryMutationEndsSession(t *testing.T) {
	b := &fakeBackend{events: make(chan InputEvent, 4)}
	m := NewManager(Config{Backend: b, ICE: []string{}})
	defer m.Close()
	_, _, dc, messages, _ := connectPeer(t, m)
	_ = dc.SendText(`{"type":"control","seq":1,"enabled":true}`)
	waitMessage(t, messages, "state")
	b.resized.Store(true)
	await(t, func() bool { st, _ := m.Status(context.Background()); return !st.Active })
	if b.releases.Load() == 0 || m.ActiveControl() {
		t.Fatal("geometry mutation retained input")
	}
}

func TestInputChecksGeometryBeforePosting(t *testing.T) {
	b := &fakeBackend{events: make(chan InputEvent, 4)}
	m := NewManager(Config{Backend: b, ICE: []string{}})
	ctx, cancel := context.WithCancel(context.Background())
	s := &liveSession{manager: m, ctx: ctx, cancel: cancel, done: make(chan struct{}), geometry: Geometry{1920, 1080}, control: true, expires: time.Now().Add(time.Second)}
	m.current = s
	b.resized.Store(true)
	s.message(webrtc.DataChannelMessage{IsString: true, Data: []byte(`{"type":"move","seq":1,"x":0.5,"y":0.5}`)})
	select {
	case <-s.done:
	case <-time.After(time.Second):
		t.Fatal("geometry mutation did not stop input")
	}
	if len(b.events) != 0 {
		t.Fatal("posted input against changed geometry")
	}
}

func TestControlHeartbeatExpiresWithoutEndingVideo(t *testing.T) {
	b := &fakeBackend{events: make(chan InputEvent, 4)}
	var owned atomic.Bool
	m := NewManager(Config{Backend: b, ICE: []string{}, OnControl: owned.Store})
	defer m.Close()
	s, _, dc, messages, _ := connectPeer(t, m)
	_ = dc.SendText(`{"type":"control","seq":1,"enabled":true}`)
	waitMessage(t, messages, "state")
	_ = dc.SendText(`{"type":"key","seq":2,"code":"KeyA","down":true}`)
	select {
	case <-b.events:
	case <-time.After(time.Second):
		t.Fatal("missing held key")
	}
	if err := m.Renew("owner", s.SessionID); err != nil {
		t.Fatal(err)
	}
	await(t, func() bool { return !m.ActiveControl() })
	if owned.Load() || b.releases.Load() == 0 {
		t.Fatal("heartbeat timeout did not release input ownership")
	}
	st, _ := m.Status(context.Background())
	if !st.Active {
		t.Fatal("control timeout unnecessarily ended video")
	}
}

func TestStaleInputCannotRestoreControlBeforeWatchdog(t *testing.T) {
	b := &fakeBackend{events: make(chan InputEvent, 4)}
	m := NewManager(Config{Backend: b, ICE: []string{}})
	defer m.Close()
	ctx, cancel := context.WithCancel(context.Background())
	s := &liveSession{manager: m, ctx: ctx, cancel: cancel, done: make(chan struct{}), geometry: Geometry{1920, 1080}, control: true, lastInput: time.Now().Add(-4 * time.Second), expires: time.Now().Add(time.Minute)}
	m.current = s
	s.message(webrtc.DataChannelMessage{IsString: true, Data: []byte(`{"type":"key","seq":1,"code":"KeyA","down":true}`)})
	if len(b.events) != 0 || m.ActiveControl() || b.releases.Load() != 1 {
		t.Fatal("stale channel restored held input")
	}
	s.message(webrtc.DataChannelMessage{IsString: true, Data: []byte(`{"type":"control","seq":2,"enabled":true}`)})
	if !m.ActiveControl() {
		t.Fatal("explicit control request failed")
	}
	s.mu.Lock()
	s.lastInput = time.Now().Add(-2 * time.Second)
	s.mu.Unlock()
	s.message(webrtc.DataChannelMessage{IsString: true, Data: []byte(`{"type":"ping","seq":3}`)})
	s.mu.Lock()
	fresh := time.Since(s.lastInput) < time.Second
	s.mu.Unlock()
	if !fresh || !m.ActiveControl() {
		t.Fatal("fresh ping did not retain control")
	}
}

func TestInputValidation(t *testing.T) {
	for _, e := range []InputEvent{{Type: "move", X: 2}, {Type: "key", Code: "invalid"}, {Type: "button", Button: 3}, {Type: "text", Text: string(make([]byte, 4097))}} {
		if validateInput(e) == nil {
			t.Fatalf("accepted invalid %s", e.Type)
		}
	}
	for _, e := range []InputEvent{{Type: "move", X: 0.5, Y: 1}, {Type: "key", Code: "KeyA"}, {Type: "control", Enabled: true}, {Type: "release"}} {
		if err := validateInput(e); err != nil {
			t.Fatalf("rejected %s: %v", e.Type, err)
		}
	}
}
