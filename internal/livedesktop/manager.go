package livedesktop

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/pion/logging"
	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"
)

type Config struct {
	Backend Backend
	Revoked func() bool
	// OnControl serializes live-control ownership with other host input paths.
	// It is called without manager/session locks, under inputMu.
	OnControl func(bool)
	ICE       []string
	Lease     time.Duration
}
type Manager struct {
	mu      sync.Mutex
	cfg     Config
	current *liveSession
	closed  bool
}
type liveSession struct {
	manager                        *Manager
	id, owner                      string
	geometry                       Geometry
	pc                             *webrtc.PeerConnection
	ctx                            context.Context
	cancel                         context.CancelFunc
	once                           sync.Once
	done                           chan struct{}
	mu                             sync.Mutex
	source                         VideoSource
	channel                        *webrtc.DataChannel
	expires                        time.Time
	lastInput                      time.Time
	control, connected, videoReady bool
	seq                            uint64
	inputMu                        sync.Mutex
	inputs                         chan receivedInput
}
type receivedInput struct {
	message webrtc.DataChannelMessage
	at      time.Time
}

func NewManager(c Config) *Manager {
	if c.Lease <= 0 {
		c.Lease = 15 * time.Second
	}
	if c.ICE == nil {
		c.ICE = []string{"stun:stun.cloudflare.com:3478"}
	}
	c.ICE = append([]string{}, c.ICE...)
	return &Manager{cfg: c}
}
func (m *Manager) revoked() bool { return m.cfg.Revoked != nil && m.cfg.Revoked() }
func (m *Manager) Status(ctx context.Context) (Status, error) {
	s := Status{Supported: m.cfg.Backend != nil, ICEServers: append([]string{}, m.cfg.ICE...)}
	if s.Supported {
		if probe, ok := m.cfg.Backend.(interface {
			LiveStatus(context.Context) (bool, bool, string)
		}); ok {
			s.Supported, s.Available, s.Reason = probe.LiveStatus(ctx)
		} else {
			_, err := m.cfg.Backend.Geometry(ctx)
			s.Supported = err == nil
			s.Available = s.Supported && m.cfg.Backend.Available(ctx)
		}
		s.Available = s.Available && !m.revoked()
	}
	m.mu.Lock()
	s.Active = m.current != nil
	s.Available = s.Available && !m.closed
	m.mu.Unlock()
	if s.Reason != "" {
		return s, nil
	}
	if !s.Supported {
		s.Reason = "live_desktop_unsupported"
	} else if !s.Available {
		s.Reason = "desktop_unavailable"
	}
	return s, nil
}
func (m *Manager) ActiveControl() bool {
	m.mu.Lock()
	s := m.current
	m.mu.Unlock()
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.control && s.ctx.Err() == nil
}
func (m *Manager) Start(ctx context.Context, owner, offer string, o Options) (result Session, err error) {
	if err = ctx.Err(); err != nil {
		return result, err
	}
	if owner == "" || len(owner) > 1024 || len(offer) == 0 || len(offer) > 65536 {
		return result, errors.New("invalid live desktop request")
	}
	if m.cfg.Backend == nil || m.revoked() || !m.cfg.Backend.Available(ctx) {
		return result, errors.New("desktop unavailable")
	}
	if o.MaxWidth == 0 {
		o.MaxWidth = 1280
	}
	if o.FPS == 0 {
		o.FPS = 15
	}
	if o.Bitrate == 0 {
		o.Bitrate = 2500000
	}
	if o.MaxWidth < 320 || o.MaxWidth > 1920 || o.FPS < 1 || o.FPS > 30 || o.Bitrate < 250000 || o.Bitrate > 8000000 {
		return result, errors.New("invalid video options")
	}
	g, e := m.cfg.Backend.Geometry(ctx)
	if e != nil || g.Width < 1 || g.Height < 1 {
		return result, errors.New("display geometry unavailable")
	}
	var id [32]byte
	if _, e = rand.Read(id[:]); e != nil {
		return result, e
	}
	sessionCtx, cancel := context.WithCancel(context.Background())
	s := &liveSession{manager: m, id: hex.EncodeToString(id[:]), owner: owner, geometry: g, ctx: sessionCtx, cancel: cancel, done: make(chan struct{}), inputs: make(chan receivedInput, 16), expires: time.Now().Add(m.cfg.Lease + 8*time.Second)}
	m.mu.Lock()
	if m.closed || m.current != nil {
		m.mu.Unlock()
		cancel()
		return result, errors.New("live desktop busy or closed")
	}
	m.current = s
	m.mu.Unlock()
	defer func() {
		if err != nil {
			s.finish()
		}
	}()
	go s.inputLoop()
	engine := &webrtc.MediaEngine{}
	if e = engine.RegisterCodec(webrtc.RTPCodecParameters{RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264, ClockRate: 90000, SDPFmtpLine: "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42e01f"}, PayloadType: 102}, webrtc.RTPCodecTypeVideo); e != nil {
		return result, errors.New("video codec unavailable")
	}
	// Default interceptors provide retransmission and RTCP reports.
	settings := webrtc.SettingEngine{}
	settings.SetSCTPMaxReceiveBufferSize(65536)
	settings.SetSCTPMaxMessageSize(8192)
	// Pion debug environment settings must never turn network/SDP details into logs.
	logger := logging.NewDefaultLoggerFactory()
	logger.Writer = io.Discard
	settings.LoggerFactory = logger
	api := webrtc.NewAPI(webrtc.WithMediaEngine(engine), webrtc.WithSettingEngine(settings))
	servers := []webrtc.ICEServer{}
	for _, url := range m.cfg.ICE {
		if !strings.HasPrefix(url, "stun:") {
			return result, errors.New("only credential-free STUN is supported")
		}
		servers = append(servers, webrtc.ICEServer{URLs: []string{url}})
	}
	pc, e := api.NewPeerConnection(webrtc.Configuration{ICEServers: servers})
	if e != nil {
		return result, errors.New("peer creation failed")
	}
	s.mu.Lock()
	if s.ctx.Err() != nil {
		s.mu.Unlock()
		_ = pc.Close()
		return result, errors.New("peer closed")
	}
	s.pc = pc
	s.mu.Unlock()
	track, e := webrtc.NewTrackLocalStaticSample(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264}, "desktop", "executor")
	if e != nil {
		return result, errors.New("video track failed")
	}
	sender, e := pc.AddTrack(track)
	if e != nil {
		return result, errors.New("video track failed")
	}
	go func() {
		buf := make([]byte, 1500)
		for {
			if _, _, e := sender.Read(buf); e != nil {
				return
			}
		}
	}()
	pc.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		switch state {
		case webrtc.PeerConnectionStateConnected:
			s.mu.Lock()
			first := !s.connected
			s.connected = true
			s.mu.Unlock()
			if first {
				go s.video(o, track)
			}
		case webrtc.PeerConnectionStateFailed, webrtc.PeerConnectionStateDisconnected, webrtc.PeerConnectionStateClosed:
			go s.finish()
		}
	})
	pc.OnDataChannel(func(dc *webrtc.DataChannel) {
		s.mu.Lock()
		valid := dc.Label() == "executor-input" && dc.Ordered() && dc.MaxRetransmits() == nil && dc.MaxPacketLifeTime() == nil && s.channel == nil
		if valid {
			s.channel = dc
		}
		s.mu.Unlock()
		if !valid {
			go s.finish()
			return
		}
		dc.OnOpen(func() {
			s.send(map[string]any{"type": "ready", "width": g.Width, "height": g.Height, "control": false})
		})
		dc.OnMessage(func(msg webrtc.DataChannelMessage) {
			if !msg.IsString || len(msg.Data) > 8192 {
				s.sendError("invalid_input")
				go s.finish()
				return
			}
			select {
			case <-s.ctx.Done():
			case s.inputs <- receivedInput{msg, time.Now()}:
			default:
				s.sendError("input_backlog")
				go s.finish()
			}
		})
		dc.OnClose(func() { go s.finish() })
		dc.OnError(func(error) { go s.finish() })
	})
	if e = pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: offer}); e != nil {
		return result, errors.New("invalid peer offer")
	}
	answer, e := pc.CreateAnswer(nil)
	if e != nil {
		return result, errors.New("peer answer failed")
	}
	gathered := webrtc.GatheringCompletePromise(pc)
	if e = pc.SetLocalDescription(answer); e != nil {
		return result, errors.New("peer answer failed")
	}
	timer := time.NewTimer(8 * time.Second)
	defer timer.Stop()
	select {
	case <-gathered:
	case <-ctx.Done():
		return result, ctx.Err()
	case <-s.ctx.Done():
		return result, errors.New("peer closed")
	case <-timer.C:
		return result, errors.New("peer gathering timeout")
	}
	if err = ctx.Err(); err != nil {
		return result, err
	}
	if s.ctx.Err() != nil {
		return result, errors.New("peer closed")
	}
	local := pc.LocalDescription()
	if local == nil || len(local.SDP) > 65536 {
		return result, errors.New("invalid peer answer")
	}
	s.mu.Lock()
	s.expires = time.Now().Add(m.cfg.Lease)
	s.mu.Unlock()
	go s.monitor()
	return Session{SessionID: s.id, Answer: local.SDP, Geometry: g, LeaseSeconds: int(math.Ceil(m.cfg.Lease.Seconds()))}, nil
}
func (m *Manager) lookup(owner, id string) (*liveSession, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.current
	if s == nil || s.id != id || s.owner != owner || s.ctx.Err() != nil {
		return nil, errors.New("live session not found")
	}
	return s, nil
}
func (m *Manager) Renew(owner, id string) error {
	s, e := m.lookup(owner, id)
	if e != nil {
		return e
	}
	s.mu.Lock()
	expired := !time.Now().Before(s.expires)
	if !expired {
		s.expires = time.Now().Add(m.cfg.Lease)
	}
	s.mu.Unlock()
	if expired || m.revoked() {
		s.finish()
		return errors.New("live session expired")
	}
	return nil
}
func (m *Manager) Stop(owner, id string) error {
	s, e := m.lookup(owner, id)
	if e != nil {
		return e
	}
	s.finish()
	return nil
}
func (m *Manager) Close() error {
	m.mu.Lock()
	m.closed = true
	s := m.current
	m.mu.Unlock()
	if s != nil {
		s.finish()
	}
	return nil
}
func (s *liveSession) finish() {
	s.once.Do(func() {
		s.cancel()
		s.mu.Lock()
		source, pc := s.source, s.pc
		s.control = false
		s.mu.Unlock()
		s.inputMu.Lock()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		if s.manager.cfg.Backend.Release(ctx) != nil {
			s.sendError("input_release_failed")
		}
		cancel()
		if s.manager.cfg.OnControl != nil {
			s.manager.cfg.OnControl(false)
		}
		s.inputMu.Unlock()
		if source != nil {
			_ = source.Close()
		}
		if pc != nil {
			_ = pc.Close()
		}
		s.manager.mu.Lock()
		if s.manager.current == s {
			s.manager.current = nil
		}
		s.manager.mu.Unlock()
		close(s.done)
	})
	<-s.done
}
func (s *liveSession) monitor() {
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	startup := time.NewTimer(12 * time.Second)
	defer startup.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-startup.C:
			s.mu.Lock()
			ready := s.videoReady
			s.mu.Unlock()
			if !ready {
				s.sendError("video_start_timeout")
				s.finish()
				return
			}
		case <-tick.C:
			s.mu.Lock()
			expired := !time.Now().Before(s.expires)
			s.mu.Unlock()
			ctx, cancel := context.WithTimeout(s.ctx, time.Second)
			available := s.manager.cfg.Backend.Available(ctx)
			geometry, geometryErr := s.manager.cfg.Backend.Geometry(ctx)
			cancel()
			if expired || s.manager.revoked() || !available || geometryErr != nil || geometry != s.geometry {
				s.sendError("session_ended")
				s.finish()
				return
			}
			s.inputMu.Lock()
			ctx, cancel = context.WithTimeout(s.ctx, time.Second)
			ok := s.expireControl(ctx)
			cancel()
			s.inputMu.Unlock()
			if !ok {
				s.finish()
				return
			}
		}
	}
}
func (s *liveSession) video(o Options, track *webrtc.TrackLocalStaticSample) {
	src, e := s.manager.cfg.Backend.OpenVideo(s.ctx, o)
	if e != nil {
		s.sendError("video_start_failed")
		s.finish()
		return
	}
	s.mu.Lock()
	if s.ctx.Err() != nil {
		s.mu.Unlock()
		_ = src.Close()
		return
	}
	s.source = src
	s.mu.Unlock()
	for {
		sample, e := src.Read(s.ctx)
		if e != nil {
			if s.ctx.Err() == nil {
				s.sendError("video_failed")
			}
			s.finish()
			return
		}
		if len(sample.Data) == 0 || len(sample.Data) > 16<<20 || sample.Duration <= 0 || sample.Duration > time.Second {
			s.sendError("invalid_video_sample")
			s.finish()
			return
		}
		if e = track.WriteSample(media.Sample{Data: sample.Data, Duration: sample.Duration}); e != nil {
			s.finish()
			return
		}
		s.mu.Lock()
		s.videoReady = true
		s.mu.Unlock()
	}
}
func (s *liveSession) send(v any) {
	s.mu.Lock()
	dc := s.channel
	s.mu.Unlock()
	if dc == nil || dc.ReadyState() != webrtc.DataChannelStateOpen {
		return
	}
	if dc.BufferedAmount() > 65536 {
		go s.finish()
		return
	}
	data, _ := json.Marshal(v)
	if dc.SendText(string(data)) != nil {
		go s.finish()
	}
}
func (s *liveSession) sendError(code string) {
	s.send(map[string]string{"type": "error", "code": code})
}
func (s *liveSession) inputLoop() {
	for {
		select {
		case <-s.ctx.Done():
			return
		case input := <-s.inputs:
			if time.Since(input.at) > 500*time.Millisecond {
				s.sendError("input_backlog")
				s.finish()
				return
			}
			s.message(input.message)
		}
	}
}
func (s *liveSession) message(msg webrtc.DataChannelMessage) {
	if !msg.IsString || len(msg.Data) > 8192 {
		s.sendError("invalid_input")
		go s.finish()
		return
	}
	var e InputEvent
	dec := json.NewDecoder(bytes.NewReader(msg.Data))
	dec.DisallowUnknownFields()
	err := dec.Decode(&e)
	var extra any
	if err != nil || dec.Decode(&extra) != io.EOF || validateInput(e) != nil {
		s.sendError("invalid_input")
		go s.finish()
		return
	}
	s.inputMu.Lock()
	defer s.inputMu.Unlock()
	s.mu.Lock()
	if s.ctx.Err() != nil || e.Seq == 0 || e.Seq <= s.seq {
		s.mu.Unlock()
		return
	}
	s.seq = e.Seq
	expired := !time.Now().Before(s.expires)
	s.mu.Unlock()
	ctx, cancel := context.WithTimeout(s.ctx, time.Second)
	defer cancel()
	geometry, geometryErr := s.manager.cfg.Backend.Geometry(ctx)
	if expired || s.manager.revoked() || !s.manager.cfg.Backend.Available(ctx) || geometryErr != nil || geometry != s.geometry {
		go s.finish()
		return
	}
	if !s.expireControl(ctx) {
		go s.finish()
		return
	}
	s.mu.Lock()
	control := s.control
	s.lastInput = time.Now()
	s.mu.Unlock()
	switch e.Type {
	case "ping":
		s.send(map[string]any{"type": "state", "control": control})
		return
	case "release", "control":
		enabled := e.Type == "control" && e.Enabled
		if !enabled {
			if s.manager.cfg.Backend.Release(ctx) != nil {
				s.sendError("input_release_failed")
				go s.finish()
				return
			}
		}
		if s.manager.cfg.OnControl != nil {
			s.manager.cfg.OnControl(enabled)
		}
		s.mu.Lock()
		s.control = enabled
		s.mu.Unlock()
		s.send(map[string]any{"type": "state", "control": enabled})
		return
	}
	if !control {
		s.sendError("read_only")
		return
	}
	if s.manager.cfg.Backend.Input(ctx, e) != nil {
		s.sendError("input_failed")
		go s.finish()
	}
}

