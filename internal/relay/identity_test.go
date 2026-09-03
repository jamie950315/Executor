package relay

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestDeviceIdentityPrivateJWKRoundTrip(t *testing.T) {
	t.Parallel()

	identity, err := GenerateDeviceIdentity()
	if err != nil {
		t.Fatalf("GenerateDeviceIdentity: %v", err)
	}
	encoded, err := identity.MarshalPrivateJWK()
	if err != nil {
		t.Fatalf("MarshalPrivateJWK: %v", err)
	}

	var fields map[string]string
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatalf("unmarshal private JWK: %v", err)
	}
	if len(fields) != 5 || fields["kty"] != "EC" || fields["crv"] != "P-256" {
		t.Fatalf("private JWK fields = %#v", fields)
	}
	for _, name := range []string{"x", "y", "d"} {
		if len(fields[name]) != 43 {
			t.Fatalf("%s length = %d, want 43 base64url characters", name, len(fields[name]))
		}
	}

	restored, err := ParseDeviceIdentity(encoded)
	if err != nil {
		t.Fatalf("ParseDeviceIdentity: %v", err)
	}
	if !reflect.DeepEqual(restored.PublicJWK(), identity.PublicJWK()) {
		t.Fatalf("restored public key = %#v, want %#v", restored.PublicJWK(), identity.PublicJWK())
	}
}

func TestDevicePublicJWKIsBrowserCompatibleAndOmitsPrivateMaterial(t *testing.T) {
	t.Parallel()

	identity, err := GenerateDeviceIdentity()
	if err != nil {
		t.Fatal(err)
	}
	publicJWK := identity.PublicJWK()
	encoded, err := json.Marshal(publicJWK)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(encoded), `{"kty":"EC","crv":"P-256","x":"`+publicJWK.X+`","y":"`+publicJWK.Y+`"}`; got != want {
		t.Fatalf("public JWK = %s, want %s", got, want)
	}
	if strings.Contains(string(encoded), `"d"`) {
		t.Fatalf("public JWK exposed private material: %s", encoded)
	}
	parsed, err := ParsePublicKeyJWK(publicJWK)
	if err != nil {
		t.Fatalf("ParsePublicKeyJWK: %v", err)
	}
	if parsed.X.Cmp(identity.publicKey().X) != 0 || parsed.Y.Cmp(identity.publicKey().Y) != 0 {
		t.Fatal("parsed public key differs from identity")
	}
}

func TestDeviceIdentityRejectsMalformedOrMismatchedKeysWithoutEchoingInput(t *testing.T) {
	t.Parallel()

	secretMarker := "test-only-sensitive-marker"
	identity, err := GenerateDeviceIdentity()
	if err != nil {
		t.Fatal(err)
	}
	privateData, err := identity.MarshalPrivateJWK()
	if err != nil {
		t.Fatal(err)
	}
	var valid PrivateKeyJWK
	if err := json.Unmarshal(privateData, &valid); err != nil {
		t.Fatal(err)
	}

	privateTests := [][]byte{
		[]byte(secretMarker),
		[]byte(`{"kty":"RSA","crv":"P-256","x":"` + valid.X + `","y":"` + valid.Y + `","d":"` + valid.D + `"}`),
		[]byte(`{"kty":"EC","crv":"P-384","x":"` + valid.X + `","y":"` + valid.Y + `","d":"` + valid.D + `"}`),
		[]byte(`{"kty":"EC","crv":"P-256","x":"` + valid.Y + `","y":"` + valid.X + `","d":"` + valid.D + `"}`),
		[]byte(`{"kty":"EC","crv":"P-256","x":"` + valid.X + `","y":"` + valid.Y + `","d":"AA"}`),
		[]byte(`{"kty":"EC","crv":"P-256","x":"` + valid.X + `","y":"` + valid.Y + `","d":"` + valid.D + `","extra":"` + secretMarker + `"}`),
	}
	for _, input := range privateTests {
		if _, err := ParseDeviceIdentity(input); err == nil {
			t.Fatalf("ParseDeviceIdentity accepted malformed input: %s", input)
		} else if strings.Contains(err.Error(), secretMarker) || strings.Contains(err.Error(), valid.D) {
			t.Fatalf("error exposed key material: %v", err)
		}
	}

	publicTests := []PublicKeyJWK{
		{KeyType: "RSA", Curve: "P-256", X: valid.X, Y: valid.Y},
		{KeyType: "EC", Curve: "P-384", X: valid.X, Y: valid.Y},
		{KeyType: "EC", Curve: "P-256", X: secretMarker, Y: valid.Y},
		{KeyType: "EC", Curve: "P-256", X: valid.X, Y: "AA"},
	}
	for _, input := range publicTests {
		if _, err := ParsePublicKeyJWK(input); err == nil {
			t.Fatalf("ParsePublicKeyJWK accepted malformed input: %#v", input)
		} else if strings.Contains(err.Error(), secretMarker) || strings.Contains(err.Error(), valid.X) {
			t.Fatalf("error exposed key material: %v", err)
		}
	}
}
