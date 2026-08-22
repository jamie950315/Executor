package relay

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestDeviceGrantRoundTripUsesES256P1363Signature(t *testing.T) {
	t.Parallel()

	identity, err := GenerateDeviceIdentity()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_700_000_000, 0)
	claims := grantClaimsFixture(now)
	token, err := SignDeviceGrant(identity, claims)
	if err != nil {
		t.Fatalf("SignDeviceGrant: %v", err)
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("compact JWS has %d parts, want 3", len(parts))
	}
	headerJSON, err := base64.RawURLEncoding.Strict().DecodeString(parts[0])
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(headerJSON), `{"alg":"ES256","typ":"executor-device-grant+jwt","version":1}`; got != want {
		t.Fatalf("protected header = %s, want %s", got, want)
	}
	signature, err := base64.RawURLEncoding.Strict().DecodeString(parts[2])
	if err != nil {
		t.Fatal(err)
	}
	if len(signature) != 64 {
		t.Fatalf("ES256 signature length = %d, want 64-byte IEEE P1363", len(signature))
	}

	verified, err := VerifyDeviceGrant(identity.PublicJWK(), token, GrantExpectation{
		DeviceID:      claims.DeviceID,
		AccessSubject: claims.AccessSubject,
		BrowserID:     claims.BrowserID,
		Generation:    claims.Generation,
		Now:           now,
	})
	if err != nil {
		t.Fatalf("VerifyDeviceGrant: %v", err)
	}
	if verified != claims {
		t.Fatalf("verified claims = %#v, want %#v", verified, claims)
	}
}

func TestDeviceGrantRequiresAllVersionedClaimsAndThirtyDayMaximum(t *testing.T) {
	t.Parallel()

	identity, err := GenerateDeviceIdentity()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_700_000_000, 0)
	valid := grantClaimsFixture(now)
	tests := []GrantClaims{
		func() GrantClaims { value := valid; value.Version = 0; return value }(),
		func() GrantClaims { value := valid; value.Version = 2; return value }(),
		func() GrantClaims { value := valid; value.DeviceID = ""; return value }(),
		func() GrantClaims { value := valid; value.AccessSubject = ""; return value }(),
		func() GrantClaims { value := valid; value.BrowserID = ""; return value }(),
		func() GrantClaims { value := valid; value.Generation = 0; return value }(),
		func() GrantClaims { value := valid; value.IssuedAt = 0; return value }(),
		func() GrantClaims { value := valid; value.ExpiresAt = value.IssuedAt; return value }(),
		func() GrantClaims {
			value := valid
			value.ExpiresAt = value.IssuedAt + int64(MaxGrantLifetime/time.Second) + 1
			return value
		}(),
		func() GrantClaims { value := valid; value.JTI = ""; return value }(),
	}
	for _, claims := range tests {
		if _, err := SignDeviceGrant(identity, claims); !errors.Is(err, ErrInvalidDeviceGrant) {
			t.Fatalf("invalid claims %#v error = %v, want ErrInvalidDeviceGrant", claims, err)
		}
	}

	maximum := valid
	maximum.ExpiresAt = maximum.IssuedAt + int64(MaxGrantLifetime/time.Second)
	if _, err := SignDeviceGrant(identity, maximum); err != nil {
		t.Fatalf("exact 30-day grant rejected: %v", err)
	}
}

func TestDeviceGrantRejectsWrongBoundContextAndGeneration(t *testing.T) {
	t.Parallel()

	identity, err := GenerateDeviceIdentity()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_700_000_000, 0)
	claims := grantClaimsFixture(now)
	token, err := SignDeviceGrant(identity, claims)
	if err != nil {
		t.Fatal(err)
	}
	valid := GrantExpectation{
		DeviceID: claims.DeviceID, AccessSubject: claims.AccessSubject, BrowserID: claims.BrowserID,
		Generation: claims.Generation, Now: now,
	}
	tests := []GrantExpectation{
		func() GrantExpectation { value := valid; value.DeviceID = "other-device"; return value }(),
		func() GrantExpectation { value := valid; value.AccessSubject = "other-access"; return value }(),
		func() GrantExpectation { value := valid; value.BrowserID = "other-browser"; return value }(),
		func() GrantExpectation { value := valid; value.Generation++; return value }(),
	}
	for _, expected := range tests {
		if _, err := VerifyDeviceGrant(identity.PublicJWK(), token, expected); !errors.Is(err, ErrDeviceGrantContextMismatch) {
			t.Fatalf("wrong expectation %#v error = %v, want context mismatch", expected, err)
		}
	}
}

