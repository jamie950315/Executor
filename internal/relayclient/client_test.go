package relayclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/jamie950315/executor/internal/config"
	"github.com/jamie950315/executor/internal/control"
	"github.com/jamie950315/executor/internal/mcp"
	"github.com/jamie950315/executor/internal/relay"
	"github.com/jamie950315/executor/internal/secrets"
)

type cancellableDispatcher struct {
	started  chan struct{}
	canceled chan struct{}
}

type blockingRotatingLifecycle struct {
	inner   *rotatingLifecycle
	started chan struct{}
	release chan struct{}
}

func (l *blockingRotatingLifecycle) Kill(ctx context.Context) (control.Result, error) {
	close(l.started)
	select {
	case <-l.release:
		return l.inner.Kill(ctx)
	case <-ctx.Done():
		return control.Result{}, ctx.Err()
	}
}

func (l *blockingRotatingLifecycle) Resume(ctx context.Context) error { return l.inner.Resume(ctx) }

func (d *cancellableDispatcher) Dispatch(ctx context.Context, _ mcp.ToolCall) (any, error) {
	close(d.started)
	<-ctx.Done()
	close(d.canceled)
	return nil, ctx.Err()
}

func TestReconnectBackoffDoublesToThirtySecondsWithBoundedJitter(t *testing.T) {
	t.Parallel()
	wantBase := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 30 * time.Second, 30 * time.Second}
	for attempt, base := range wantBase {
		minimum := base * 80 / 100
		maximum := base * 120 / 100
		for _, sample := range []uint64{0, ^uint64(0) / 2, ^uint64(0)} {
			got := reconnectDelay(attempt, sample)
			if got < minimum || got > maximum {
				t.Fatalf("attempt %d sample %d delay = %v, want %v..%v", attempt, sample, got, minimum, maximum)
			}
		}
	}
}

