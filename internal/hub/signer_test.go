package hub

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jamie950315/executor/internal/mcp"
	"github.com/jamie950315/executor/internal/relay"
)

type signedTransport struct {
	calls int
	proof relay.SignedHubRequest
}

func (s *signedTransport) Devices(context.Context) ([]Device, error) {
	return []Device{{ID: "mac", Online: true, Authorized: true}}, nil
}
func (s *signedTransport) Submit(_ context.Context, id string, p relay.SignedHubRequest) (any, error) {
	s.calls++
	s.proof = p
	return map[string]any{"ok": true}, nil
}

func TestSignedRelayValidatesGrantBeforeSending(t *testing.T) {
	device, _ := relay.GenerateDeviceIdentity()
	hub, _ := relay.GenerateDeviceIdentity()
	now := time.Unix(1700000000, 0)
	keyID, _ := relay.HubKeyID(hub.PublicJWK())
	grant, err := relay.SignHubGrant(device, relay.HubGrantClaims{Version: 1, DeviceID: "mac", HubID: "pi5", HubKeyID: keyID, Generation: 1, DelegationVersion: 2, IssuedAt: now.Unix(), ExpiresAt: now.Add(time.Hour).Unix(), JTI: "grant"})
	if err != nil {
		t.Fatal(err)
	}
	transport := &signedTransport{}
	denied := false
	source := func(context.Context, string) (Delegation, error) {
		if denied {
			return Delegation{}, errors.New("revoked")
		}
		return Delegation{DeviceKey: device.PublicJWK(), Grant: grant, Generation: 1, Version: 2}, nil
	}
	client, err := NewSignedRelay(transport, "pi5", hub, source, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	call := mcp.ToolCall{Name: "filesystem_write", SessionID: "caller", Arguments: map[string]any{"path": "fixture", "content": "ok"}}
	if _, err := client.Call(context.Background(), "mac", call); err != nil {
		t.Fatal(err)
	}
	if transport.calls != 1 || transport.proof.Request.CallerID != "caller" || transport.proof.Request.DeviceID != "mac" || transport.proof.Request.Method != "filesystem_write" {
		t.Fatal("bad signed routing")
	}
	// Verify actual proof without executing any tool.
	expected := relay.HubGrantExpectation{DeviceID: "mac", HubID: "pi5", HubKeyID: keyID, Generation: 1, DelegationVersion: 2, Now: now}
	replay := acceptOnce{}
	if err := relay.VerifyHubRequest(device.PublicJWK(), hub.PublicJWK(), expected, transport.proof, &replay); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Call(context.Background(), "other-device", call); err == nil || transport.calls != 1 {
		t.Fatal("wrong device grant sent")
	}
	denied = true
	listed, err := client.Devices(context.Background())
	if err != nil || len(listed) != 1 || listed[0].Authorized {
		t.Fatal("directory exposed stale authorization")
	}
	if _, err := client.Call(context.Background(), "mac", call); err == nil || transport.calls != 1 {
		t.Fatal("revoked grant sent")
	}
}

type acceptOnce struct{ used bool }

func (a *acceptOnce) Consume(string, time.Time) error {
	if a.used {
		return errors.New("replay")
	}
	a.used = true
	return nil
}
