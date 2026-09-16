package relay

import (
	"strings"
	"testing"
	"time"
)

func TestHubGrantIsDeviceAndHubBound(t *testing.T) {
	identity, err := GenerateDeviceIdentity()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1700000000, 0)
	claims := HubGrantClaims{Version: 1, DeviceID: "mac", HubID: "pi5", HubKeyID: strings.Repeat("a", 64), Generation: 2, DelegationVersion: 3, IssuedAt: now.Unix(), ExpiresAt: now.Add(time.Hour).Unix(), JTI: "test-grant"}
	token, err := SignHubGrant(identity, claims)
	if err != nil {
		t.Fatal(err)
	}
	expected := HubGrantExpectation{DeviceID: claims.DeviceID, HubID: claims.HubID, HubKeyID: claims.HubKeyID, Generation: 2, DelegationVersion: 3, Now: now}
	got, err := VerifyHubGrant(identity.PublicJWK(), token, expected)
	if err != nil || got != claims {
		t.Fatal("Hub grant round trip failed")
	}
	for _, change := range []func(*HubGrantExpectation){
		func(e *HubGrantExpectation) { e.DeviceID = "windows" },
		func(e *HubGrantExpectation) { e.HubID = "another-hub" },
		func(e *HubGrantExpectation) { e.HubKeyID = strings.Repeat("b", 64) },
		func(e *HubGrantExpectation) { e.Generation++ },
		func(e *HubGrantExpectation) { e.DelegationVersion++ },
		func(e *HubGrantExpectation) { e.Now = now.Add(2 * time.Hour) },
	} {
		e := expected
		change(&e)
		if _, err := VerifyHubGrant(identity.PublicJWK(), token, e); err == nil {
			t.Fatal("mismatched or expired grant accepted")
		}
	}
	browserToken, err := SignDeviceGrant(identity, grantClaimsFixture(now))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyHubGrant(identity.PublicJWK(), browserToken, expected); err == nil {
		t.Fatal("browser grant accepted as Hub grant")
	}
	if _, err := VerifyDeviceGrant(identity.PublicJWK(), token, GrantExpectation{DeviceID: "mac", AccessSubject: "owner", BrowserID: "pi5", Generation: 2, Now: now}); err == nil {
		t.Fatal("Hub grant accepted as browser grant")
	}
	other, err := GenerateDeviceIdentity()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyHubGrant(other.PublicJWK(), token, expected); err == nil {
		t.Fatal("wrong device signing key accepted")
	}
}

func TestHubGrantRejectsUnboundedOrIncompleteDelegation(t *testing.T) {
	identity, err := GenerateDeviceIdentity()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1700000000, 0)
	claims := HubGrantClaims{Version: 1, DeviceID: "mac", HubID: "pi5", HubKeyID: strings.Repeat("a", 64), Generation: 2, DelegationVersion: 3, IssuedAt: now.Unix(), ExpiresAt: now.Add(time.Hour).Unix(), JTI: "test-grant"}
	for _, change := range []func(*HubGrantClaims){
		func(c *HubGrantClaims) { c.HubID = "" }, func(c *HubGrantClaims) { c.HubKeyID = "not-a-key-id" },
		func(c *HubGrantClaims) { c.DelegationVersion = 0 }, func(c *HubGrantClaims) { c.ExpiresAt = now.Add(31 * 24 * time.Hour).Unix() },
	} {
		c := claims
		change(&c)
		if _, err := SignHubGrant(identity, c); err == nil {
			t.Fatal("invalid grant signed")
		}
	}
}