func TestClientResetsReconnectBackoffAfterAuthenticatedConnection(t *testing.T) {
	configPath, cfg, values := connectedRelayFixture(t)
	now := time.Unix(1_700_000_000, 0)
	connected := make(chan *fakeSocket, 1)
	thirdDelay := make(chan struct{})
	var dialCount atomic.Int32
	var delayMu sync.Mutex
	var delays []time.Duration
	client, err := NewClient(ClientOptions{
		ConfigPath: configPath, ExecutorVersion: "test-version", HeartbeatInterval: time.Hour,
		Now: func() time.Time { return now },
		Dial: func(context.Context, string, *http.Client) (relaySocket, error) {
			if dialCount.Add(1) < 3 {
				return nil, errors.New("temporary relay failure")
			}
			socket := newFakeSocket()
			connected <- socket
			return socket, nil
		},
		Sleep: func(ctx context.Context, delay time.Duration) error {
			delayMu.Lock()
			delays = append(delays, delay)
			count := len(delays)
			delayMu.Unlock()
			if count == 3 {
				close(thirdDelay)
				<-ctx.Done()
				return ctx.Err()
			}
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- client.Run(ctx) }()
	var socket *fakeSocket
	select {
	case socket = <-connected:
	case <-time.After(time.Second):
		cancel()
		t.Fatal("client did not reach an authenticated connection")
	}
	completeFakeHandshake(t, socket, cfg, values, now)
	_ = socket.Close(0, "test disconnect")
	select {
	case <-thirdDelay:
	case <-time.After(time.Second):
		cancel()
		t.Fatal("client did not schedule reconnect after authenticated disconnect")
	}
	delayMu.Lock()
	got := append([]time.Duration(nil), delays...)
	delayMu.Unlock()
	if len(got) != 3 || got[0] < 800*time.Millisecond || got[0] > 1200*time.Millisecond ||
		got[1] < 1600*time.Millisecond || got[1] > 2400*time.Millisecond ||
		got[2] < 800*time.Millisecond || got[2] > 1200*time.Millisecond {
		cancel()
		t.Fatalf("reconnect delays after authentication = %v, want approximately [1s 2s 1s]", got)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("client did not stop")
	}
}

func TestClientRuntimeStatusTracksAuthenticatedSocketAndDisconnect(t *testing.T) {
	configPath, cfg, values := connectedRelayFixture(t)
	now := time.Unix(1_700_000_000, 0).UTC()
	connected := make(chan *fakeSocket, 1)
	client, err := NewClient(ClientOptions{
		ConfigPath: configPath, ExecutorVersion: "test-version", HeartbeatInterval: time.Hour,
		Now: func() time.Time { return now },
		Dial: func(context.Context, string, *http.Client) (relaySocket, error) {
			socket := newFakeSocket()
			connected <- socket
			return socket, nil
		},
		Sleep: func(ctx context.Context, _ time.Duration) error {
			<-ctx.Done()
			return ctx.Err()
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	statusMethod := reflect.ValueOf(client).MethodByName("Status")
	if !statusMethod.IsValid() {
		t.Fatal("relay client has no metadata-only runtime Status method")
	}
	state := func() string {
		values := statusMethod.Call(nil)
		if len(values) != 1 {
			t.Fatal("relay client Status returned an invalid value count")
		}
		field := values[0].FieldByName("State")
		if !field.IsValid() || field.Kind() != reflect.String {
			t.Fatal("relay client Status omitted state")
		}
		return field.String()
	}
	waitState := func(want string) {
		deadline := time.Now().Add(time.Second)
		for time.Now().Before(deadline) {
			if state() == want {
				return
			}
			time.Sleep(time.Millisecond)
		}
		t.Fatalf("relay state = %q, want %q", state(), want)
	}
	if got := state(); got != "disconnected" {
		t.Fatalf("initial relay state = %q, want disconnected", got)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- client.Run(ctx) }()
	socket := <-connected
	completeFakeHandshake(t, socket, cfg, values, now)
	waitState("connected")
	_ = socket.Close(0, "test disconnect")
	waitState("disconnected")
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestResultMessagesUseOneResponseOrOrderedLosslessChunksWithinBounds(t *testing.T) {
	t.Parallel()
	small := []byte(`{"ready":true}`)
	messages, err := resultMessages("request-small", small, 1024, 4096)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 || messages[0].Type != relay.MessageTypeResponse {
		t.Fatalf("small result messages = %#v", messages)
	}
	payload, err := relay.DecodePayload[relay.ResponsePayload](messages[0])
	if err != nil || !bytes.Equal(payload.Result, small) {
		t.Fatalf("small response payload = %s err=%v", payload.Result, err)
	}

	large := bytes.Repeat([]byte("0123456789abcdef"), 80)
	messages, err = resultMessages("request-large", large, 300, 16*1024)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) < 2 {
		t.Fatalf("large result used %d messages, want chunks", len(messages))
	}
	var rebuilt []byte
	for sequence, message := range messages {
		encoded, err := json.Marshal(message)
		if err != nil {
			t.Fatal(err)
		}
		if len(encoded) > 300 || message.Type != relay.MessageTypeStreamChunk {
			t.Fatalf("chunk %d size/type = %d/%s", sequence, len(encoded), message.Type)
		}
		chunk, err := relay.DecodePayload[relay.StreamChunkPayload](message)
		if err != nil {
			t.Fatal(err)
		}
		if chunk.Sequence != uint64(sequence) || chunk.Final != (sequence == len(messages)-1) {
			t.Fatalf("chunk %d metadata = %#v", sequence, chunk)
		}
		rebuilt = append(rebuilt, chunk.Data...)
	}
	if !bytes.Equal(rebuilt, large) {
		t.Fatal("chunking truncated or reordered the result")
	}
	if _, err := resultMessages("request-too-large", large, 300, len(large)); !errors.Is(err, ErrResultTooLarge) {
		t.Fatalf("oversized result error = %v", err)
	}
}

func TestResultMessageIDsStayWithinCrossLanguageLimitForMaximumRequestID(t *testing.T) {
	t.Parallel()
	requestID := strings.Repeat("r", 256)
	messages, err := resultMessages(requestID, []byte(`{"ok":true}`), 1024, 4096)
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range messages {
		if len(message.MessageID) > 256 {
			t.Fatalf("message ID length = %d, want <= 256", len(message.MessageID))
		}
	}
}

func TestRequestRegistryCancelsExactRequestAndRejectsDuplicates(t *testing.T) {
	t.Parallel()
	registry := newRequestRegistry()
	ctx, cancel := context.WithCancel(context.Background())
	if !registry.Add("request-1", cancel) {
		t.Fatal("first request registration failed")
	}
	if registry.Add("request-1", func() {}) {
		t.Fatal("duplicate request registration succeeded")
	}
	if !registry.Cancel("request-1") {
		t.Fatal("known request was not cancelled")
	}
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("registered request context was not cancelled")
	}
	if registry.Cancel("request-1") {
		t.Fatal("cancelled request remained registered")
	}
}

func TestClientAuthenticatesRefreshesDispatchesHeartbeatsAndStopsOnContext(t *testing.T) {
	configPath, cfg, values := connectedRelayFixture(t)
	now := time.Unix(1_700_000_000, 0)
	dispatcher := &recordingDispatcher{}
	adapter, err := NewAdapter(AdapterOptions{
		ConfigPath: configPath, Now: func() time.Time { return now },
		DispatcherFactory: func(config.Config, secrets.Values) Dispatcher { return dispatcher },
	})
	if err != nil {
		t.Fatal(err)
	}
	socket := newFakeSocket()
	var dialURL string
	client, err := NewClient(ClientOptions{
		ConfigPath: configPath, ExecutorVersion: "test-version", Adapter: adapter,
		HeartbeatInterval: 10 * time.Millisecond, Now: func() time.Time { return now },
		Dial: func(_ context.Context, endpoint string, _ *http.Client) (relaySocket, error) {
			dialURL = endpoint
			return socket, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- client.Run(ctx) }()

	completeFakeHandshake(t, socket, cfg, values, now)
	if want := "wss://dashboard.example.test/api/device/connect/" + url.PathEscape(cfg.UnifiedDashboard.DeviceID); dialURL != want {
		t.Fatalf("dial URL = %q, want %q", dialURL, want)
	}

	heartbeat := decodeEnvelopeForTest(t, nextFakeWrite(t, socket))
	if heartbeat.Type != relay.MessageTypeHeartbeat {
		t.Fatalf("heartbeat type = %s", heartbeat.Type)
	}
	grant := signGrantForTest(t, values, cfg, "access-1", "browser-1", now)
	request, err := relay.NewEnvelope(relay.MessageTypeRequest, "message-1", relay.RequestPayload{
		RequestID: "request-1", Method: "device_status",
		Arguments: callArgumentsForTest(t, grant, "access-1", "browser-1", map[string]any{"action": "summary"}),
	})
	if err != nil {
		t.Fatal(err)
	}
	socket.reads <- mustJSON(t, request)
	response := nextEnvelopeOfType(t, socket, relay.MessageTypeResponse)
	payload, err := relay.DecodePayload[relay.ResponsePayload](response)
	if err != nil || payload.RequestID != "request-1" || string(payload.Result) != `{"marker":"SENSITIVE_RESULT_MARKER","ok":true}` {
		t.Fatalf("response payload = %#v err=%v", payload, err)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("client did not stop on context cancellation")
	}
	select {
	case <-socket.closed:
	case <-time.After(time.Second):
		t.Fatal("client did not close the WebSocket on shutdown")
	}
}

func TestClientNegotiatesOneBeforeSendingDeviceAuthentication(t *testing.T) {
	configPath, _, _ := connectedRelayFixture(t)
	socket := newFakeSocket()
	client, err := NewClient(ClientOptions{
		ConfigPath: configPath, ExecutorVersion: "test-version", HeartbeatInterval: time.Hour,
		Dial: func(context.Context, string, *http.Client) (relaySocket, error) { return socket, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- client.Run(ctx) }()
	offer, err := relay.NewEnvelope(relay.MessageTypeVersionNegotiation, "negotiation-offer", relay.VersionNegotiationPayload{
		SupportedVersions: []uint16{1, 2},
	})
	if err != nil {
		t.Fatal(err)
	}
	socket.reads <- mustJSON(t, offer)
	response := decodeEnvelopeForTest(t, nextFakeWrite(t, socket))
	if response.Type != relay.MessageTypeVersionNegotiation || response.MessageID != offer.MessageID {
		t.Fatalf("version negotiation response = %#v", response)
	}
	payload, err := relay.DecodePayload[relay.VersionNegotiationPayload](response)
	if err != nil || len(payload.SupportedVersions) != 1 || payload.SupportedVersions[0] != relay.ProtocolVersion {
		t.Fatalf("version selection = %#v err=%v", payload, err)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("client did not stop")
	}
}

func TestClientTimesOutStalledHandshakeBeforeReconnectBackoff(t *testing.T) {
	configPath, _, _ := connectedRelayFixture(t)
	socket := newFakeSocket()
	delayObserved := make(chan time.Duration, 1)
	client, err := NewClient(ClientOptions{
		ConfigPath: configPath, ExecutorVersion: "test-version", HeartbeatInterval: time.Hour,
		HandshakeTimeout: 25 * time.Millisecond,
		Dial: func(context.Context, string, *http.Client) (relaySocket, error) {
			return socket, nil
		},
		Sleep: func(ctx context.Context, delay time.Duration) error {
			select {
			case delayObserved <- delay:
			default:
			}
			<-ctx.Done()
			return ctx.Err()
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- client.Run(ctx) }()
	select {
	case delay := <-delayObserved:
		if delay < 800*time.Millisecond || delay > 1200*time.Millisecond {
			cancel()
			t.Fatalf("first reconnect delay = %v, want approximately 1s", delay)
		}
	case <-time.After(time.Second):
		cancel()
		t.Fatal("stalled relay handshake did not time out")
	}
	select {
	case <-socket.closed:
	case <-time.After(time.Second):
		cancel()
		t.Fatal("timed-out relay handshake did not close its socket")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("client did not stop after stalled handshake test")
	}
}

func TestClientUsesRealTLSWebSocketTransportForHandshakeRefreshAndHeartbeat(t *testing.T) {
	configPath, cfg, values := connectedRelayFixture(t)
	now := time.Now().UTC().Truncate(time.Second)
	heartbeatReceived := make(chan struct{})
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		connection, err := websocket.Accept(writer, request, nil)
		if err != nil {
			return
		}
		defer connection.CloseNow()
		if err := connection.Write(request.Context(), websocket.MessageText, []byte(`{"version":1,"type":"version_negotiation","message_id":"tls-negotiation","payload":{"supported_versions":[1]}}`)); err != nil {
			return
		}
		if _, selection, err := connection.Read(request.Context()); err != nil {
			return
		} else if envelope, decodeErr := relay.DecodeEnvelope(selection); decodeErr != nil || envelope.Type != relay.MessageTypeVersionNegotiation || envelope.MessageID != "tls-negotiation" {
			return
		}
		nonce := "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
		challenge := []byte(`{"version":1,"type":"device_challenge","device_id":"` + cfg.UnifiedDashboard.DeviceID + `","nonce":"` + nonce + `","issued_at":` + strconv.FormatInt(now.Unix(), 10) + `}`)
		if err := connection.Write(request.Context(), websocket.MessageText, challenge); err != nil {
			return
		}
		if _, _, err := connection.Read(request.Context()); err != nil {
			return
		}
		if err := connection.Write(request.Context(), websocket.MessageText, []byte(`{"version":1,"type":"device_authenticated"}`)); err != nil {
			return
		}
		if _, _, err := connection.Read(request.Context()); err != nil {
			return
		}
		ack := []byte(`{"version":1,"type":"device_refreshed","generation":` + strconv.FormatUint(values.Generation, 10) + `}`)
		if err := connection.Write(request.Context(), websocket.MessageText, ack); err != nil {
			return
		}
		_, heartbeat, err := connection.Read(request.Context())
		if err == nil {
			envelope, decodeErr := relay.DecodeEnvelope(heartbeat)
			if decodeErr == nil && envelope.Type == relay.MessageTypeHeartbeat {
				close(heartbeatReceived)
			}
		}
	}))
	defer server.Close()
	cfg.UnifiedDashboard.URL = server.URL
	if err := config.Save(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	client, err := NewClient(ClientOptions{
		ConfigPath: configPath, ExecutorVersion: "test-version", HTTPClient: server.Client(),
		HeartbeatInterval: 5 * time.Millisecond, Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- client.Run(ctx) }()
	select {
	case <-heartbeatReceived:
	case <-time.After(3 * time.Second):
		cancel()
		t.Fatal("real WebSocket transport did not complete the relay handshake")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("real WebSocket client did not stop")
	}
}

func TestClientCancellationCancelsOnlyTheMatchingDispatch(t *testing.T) {
	configPath, cfg, values := connectedRelayFixture(t)
	now := time.Unix(1_700_000_000, 0)
	dispatcher := &cancellableDispatcher{started: make(chan struct{}), canceled: make(chan struct{})}
	adapter, err := NewAdapter(AdapterOptions{
		ConfigPath: configPath, Now: func() time.Time { return now },
		DispatcherFactory: func(config.Config, secrets.Values) Dispatcher { return dispatcher },
	})
	if err != nil {
		t.Fatal(err)
	}
	socket := newFakeSocket()
	client, err := NewClient(ClientOptions{
		ConfigPath: configPath, ExecutorVersion: "test-version", Adapter: adapter,
		HeartbeatInterval: time.Hour, Now: func() time.Time { return now },
		Dial: func(context.Context, string, *http.Client) (relaySocket, error) { return socket, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- client.Run(ctx) }()
	completeFakeHandshake(t, socket, cfg, values, now)

	grant := signGrantForTest(t, values, cfg, "access-1", "browser-1", now)
	request, err := relay.NewEnvelope(relay.MessageTypeRequest, "message-cancel", relay.RequestPayload{
		RequestID: "request-cancel", Method: "device_status",
		Arguments: callArgumentsForTest(t, grant, "access-1", "browser-1", map[string]any{"action": "summary"}),
	})
	if err != nil {
		t.Fatal(err)
	}
	socket.reads <- mustJSON(t, request)
	select {
	case <-dispatcher.started:
	case <-time.After(time.Second):
		t.Fatal("request did not reach dispatcher")
	}
	cancellation, _ := relay.NewEnvelope(relay.MessageTypeCancellation, "cancel-message", relay.CancellationPayload{RequestID: "request-cancel"})
	socket.reads <- mustJSON(t, cancellation)
	select {
	case <-dispatcher.canceled:
	case <-time.After(time.Second):
		t.Fatal("cancellation did not reach matching request context")
	}
	response := nextEnvelopeOfType(t, socket, relay.MessageTypeResponse)
	payload, err := relay.DecodePayload[relay.ResponsePayload](response)
	if err != nil || payload.Failure == nil || payload.Failure.Code != "cancelled" {
		t.Fatalf("cancel response = %#v err=%v", payload, err)
	}
}

func TestClientRefreshesBeforeEncryptedRotateOrKillResponseAndKillDisconnects(t *testing.T) {
	for _, method := range []string{"control.rotate", "control.kill"} {
		t.Run(method, func(t *testing.T) {
			configPath, cfg, values := connectedRelayFixture(t)
			now := time.Unix(1_700_000_000, 0)
			lifecycle := &blockingRotatingLifecycle{
				inner: &rotatingLifecycle{stateDir: cfg.StateDir}, started: make(chan struct{}), release: make(chan struct{}),
			}
			adapter, err := NewAdapter(AdapterOptions{
				ConfigPath: configPath, Now: func() time.Time { return now },
				LifecycleFactory: func(string) (Lifecycle, error) { return lifecycle, nil },
			})
			if err != nil {
				t.Fatal(err)
			}
			socket := newFakeSocket()
			client, err := NewClient(ClientOptions{
				ConfigPath: configPath, ExecutorVersion: "test-version", Adapter: adapter,
				HeartbeatInterval: 5 * time.Millisecond, Now: func() time.Time { return now },
				Dial: func(context.Context, string, *http.Client) (relaySocket, error) { return socket, nil },
			})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			go func() { _ = client.Run(ctx) }()
			completeFakeHandshake(t, socket, cfg, values, now)

			browser, err := relay.GenerateDeviceIdentity()
			if err != nil {
				t.Fatal(err)
			}
			grant := signGrantForTest(t, values, cfg, "access-1", "browser-1", now)
			request, err := relay.NewEnvelope(relay.MessageTypeRequest, "message-lifecycle", relay.RequestPayload{
				RequestID: "request-lifecycle", Method: method,
				Arguments: callArgumentsForTest(t, grant, "access-1", "browser-1", map[string]any{"response_public_key": browser.PublicJWK()}),
			})
			if err != nil {
				t.Fatal(err)
			}
			socket.reads <- mustJSON(t, request)
			select {
			case <-lifecycle.started:
			case <-time.After(time.Second):
				t.Fatal("lifecycle action did not start")
			}
			select {
			case unexpected := <-socket.writes:
				t.Fatalf("relay wrote during lifecycle transition: %s", unexpected)
			case <-time.After(20 * time.Millisecond):
			}
			close(lifecycle.release)
			refreshWire := nextFakeWrite(t, socket)
			var refreshMessage map[string]any
			if err := json.Unmarshal(refreshWire, &refreshMessage); err != nil || refreshMessage["type"] != "device_refresh" || refreshMessage["generation"] != float64(values.Generation+1) {
				t.Fatalf("first lifecycle write was not generation refresh: %s err=%v", refreshWire, err)
			}
			socket.reads <- []byte(`{"version":1,"type":"device_refreshed","generation":2}`)
			response := nextEnvelopeOfType(t, socket, relay.MessageTypeResponse)
			payload, err := relay.DecodePayload[relay.ResponsePayload](response)
			if err != nil || bytes.Contains(payload.Result, []byte("recovery_key")) || !bytes.Contains(payload.Result, []byte("ciphertext")) {
				t.Fatalf("lifecycle response = %s err=%v", payload.Result, err)
			}
			if method == "control.kill" {
				select {
				case <-socket.closed:
				case <-time.After(time.Second):
					t.Fatal("Kill response was not followed by relay disconnect")
				}
			} else {
				select {
				case <-socket.closed:
					t.Fatal("Rotate disconnected the healthy relay")
				case <-time.After(20 * time.Millisecond):
				}
			}
		})
	}
}

func TestPreparedRemoteKillStopsAgentOnlyAfterEncryptedResponseAndSocketClose(t *testing.T) {
	configPath, cfg, values := connectedRelayFixture(t)
	now := time.Unix(1_700_000_000, 0)
	socket := newFakeSocket()
	type finalizerObservation struct {
		responseQueued bool
		socketClosed   bool
	}
	finalized := make(chan finalizerObservation, 1)
	lifecycle := &preparedRemoteKillLifecycle{
		inner: &rotatingLifecycle{stateDir: cfg.StateDir},
		finalizer: func(context.Context) error {
			closed := false
			select {
			case <-socket.closed:
				closed = true
			default:
			}
			finalized <- finalizerObservation{
				responseQueued: len(socket.writes) > 0,
				socketClosed:   closed,
			}
			return nil
		},
	}
	adapter, err := NewAdapter(AdapterOptions{
		ConfigPath: configPath,
		Now:        func() time.Time { return now },
		LifecycleFactory: func(string) (Lifecycle, error) {
			return lifecycle, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	client, err := NewClient(ClientOptions{
		ConfigPath: configPath, ExecutorVersion: "test-version", Adapter: adapter,
		HeartbeatInterval: time.Hour, Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	connection := newConnection(client, socket, cfg)
	browser, err := relay.GenerateDeviceIdentity()
	if err != nil {
		t.Fatal(err)
	}
	grant := signGrantForTest(t, values, cfg, "access-1", "browser-1", now)
	arguments := callArgumentsForTest(t, grant, "access-1", "browser-1", map[string]any{
		"response_public_key": browser.PublicJWK(),
	})

	connection.startRequest(context.Background(), "prepared-remote-kill", "control.kill", arguments, nil)
	refreshWire := nextFakeWrite(t, socket)
	var refresh signedDeviceRefresh
	if err := json.Unmarshal(refreshWire, &refresh); err != nil || refresh.Generation != values.Generation+1 {
		t.Fatalf("remote Kill refresh = %s err=%v", refreshWire, err)
	}
	ack := []byte(`{"version":1,"type":"device_refreshed","generation":2}`)
	if err := connection.handleMessage(context.Background(), ack); err != nil {
		t.Fatalf("remote Kill refresh acknowledgement: %v", err)
	}
	select {
	case observed := <-finalized:
		if !observed.responseQueued || !observed.socketClosed {
			t.Fatalf("remote Kill finalizer ordering = %#v", observed)
		}
	case <-time.After(time.Second):
		t.Fatal("remote Kill finalizer did not run")
	}
	response := nextEnvelopeOfType(t, socket, relay.MessageTypeResponse)
	payload, err := relay.DecodePayload[relay.ResponsePayload](response)
	if err != nil || payload.Failure != nil || bytes.Contains(payload.Result, []byte("recovery_key")) ||
		!bytes.Contains(payload.Result, []byte("ciphertext")) {
		t.Fatalf("prepared remote Kill response = %#v err=%v", payload, err)
	}
	waitForRequests(t, connection)
	if lifecycle.prepares != 1 || lifecycle.directKills != 0 {
		t.Fatalf("prepared remote Kill path = prepares:%d direct:%d", lifecycle.prepares, lifecycle.directKills)
	}
}

func TestLifecycleRefreshTimeoutStillReturnsEncryptedRecoveryResultAndReconnects(t *testing.T) {
	configPath, cfg, values := connectedRelayFixture(t)
	now := time.Unix(1_700_000_000, 0)
	adapter, err := NewAdapter(AdapterOptions{
		ConfigPath: configPath,
		Now:        func() time.Time { return now },
		LifecycleFactory: func(string) (Lifecycle, error) {
			return &rotatingLifecycle{stateDir: cfg.StateDir}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	client, err := NewClient(ClientOptions{
		ConfigPath: configPath, ExecutorVersion: "test-version", Adapter: adapter,
		HeartbeatInterval: time.Hour, RefreshTimeout: 25 * time.Millisecond, Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	socket := newFakeSocket()
	connection := newConnection(client, socket, cfg)
	browser, err := relay.GenerateDeviceIdentity()
	if err != nil {
		t.Fatal(err)
	}
	grant := signGrantForTest(t, values, cfg, "access-1", "browser-1", now)
	arguments := callArgumentsForTest(t, grant, "access-1", "browser-1", map[string]any{
		"response_public_key": browser.PublicJWK(),
	})

	connection.startRequest(context.Background(), "request-refresh-timeout", "control.rotate", arguments, nil)
	refreshWire := nextFakeWrite(t, socket)
	var refresh signedDeviceRefresh
	if err := json.Unmarshal(refreshWire, &refresh); err != nil || refresh.Generation != values.Generation+1 {
		t.Fatalf("lifecycle refresh = %s err=%v", refreshWire, err)
	}
	response := nextEnvelopeOfType(t, socket, relay.MessageTypeResponse)
	payload, err := relay.DecodePayload[relay.ResponsePayload](response)
	if err != nil || payload.Failure != nil || bytes.Contains(payload.Result, []byte("recovery_key")) ||
		!bytes.Contains(payload.Result, []byte("ciphertext")) {
		t.Fatalf("refresh-timeout lifecycle response = %#v err=%v", payload, err)
	}
	select {
	case <-socket.closed:
	case <-time.After(time.Second):
		t.Fatal("refresh-timeout lifecycle response did not force a clean reconnect")
	}
	waitForRequests(t, connection)
}

func TestRequestCancellationReleasesLifecycleRefreshAndRequestState(t *testing.T) {
	configPath, cfg, values := connectedRelayFixture(t)
	now := time.Unix(1_700_000_000, 0)
	adapter, err := NewAdapter(AdapterOptions{
		ConfigPath: configPath,
		Now:        func() time.Time { return now },
		LifecycleFactory: func(string) (Lifecycle, error) {
			return &rotatingLifecycle{stateDir: cfg.StateDir}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	client, err := NewClient(ClientOptions{
		ConfigPath: configPath, ExecutorVersion: "test-version", Adapter: adapter,
		HeartbeatInterval: time.Hour, Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	socket := newFakeSocket()
	connection := newConnection(client, socket, cfg)
	browser, err := relay.GenerateDeviceIdentity()
	if err != nil {
		t.Fatal(err)
	}
	grant := signGrantForTest(t, values, cfg, "access-1", "browser-1", now)
	arguments := callArgumentsForTest(t, grant, "access-1", "browser-1", map[string]any{
		"response_public_key": browser.PublicJWK(),
	})
	connection.startRequest(context.Background(), "request-cancel-refresh", "control.rotate", arguments, nil)
	refreshWire := nextFakeWrite(t, socket)
	var refresh signedDeviceRefresh
	if err := json.Unmarshal(refreshWire, &refresh); err != nil || refresh.Generation != values.Generation+1 {
		t.Fatalf("lifecycle refresh = %s err=%v", refreshWire, err)
	}
	cancellation, err := relay.NewEnvelope(
		relay.MessageTypeCancellation,
		"cancel-lifecycle-refresh",
		relay.CancellationPayload{RequestID: "request-cancel-refresh"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := connection.handleMessage(context.Background(), mustJSON(t, cancellation)); err != nil {
		t.Fatalf("handle cancellation: %v", err)
	}
	response := nextEnvelopeOfType(t, socket, relay.MessageTypeResponse)
	payload, err := relay.DecodePayload[relay.ResponsePayload](response)
	if err != nil || payload.Failure == nil || payload.Failure.Code != "cancelled" {
		t.Fatalf("cancelled lifecycle response = %#v err=%v", payload, err)
	}
	select {
	case <-socket.closed:
	case <-time.After(time.Second):
		t.Fatal("cancelled lifecycle refresh left a stale relay connection open")
	}
	waitForRequests(t, connection)
	if connection.lifecycleInFlight.Load() != 0 {
		t.Fatalf("lifecycle in-flight count = %d, want 0", connection.lifecycleInFlight.Load())
	}
	if connection.requests.Cancel("request-cancel-refresh") {
		t.Fatal("cancelled lifecycle request remained registered")
	}
}

func TestLateRefreshAcknowledgementCannotSatisfyUnrelatedRefresh(t *testing.T) {
	configPath, cfg, _ := connectedRelayFixture(t)
	client, err := NewClient(ClientOptions{ConfigPath: configPath, ExecutorVersion: "test-version"})
	if err != nil {
		t.Fatal(err)
	}
	socket := newFakeSocket()
	connection := newConnection(client, socket, cfg)
	firstValues, err := secrets.Rotate(cfg.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	firstCtx, cancelFirst := context.WithCancel(context.Background())
	firstResult := make(chan error, 1)
	go func() { firstResult <- connection.refresh(firstCtx, firstValues.Generation) }()
	_ = nextFakeWrite(t, socket)
	cancelFirst()
	select {
	case err := <-firstResult:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled refresh error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled refresh did not return")
	}

	secondValues, err := secrets.Rotate(cfg.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	secondResult := make(chan error, 1)
	go func() { secondResult <- connection.refresh(context.Background(), secondValues.Generation) }()
	_ = nextFakeWrite(t, socket)
	lateAck := []byte(`{"version":1,"type":"device_refreshed","generation":` + strconv.FormatUint(firstValues.Generation, 10) + `}`)
	if err := connection.handleMessage(context.Background(), lateAck); err != nil {
		t.Fatalf("late acknowledgement: %v", err)
	}
	select {
	case err := <-secondResult:
		t.Fatalf("late acknowledgement completed unrelated refresh: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	currentAck := []byte(`{"version":1,"type":"device_refreshed","generation":` + strconv.FormatUint(secondValues.Generation, 10) + `}`)
	if err := connection.handleMessage(context.Background(), currentAck); err != nil {
		t.Fatalf("current acknowledgement: %v", err)
	}
	select {
	case err := <-secondResult:
		if err != nil {
			t.Fatalf("current acknowledgement result: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("current acknowledgement did not release refresh")
	}
	if err := connection.handleMessage(context.Background(), lateAck); err != nil {
		t.Fatalf("late acknowledgement after refresh completion: %v", err)
	}
}

func TestRefreshTimesOutWithoutAcknowledgement(t *testing.T) {
	configPath, cfg, values := connectedRelayFixture(t)
	client, err := NewClient(ClientOptions{
		ConfigPath: configPath, ExecutorVersion: "test-version", RefreshTimeout: 25 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	socket := newFakeSocket()
	connection := newConnection(client, socket, cfg)
	result := make(chan error, 1)
	go func() { result <- connection.refresh(context.Background(), values.Generation) }()
	_ = nextFakeWrite(t, socket)

	select {
	case err := <-result:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("refresh timeout error = %v, want deadline exceeded", err)
		}
	case <-time.After(time.Second):
		t.Fatal("refresh without acknowledgement did not time out")
	}
	connection.refreshWaitMu.Lock()
	waiter := connection.refreshWait
	connection.refreshWaitMu.Unlock()
	if waiter != nil {
		t.Fatal("timed-out refresh waiter remained registered")
	}
}

func TestConnectionRejectsStaleDashboardConfiguration(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*config.Config)
	}{
		{name: "unenrolled", mutate: func(cfg *config.Config) { cfg.UnifiedDashboard.Enrolled = false }},
		{name: "dashboard origin", mutate: func(cfg *config.Config) { cfg.UnifiedDashboard.URL = "https://replacement.example.test" }},
		{name: "device identity", mutate: func(cfg *config.Config) { cfg.UnifiedDashboard.DeviceID = "replacement-device" }},
		{name: "state directory", mutate: func(cfg *config.Config) { cfg.StateDir = t.TempDir() }},
		{name: "public hostname", mutate: func(cfg *config.Config) { cfg.Domain = "replacement.example.test" }},
		{name: "broker endpoint", mutate: func(cfg *config.Config) { cfg.BrokerEndpoint += ".replacement" }},
		{name: "desktop endpoint", mutate: func(cfg *config.Config) { cfg.DesktopEndpoint += ".replacement" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			configPath, cfg, _ := connectedRelayFixture(t)
			client, err := NewClient(ClientOptions{ConfigPath: configPath, ExecutorVersion: "test-version"})
			if err != nil {
				t.Fatal(err)
			}
			connection := newConnection(client, newFakeSocket(), cfg)
			if _, err := connection.loadMatchingConfig(); err != nil {
				t.Fatalf("unchanged connection configuration rejected: %v", err)
			}
			test.mutate(&cfg)
			if err := config.Save(configPath, cfg); err != nil {
				t.Fatal(err)
			}
			if _, err := connection.loadMatchingConfig(); err == nil {
				t.Fatal("stale relay connection accepted changed configuration")
			}
		})
	}
}

func TestClientDoesNotReconnectWhileDisabled(t *testing.T) {
	configPath, cfg, _ := connectedRelayFixture(t)
	if err := os.WriteFile(filepath.Join(cfg.StateDir, "disabled"), []byte("quiesced\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var dials atomic.Int32
	client, err := NewClient(ClientOptions{
		ConfigPath: configPath, ExecutorVersion: "test-version", HeartbeatInterval: time.Hour,
		Dial: func(context.Context, string, *http.Client) (relaySocket, error) {
			dials.Add(1)
			return nil, errors.New("unexpected dial")
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := client.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if dials.Load() != 0 {
		t.Fatalf("disabled client dialed %d times", dials.Load())
	}
}

func TestClientWaitsForEnrollmentAndConnectsWithoutServiceRestart(t *testing.T) {
	configPath, cfg, _ := relayFixture(t)
	waitingForEnrollment := make(chan struct{})
	releaseEnrollmentWait := make(chan struct{})
	dialed := make(chan string, 1)
	var waitCount atomic.Int32
	client, err := NewClient(ClientOptions{
		ConfigPath: configPath, ExecutorVersion: "test-version", HeartbeatInterval: time.Hour,
		Dial: func(_ context.Context, endpoint string, _ *http.Client) (relaySocket, error) {
			select {
			case dialed <- endpoint:
			default:
			}
			return nil, errors.New("test relay unavailable")
		},
		Sleep: func(ctx context.Context, _ time.Duration) error {
			if waitCount.Add(1) == 1 {
				close(waitingForEnrollment)
				select {
				case <-releaseEnrollmentWait:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			<-ctx.Done()
			return ctx.Err()
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- client.Run(ctx) }()
	select {
	case <-waitingForEnrollment:
	case err := <-done:
		cancel()
		t.Fatalf("client stopped before enrollment: %v", err)
	case <-time.After(time.Second):
		cancel()
		t.Fatal("client did not wait for enrollment")
	}
	cfg.UnifiedDashboard.URL = "https://dashboard.example.test"
	cfg.UnifiedDashboard.Enrolled = true
	if err := config.Save(configPath, cfg); err != nil {
		cancel()
		t.Fatal(err)
	}
	close(releaseEnrollmentWait)
	select {
	case endpoint := <-dialed:
		if endpoint != "wss://dashboard.example.test/api/device/connect/"+cfg.UnifiedDashboard.DeviceID {
			t.Fatalf("relay endpoint = %q", endpoint)
		}
	case <-time.After(time.Second):
		cancel()
		t.Fatal("client did not connect after enrollment")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("client did not stop")
	}
}

func TestClientRejectsProtocolMismatchBeforeSendingDeviceProof(t *testing.T) {
	for name, versions := range map[string][]uint16{
		"no overlap": {2}, "empty": {}, "duplicate": {1, 1}, "zero": {0, 1},
	} {
		t.Run(name, func(t *testing.T) {
			configPath, _, _ := connectedRelayFixture(t)
			socket := newFakeSocket()
			client, err := NewClient(ClientOptions{
				ConfigPath: configPath, ExecutorVersion: "test-version", HeartbeatInterval: time.Hour,
				Dial: func(context.Context, string, *http.Client) (relaySocket, error) { return socket, nil },
			})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			go func() { _ = client.Run(ctx) }()
			offer, err := relay.NewEnvelope(relay.MessageTypeVersionNegotiation, "invalid-offer", relay.VersionNegotiationPayload{SupportedVersions: versions})
			if err != nil {
				t.Fatal(err)
			}
			socket.reads <- mustJSON(t, offer)
			select {
			case message := <-socket.writes:
				t.Fatalf("invalid negotiation received a device proof: %s", message)
			case <-socket.closed:
			case <-time.After(time.Second):
				t.Fatal("invalid negotiation did not close the relay")
			}
		})
	}
}

func TestClientRejectsVersionNegotiationReplayBeforeChallenge(t *testing.T) {
	configPath, _, _ := connectedRelayFixture(t)
	socket := newFakeSocket()
	client, err := NewClient(ClientOptions{
		ConfigPath: configPath, ExecutorVersion: "test-version", HeartbeatInterval: time.Hour,
		Dial: func(context.Context, string, *http.Client) (relaySocket, error) { return socket, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = client.Run(ctx) }()
	offer, err := relay.NewEnvelope(relay.MessageTypeVersionNegotiation, "replayed-offer", relay.VersionNegotiationPayload{SupportedVersions: []uint16{1}})
	if err != nil {
		t.Fatal(err)
	}
	socket.reads <- mustJSON(t, offer)
	_ = nextFakeWrite(t, socket)
	socket.reads <- mustJSON(t, offer)
	select {
	case unexpected := <-socket.writes:
		t.Fatalf("negotiation replay received another response: %s", unexpected)
	case <-socket.closed:
	case <-time.After(time.Second):
		t.Fatal("negotiation replay did not close the relay")
	}
}

func TestConnectedClientClosesWhenLocalDisabledMarkerAppears(t *testing.T) {
	configPath, cfg, values := connectedRelayFixture(t)
	now := time.Unix(1_700_000_000, 0)
	socket := newFakeSocket()
	client, err := NewClient(ClientOptions{
		ConfigPath: configPath, ExecutorVersion: "test-version", HeartbeatInterval: 5 * time.Millisecond,
		Now:  func() time.Time { return now },
		Dial: func(context.Context, string, *http.Client) (relaySocket, error) { return socket, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = client.Run(ctx) }()
	completeFakeHandshake(t, socket, cfg, values, now)
	if err := os.WriteFile(filepath.Join(cfg.StateDir, "disabled"), []byte("quiesced\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case <-socket.closed:
	case <-time.After(time.Second):
		t.Fatal("connected relay stayed open after Executor was disabled")
	}
}

type fakeSocket struct {
	reads  chan []byte
	writes chan []byte
	closed chan struct{}
	once   sync.Once
}

func newFakeSocket() *fakeSocket {
	return &fakeSocket{reads: make(chan []byte, 16), writes: make(chan []byte, 32), closed: make(chan struct{})}
}

func (s *fakeSocket) Read(ctx context.Context) ([]byte, error) {
	select {
	case message := <-s.reads:
		return message, nil
	case <-s.closed:
		return nil, errors.New("closed")
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (s *fakeSocket) Write(ctx context.Context, message []byte) error {
	select {
	case s.writes <- append([]byte(nil), message...):
		return nil
	case <-s.closed:
		return errors.New("closed")
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *fakeSocket) Close(int, string) error {
	s.once.Do(func() { close(s.closed) })
	return nil
}

func (s *fakeSocket) SetReadLimit(int64) {}

func connectedRelayFixture(t *testing.T) (string, config.Config, secrets.Values) {
	t.Helper()
	configPath, cfg, values := relayFixture(t)
	cfg.UnifiedDashboard.URL = "https://dashboard.example.test"
	cfg.UnifiedDashboard.Enrolled = true
	if err := config.Save(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	return configPath, cfg, values
}

func completeFakeHandshake(t *testing.T, socket *fakeSocket, cfg config.Config, values secrets.Values, now time.Time) {
	t.Helper()
	offer, err := relay.NewEnvelope(relay.MessageTypeVersionNegotiation, "negotiation-fixture", relay.VersionNegotiationPayload{
		SupportedVersions: []uint16{relay.ProtocolVersion},
	})
	if err != nil {
		t.Fatal(err)
	}
	socket.reads <- mustJSON(t, offer)
	selection := decodeEnvelopeForTest(t, nextFakeWrite(t, socket))
	selected, err := relay.DecodePayload[relay.VersionNegotiationPayload](selection)
	if err != nil || selection.Type != relay.MessageTypeVersionNegotiation || selection.MessageID != offer.MessageID ||
		len(selected.SupportedVersions) != 1 || selected.SupportedVersions[0] != relay.ProtocolVersion {
		t.Fatalf("version selection = %#v payload=%#v err=%v", selection, selected, err)
	}
	nonce := "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	socket.reads <- []byte(`{"version":1,"type":"device_challenge","device_id":"` + cfg.UnifiedDashboard.DeviceID + `","nonce":"` + nonce + `","issued_at":` + strconv.FormatInt(now.Unix(), 10) + `}`)
	var challengeResponse struct {
		Version   uint16 `json:"version"`
		Type      string `json:"type"`
		Nonce     string `json:"nonce"`
		IssuedAt  int64  `json:"issued_at"`
		Signature string `json:"signature"`
	}
	if err := json.Unmarshal(nextFakeWrite(t, socket), &challengeResponse); err != nil {
		t.Fatal(err)
	}
	identity, err := relay.ParseDeviceIdentity(values.RelayPrivateJWK)
	if err != nil {
		t.Fatal(err)
	}
	if challengeResponse.Version != 1 || challengeResponse.Type != "device_challenge_response" || challengeResponse.Nonce != nonce ||
		challengeResponse.IssuedAt != now.Unix() || !relay.VerifyDeviceChallenge(identity.PublicJWK(), cfg.UnifiedDashboard.DeviceID, nonce, now.Unix(), challengeResponse.Signature) {
		t.Fatalf("challenge response = %#v", challengeResponse)
	}
	socket.reads <- []byte(`{"version":1,"type":"device_authenticated"}`)
	var refresh struct {
		Version uint16 `json:"version"`
		Type    string `json:"type"`
		relay.DeviceRefresh
		Signature string `json:"signature"`
	}
	if err := json.Unmarshal(nextFakeWrite(t, socket), &refresh); err != nil {
		t.Fatal(err)
	}
	if refresh.Version != 1 || refresh.Type != "device_refresh" || refresh.DeviceID != cfg.UnifiedDashboard.DeviceID ||
		refresh.Generation != values.Generation || !relay.VerifyDeviceRefresh(identity.PublicJWK(), refresh.DeviceRefresh, refresh.Signature) {
		t.Fatalf("device refresh = %#v", refresh)
	}
	socket.reads <- []byte(`{"version":1,"type":"device_refreshed","generation":` + strconv.FormatUint(values.Generation, 10) + `}`)
}

func nextFakeWrite(t *testing.T, socket *fakeSocket) []byte {
	t.Helper()
	select {
	case message := <-socket.writes:
		return message
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for WebSocket write")
		return nil
	}
}

func decodeEnvelopeForTest(t *testing.T, message []byte) relay.Envelope {
	t.Helper()
	envelope, err := relay.DecodeEnvelope(message)
	if err != nil {
		t.Fatalf("DecodeEnvelope(%s): %v", message, err)
	}
	return envelope
}

func nextEnvelopeOfType(t *testing.T, socket *fakeSocket, messageType relay.MessageType) relay.Envelope {
	t.Helper()
	for attempts := 0; attempts < 20; attempts++ {
		envelope := decodeEnvelopeForTest(t, nextFakeWrite(t, socket))
		if envelope.Type == messageType {
			return envelope
		}
	}
	t.Fatalf("no %s envelope received", messageType)
	return relay.Envelope{}
}

func waitForRequests(t *testing.T, connection *connection) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		connection.requestWG.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("relay request did not finish")
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}