func TestDeviceGrantExpiryAndClockSkewLimits(t *testing.T) {
	t.Parallel()

	identity, err := GenerateDeviceIdentity()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_700_000_000, 0)
	expectation := func(claims GrantClaims, verificationTime time.Time) GrantExpectation {
		return GrantExpectation{
			DeviceID: claims.DeviceID, AccessSubject: claims.AccessSubject, BrowserID: claims.BrowserID,
			Generation: claims.Generation, Now: verificationTime,
		}
	}

	expiredClaims := grantClaimsFixture(now)
	expiredClaims.IssuedAt = now.Add(-time.Hour).Unix()
	expiredClaims.ExpiresAt = now.Unix()
	expired, err := SignDeviceGrant(identity, expiredClaims)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyDeviceGrant(identity.PublicJWK(), expired, expectation(expiredClaims, now)); !errors.Is(err, ErrDeviceGrantExpired) {
		t.Fatalf("expired grant error = %v, want ErrDeviceGrantExpired", err)
	}

	withinSkew := grantClaimsFixture(now)
	withinSkew.IssuedAt = now.Add(MaxGrantClockSkew).Unix()
	withinSkew.ExpiresAt = withinSkew.IssuedAt + int64(time.Hour/time.Second)
	withinToken, err := SignDeviceGrant(identity, withinSkew)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyDeviceGrant(identity.PublicJWK(), withinToken, expectation(withinSkew, now)); err != nil {
		t.Fatalf("grant at clock skew limit rejected: %v", err)
	}

	beyondSkew := withinSkew
	beyondSkew.IssuedAt++
	beyondSkew.ExpiresAt++
	beyondToken, err := SignDeviceGrant(identity, beyondSkew)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyDeviceGrant(identity.PublicJWK(), beyondToken, expectation(beyondSkew, now)); !errors.Is(err, ErrDeviceGrantNotYetValid) {
		t.Fatalf("future grant error = %v, want ErrDeviceGrantNotYetValid", err)
	}
}

func TestDeviceGrantRejectsTamperingWrongKeyAndMalformedTokensWithoutEchoingThem(t *testing.T) {
	t.Parallel()

	identity, err := GenerateDeviceIdentity()
	if err != nil {
		t.Fatal(err)
	}
	otherIdentity, err := GenerateDeviceIdentity()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_700_000_000, 0)
	claims := grantClaimsFixture(now)
	token, err := SignDeviceGrant(identity, claims)
	if err != nil {
		t.Fatal(err)
	}
	expected := GrantExpectation{
		DeviceID: claims.DeviceID, AccessSubject: claims.AccessSubject, BrowserID: claims.BrowserID,
		Generation: claims.Generation, Now: now,
	}
	parts := strings.Split(token, ".")
	tests := []struct {
		publicKey PublicKeyJWK
		token     string
	}{
		{publicKey: otherIdentity.PublicJWK(), token: token},
		{publicKey: identity.PublicJWK(), token: flipBase64URLCharacter(parts[0]) + "." + parts[1] + "." + parts[2]},
		{publicKey: identity.PublicJWK(), token: parts[0] + "." + flipBase64URLCharacter(parts[1]) + "." + parts[2]},
		{publicKey: identity.PublicJWK(), token: parts[0] + "." + parts[1] + "." + flipBase64URLCharacter(parts[2])},
		{publicKey: identity.PublicJWK(), token: "test-only-sensitive-marker"},
		{publicKey: PublicKeyJWK{KeyType: "EC", Curve: "P-256", X: "bad", Y: "bad"}, token: token},
	}
	for _, test := range tests {
		if _, err := VerifyDeviceGrant(test.publicKey, test.token, expected); err == nil {
			t.Fatalf("invalid grant verified: %q", test.token)
		} else if strings.Contains(err.Error(), test.token) || strings.Contains(err.Error(), token) || strings.Contains(err.Error(), "test-only-sensitive-marker") {
			t.Fatalf("error exposed grant content: %v", err)
		}
	}
}

func TestDeviceGrantClaimsUseStableWireNames(t *testing.T) {
	t.Parallel()

	claims := grantClaimsFixture(time.Unix(1_700_000_000, 0))
	encoded, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"version":1,"device_id":"device-1","access_subject":"access-1","browser_id":"browser-1","generation":7,"issued_at":1700000000,"expires_at":1700086400,"jti":"grant-1"}`
	if got := string(encoded); got != want {
		t.Fatalf("claims JSON = %s, want %s", got, want)
	}
}

func grantClaimsFixture(now time.Time) GrantClaims {
	return GrantClaims{
		Version:       ProtocolVersion,
		DeviceID:      "device-1",
		AccessSubject: "access-1",
		BrowserID:     "browser-1",
		Generation:    7,
		IssuedAt:      now.Unix(),
		ExpiresAt:     now.Add(24 * time.Hour).Unix(),
		JTI:           "grant-1",
	}
}
