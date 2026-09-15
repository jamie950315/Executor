package relay

import (
	"errors"
	"testing"
	"time"
)

type proofReplay struct {
	seen map[string]bool
	fail bool
}

func (s *proofReplay) Consume(key string, _ time.Time) error {
	if s.fail || s.seen[key] {
		return errors.New("unavailable or replay")
	}
	s.seen[key] = true
	return nil
}

func TestHubRequestBindsBodyGrantDeviceAndCallerAndRejectsReplay(t *testing.T) {
	device, _ := GenerateDeviceIdentity()
	hub, _ := GenerateDeviceIdentity()
	now := time.Unix(1700000000, 0)
	keyID, err := HubKeyID(hub.PublicJWK())
	if err != nil {
		t.Fatal(err)
	}
	expected := HubGrantExpectation{DeviceID: "mac", HubID: "pi5", HubKeyID: keyID, Generation: 2, DelegationVersion: 1, Now: now}
	grant, err := SignHubGrant(device, HubGrantClaims{Version: 1, DeviceID: "mac", HubID: "pi5", HubKeyID: keyID, Generation: 2, DelegationVersion: 1, IssuedAt: now.Unix(), ExpiresAt: now.Add(time.Hour).Unix(), JTI: "delegation"})
	if err != nil {
		t.Fatal(err)
	}
	request := HubRequest{Version: 1, DeviceID: "mac", HubID: "pi5", CallerID: "conversation-1", RequestID: "request-1", Method: "filesystem_write", Input: []byte(`{"path":"fixture","content":"ok"}`), Grant: grant, IssuedAt: now.Unix(), ExpiresAt: now.Add(time.Minute).Unix(), Nonce: "nonce-unique-for-request"}
	signed, err := SignHubRequest(hub, request)
	if err != nil {
		t.Fatal(err)
	}
	replay := &proofReplay{seen: map[string]bool{}}
	if err := VerifyHubRequest(device.PublicJWK(), hub.PublicJWK(), expected, signed, replay); err != nil {
		t.Fatal(err)
	}
	if err := VerifyHubRequest(device.PublicJWK(), hub.PublicJWK(), expected, signed, replay); err == nil {
		t.Fatal("replay accepted")
	}
	tooFuture := request
	tooFuture.IssuedAt = now.Add(6 * time.Second).Unix()
	tooFuture.ExpiresAt = now.Add(66 * time.Second).Unix()
	futureProof, err := SignHubRequest(hub, tooFuture)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyHubRequest(device.PublicJWK(), hub.PublicJWK(), expected, futureProof, &proofReplay{seen: map[string]bool{}}); err == nil {
		t.Fatal("future proof accepted")
	}
	tooLong := request
	tooLong.ExpiresAt = now.Add(61 * time.Second).Unix()
	if _, err := SignHubRequest(hub, tooLong); err == nil {
		t.Fatal("overlong proof signed")
	}
	for _, change := range []func(*HubRequest){
		func(r *HubRequest) { r.DeviceID = "windows" }, func(r *HubRequest) { r.CallerID = "other" },
		func(r *HubRequest) { r.Method = "terminal" }, func(r *HubRequest) { r.Input = []byte(`{"content":"changed"}`) },
		func(r *HubRequest) { r.Grant += "x" }, func(r *HubRequest) { r.RequestID = "different" },
	} {
		bad := signed
		change(&bad.Request)
		if err := VerifyHubRequest(device.PublicJWK(), hub.PublicJWK(), expected, bad, &proofReplay{seen: map[string]bool{}}); err == nil {
			t.Fatal("tampered proof accepted")
		}
	}
	if err := VerifyHubRequest(device.PublicJWK(), hub.PublicJWK(), expected, signed, nil); err == nil {
		t.Fatal("missing replay storage accepted")
	}
	if err := VerifyHubRequest(device.PublicJWK(), hub.PublicJWK(), expected, signed, &proofReplay{fail: true}); err == nil {
		t.Fatal("failed replay storage accepted")
	}
	expected.Now = now.Add(2 * time.Minute)
	if err := VerifyHubRequest(device.PublicJWK(), hub.PublicJWK(), expected, signed, &proofReplay{seen: map[string]bool{}}); err == nil {
		t.Fatal("expired proof accepted")
	}
}
