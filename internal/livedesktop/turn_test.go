package livedesktop

import (
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"github.com/pion/logging"
	"github.com/pion/stun/v3"
	"github.com/pion/turn/v4"
	"github.com/pion/webrtc/v4"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestTURNRelayCarriesVideoWithTemporaryCredentials(t *testing.T) {
	listener, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	secret := strings.Repeat("test-only-", 4)
	logger := logging.NewDefaultLoggerFactory()
	logger.Writer = io.Discard
	server, err := turn.NewServer(turn.ServerConfig{Realm: "executor-test", LoggerFactory: logger,
		AuthHandler: func(username, realm string, _ net.Addr) ([]byte, bool) {
			mac := hmac.New(sha1.New, []byte(secret))
			mac.Write([]byte(username))
			return turn.GenerateAuthKey(username, realm, base64.StdEncoding.EncodeToString(mac.Sum(nil))), true
		},
		PacketConnConfigs: []turn.PacketConnConfig{{PacketConn: listener, RelayAddressGenerator: &turn.RelayAddressGeneratorStatic{RelayAddress: net.ParseIP("127.0.0.1"), Address: "127.0.0.1"}}},
	})
	if err != nil {
		listener.Close()
		t.Fatal(err)
	}
	defer server.Close()
	cfg := &TURNConfig{URLs: []string{"turn:" + listener.LocalAddr().String() + "?transport=udp"}, Secret: secret}
	m := NewManager(Config{Backend: &fakeBackend{events: make(chan InputEvent, 32)}, ICE: []string{}, TURN: cfg})
	defer m.Close()
	credentials, err := m.Connectivity(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	c := credentials.ICEServers[0]
	session, _, _, _, video := connectPeerWithConfig(t, m, webrtc.Configuration{ICETransportPolicy: webrtc.ICETransportPolicyRelay, ICEServers: []webrtc.ICEServer{{URLs: c.URLs, Username: c.Username, Credential: c.Credential}}})
	select {
	case <-video:
	case <-time.After(5 * time.Second):
		t.Fatal("relay did not deliver video")
	}
	if err := m.Stop("owner", session.SessionID); err != nil {
		t.Fatal(err)
	}
}

func TestDirectConnectionSurvivesUnreachableTURN(t *testing.T) {
	blackhole, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer blackhole.Close()
	m := NewManager(Config{Backend: &fakeBackend{events: make(chan InputEvent, 32)}, ICE: []string{}, TURN: &TURNConfig{URLs: []string{"turn:" + blackhole.LocalAddr().String() + "?transport=udp"}, Secret: strings.Repeat("test-only-", 4)}})
	defer m.Close()
	session, _, _, _, video := connectPeer(t, m)
	select {
	case <-video:
	case <-time.After(5 * time.Second):
		t.Fatal("usable direct path was lost because relay discovery failed")
	}
	if err := m.Stop("owner", session.SessionID); err != nil {
		t.Fatal(err)
	}
}

func TestTURNConnectivityIsFreshAndUnavailableAfterClose(t *testing.T) {
	secret := strings.Repeat("test-only-", 4)
	m := NewManager(Config{Backend: &fakeBackend{}, ICE: []string{}, TURN: &TURNConfig{URLs: []string{"turn:relay.example:5349"}, Secret: secret}})
	status, err := m.Status(context.Background())
	if err != nil || !status.RelayConfigured {
		t.Fatal("relay status missing")
	}
	data, _ := json.Marshal(status)
	if strings.Contains(string(data), secret) {
		t.Fatal("status exposed secret")
	}
	a, err := m.Connectivity(context.Background())
	if err != nil || len(a.ICEServers) != 1 {
		t.Fatal("missing credentials")
	}
	b, err := m.Connectivity(context.Background())
	if err != nil || a.ICEServers[0].Username == b.ICEServers[0].Username {
		t.Fatal("credentials reused")
	}
	m.Close()
	if _, err := m.Connectivity(context.Background()); err == nil {
		t.Fatal("closed helper issued credentials")
	}
}

func TestTURNCredentialsAreTemporaryAndNeverExposeSecret(t *testing.T) {
	c := TURNConfig{URLs: []string{"turn:relay.example:5349?transport=tcp"}, Secret: strings.Repeat("test-only-", 4)}
	now := time.Unix(1700000000, 0)
	a, err := c.credentials(now)
	if err != nil {
		t.Fatal(err)
	}
	b, err := c.credentials(now)
	if err != nil {
		t.Fatal(err)
	}
	if a.Username == b.Username || !strings.HasPrefix(a.Username, "1700003600:") {
		t.Fatal("credentials must be expiring and unique")
	}
	mac := hmac.New(sha1.New, []byte(c.Secret))
	mac.Write([]byte(a.Username))
	if a.Credential != base64.StdEncoding.EncodeToString(mac.Sum(nil)) {
		t.Fatal("incorrect TURN REST credential")
	}
	data, _ := json.Marshal(a)
	if strings.Contains(string(data), c.Secret) {
		t.Fatal("long-term secret exposed")
	}
	metadata, _ := json.Marshal(c)
	if strings.Contains(string(metadata), c.Secret) || strings.Contains(fmt.Sprint(c), c.Secret) {
		t.Fatal("configuration diagnostics expose the long-term secret")
	}
}

func TestTURNConfigFailsClosed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "live-turn.json")
	if c, err := LoadTURNConfig(path); err != nil || c != nil {
		t.Fatal("missing optional config must retain direct mode")
	}
	for _, data := range []string{`{}`, `{"urls":["https://relay.example"],"secret":"bad"}`, `{"urls":["turn:relay.example"],"secret":"long-enough-test-only-secret-value","unknown":true}`, `{} {}`} {
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadTURNConfig(path); err == nil {
			t.Fatal("invalid configured relay accepted")
		}
	}
	c := TURNConfig{URLs: []string{"turn:relay.example:5349?transport=udp"}, Secret: strings.Repeat("test-only-", 4)}
	data, _ := json.Marshal(map[string]any{"urls": c.URLs, "secret": c.Secret})
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadTURNConfig(path); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(path, 0644); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadTURNConfig(path); err == nil {
			t.Fatal("world-readable TURN secret accepted")
		}
		if err := os.Chmod(path, 0600); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(dir, "linked-turn.json")
		if err := os.Symlink(path, link); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadTURNConfig(link); err == nil {
			t.Fatal("symlinked TURN secret accepted")
		}
	}
}