// Caller holds inputMu. An authenticated signaling lease cannot keep a held
// key alive when the low-latency input channel has stopped delivering messages.
func (s *liveSession) expireControl(ctx context.Context) bool {
	s.mu.Lock()
	expired := s.control && time.Since(s.lastInput) >= 3*time.Second
	s.mu.Unlock()
	if !expired {
		return true
	}
	if s.manager.cfg.Backend.Release(ctx) != nil {
		s.sendError("input_release_failed")
		return false
	}
	if s.manager.cfg.OnControl != nil {
		s.manager.cfg.OnControl(false)
	}
	s.mu.Lock()
	s.control = false
	s.mu.Unlock()
	s.send(map[string]any{"type": "state", "control": false, "reason": "input_idle"})
	return true
}

func validateInput(e InputEvent) error {
	valid := false
	switch e.Type {
	case "move":
		valid = !math.IsNaN(e.X) && !math.IsNaN(e.Y) && e.X >= 0 && e.X <= 1 && e.Y >= 0 && e.Y <= 1
	case "button":
		valid = e.Button >= 0 && e.Button <= 2 && e.X >= 0 && e.X <= 1 && e.Y >= 0 && e.Y <= 1
	case "key":
		valid = validCode(e.Code)
	case "text":
		valid = len(e.Text) <= 4096 && utf8.ValidString(e.Text) && !strings.ContainsRune(e.Text, 0)
	case "wheel":
		valid = e.ScrollX >= -2000 && e.ScrollX <= 2000 && e.ScrollY >= -2000 && e.ScrollY <= 2000
	case "control", "release", "ping":
		valid = true
	}
	if !valid {
		return errors.New("invalid input")
	}
	return nil
}
func validCode(code string) bool {
	if len(code) == 4 && strings.HasPrefix(code, "Key") && code[3] >= 'A' && code[3] <= 'Z' {
		return true
	}
	if len(code) == 6 && strings.HasPrefix(code, "Digit") && code[5] >= '0' && code[5] <= '9' {
		return true
	}
	for _, v := range strings.Fields("Escape Tab CapsLock ShiftLeft ShiftRight ControlLeft ControlRight AltLeft AltRight MetaLeft MetaRight Space Enter Backspace Delete Insert Home End PageUp PageDown ArrowLeft ArrowRight ArrowUp ArrowDown Minus Equal BracketLeft BracketRight Backslash Semicolon Quote Backquote Comma Period Slash F1 F2 F3 F4 F5 F6 F7 F8 F9 F10 F11 F12") {
		if code == v {
			return true
		}
	}
	return false
}
