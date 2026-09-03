package relay

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestSensitiveResultEnvelopeRoundTripBindsRequestAndOmitsPlaintext(t *testing.T) {
	t.Parallel()
	browser, err := GenerateDeviceIdentity()
	if err != nil {
		t.Fatal(err)
	}
	context := SensitiveResultContext{
		DeviceID: "device-1", AccessSubject: "access-1", BrowserID: "browser-1",
		Generation: 8, RequestID: "request-1", Method: "control.rotate",
	}
	plaintext := []byte(`{"recovery_key":"test-only-recovery","url_secret":"test-only-url","dashboard":"test-only-dashboard"}`)
	envelope, err := SealSensitiveResultEnvelope(browser.PublicJWK(), context, plaintext)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"test-only-recovery", "test-only-url", "test-only-dashboard"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("sensitive envelope exposed %q: %s", secret, encoded)
		}
	}
	opened, err := OpenSensitiveResultEnvelope(browser, envelope, context)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(opened, plaintext) {
		t.Fatalf("plaintext = %s, want %s", opened, plaintext)
	}

	wrong := context
	wrong.RequestID = "request-2"
	if _, err := OpenSensitiveResultEnvelope(browser, envelope, wrong); !errors.Is(err, ErrSensitiveResultContextMismatch) {
		t.Fatalf("wrong request error = %v", err)
	}
	wrong = context
	wrong.Method = "control.kill"
	if _, err := OpenSensitiveResultEnvelope(browser, envelope, wrong); !errors.Is(err, ErrSensitiveResultContextMismatch) {
		t.Fatalf("wrong method error = %v", err)
	}
}