// An explicit owner opt-in sends synthetic video only; no screen/input is used.
func TestTURNExternalOptIn(t *testing.T) {
	path := os.Getenv("EXECUTOR_TEST_TURN_CONFIG")
	if path == "" {
		t.Skip("requires an explicit private TURN config path")
	}
	cfg, err := LoadTURNConfig(path)
	if err != nil || cfg == nil {
		t.Fatal("private TURN test configuration unavailable")
	}
	for _, url := range cfg.URLs {
		t.Run("relay", func(t *testing.T) {
			m := NewManager(Config{Backend: &fakeBackend{events: make(chan InputEvent, 32)}, TURN: cfg})
			defer m.Close()
			credential, err := cfg.credentials(time.Now())
			if err != nil {
				t.Fatal("cannot issue test relay credential")
			}
			session, _, _, _, video := connectPeerWithConfig(t, m, webrtc.Configuration{ICETransportPolicy: webrtc.ICETransportPolicyRelay, ICEServers: []webrtc.ICEServer{{URLs: []string{url}, Username: credential.Username, Credential: credential.Credential}}})
			select {
			case <-video:
			case <-time.After(5 * time.Second):
				t.Fatal("external relay did not deliver synthetic video")
			}
			if err := m.Stop("owner", session.SessionID); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestTURNExternalAllocationBudgetOptIn(t *testing.T) {
	path := os.Getenv("EXECUTOR_TEST_TURN_CONFIG")
	if path == "" {
		t.Skip("requires private TURN configuration")
	}
	cfg, err := LoadTURNConfig(path)
	if err != nil || cfg == nil {
		t.Fatal("private TURN configuration unavailable")
	}
	u, err := stun.ParseURI(cfg.URLs[0])
	if err != nil {
		t.Fatal("invalid TURN endpoint")
	}
	address := net.JoinHostPort(u.Host, fmt.Sprint(u.Port))
	logger := logging.NewDefaultLoggerFactory()
	logger.Writer = io.Discard
	for i := 0; i < 3; i++ {
		transport, err := net.ListenPacket("udp4", "0.0.0.0:0")
		if err != nil {
			t.Fatal(err)
		}
		credential, err := cfg.credentials(time.Now())
		if err != nil {
			transport.Close()
			t.Fatal(err)
		}
		client, err := turn.NewClient(&turn.ClientConfig{Conn: transport, TURNServerAddr: address, STUNServerAddr: address, Username: credential.Username, Password: credential.Credential, LoggerFactory: logger, RTO: time.Second})
		if err != nil {
			transport.Close()
			t.Fatal("TURN client could not initialize")
		}
		t.Cleanup(func() { client.Close(); transport.Close() })
		if err = client.Listen(); err != nil {
			t.Fatal("TURN client could not listen")
		}
		allocation, err := client.Allocate()
		if err != nil {
			t.Fatalf("allocation %d rejected: %v", i+1, err)
		}
		t.Cleanup(func() { allocation.Close() })
	}
}

func TestTURNExternalRejectsExpiredAuthorizationOptIn(t *testing.T) {
	path := os.Getenv("EXECUTOR_TEST_TURN_CONFIG")
	if path == "" {
		t.Skip("requires private TURN configuration")
	}
	cfg, err := LoadTURNConfig(path)
	if err != nil || cfg == nil {
		t.Fatal("private TURN configuration unavailable")
	}
	u, err := stun.ParseURI(cfg.URLs[0])
	if err != nil {
		t.Fatal("invalid TURN endpoint")
	}
	address := net.JoinHostPort(u.Host, fmt.Sprint(u.Port))
	transport, err := net.ListenPacket("udp4", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	defer transport.Close()
	credential, err := cfg.credentials(time.Now().Add(-2 * time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	logger := logging.NewDefaultLoggerFactory()
	logger.Writer = io.Discard
	client, err := turn.NewClient(&turn.ClientConfig{Conn: transport, TURNServerAddr: address, STUNServerAddr: address, Username: credential.Username, Password: credential.Credential, LoggerFactory: logger, RTO: time.Second})
	if err != nil {
		t.Fatal("cannot create test TURN client")
	}
	defer client.Close()
	if err = client.Listen(); err != nil {
		t.Fatal("cannot start TURN client")
	}
	allocation, err := client.Allocate()
	if err == nil {
		allocation.Close()
		t.Fatal("expired authorization was accepted")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Fatalf("expected expired authorization rejection, got %v", err)
	}
}
