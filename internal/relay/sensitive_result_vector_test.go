package relay

import (
	"encoding/json"
	"os"
	"testing"
)

func TestSensitiveResultCrossLanguageVectorOpensInGo(t *testing.T) {
	t.Parallel()

	encoded, err := os.ReadFile("testdata/wire-vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var vectors struct {
		SensitiveResult struct {
			Context               SensitiveResultContext  `json:"context"`
			Plaintext             string                  `json:"test_only_plaintext"`
			BrowserPrivateKey     json.RawMessage         `json:"test_only_browser_private_key"`
			ExpectedEnvelope      SensitiveResultEnvelope `json:"expected_envelope"`
			AdditionalData        string                  `json:"aad"`
		} `json:"sensitive_result"`
	}
	if err := json.Unmarshal(encoded, &vectors); err != nil {
		t.Fatal(err)
	}
	identity, err := ParseDeviceIdentity(vectors.SensitiveResult.BrowserPrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	opened, err := OpenSensitiveResultEnvelope(identity, vectors.SensitiveResult.ExpectedEnvelope, vectors.SensitiveResult.Context)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(opened)
	if string(opened) != vectors.SensitiveResult.Plaintext {
		t.Fatalf("opened plaintext mismatch")
	}
	if got := string(sensitiveResultAdditionalData(vectors.SensitiveResult.Context)); got != vectors.SensitiveResult.AdditionalData {
		t.Fatalf("AAD = %q, want %q", got, vectors.SensitiveResult.AdditionalData)
	}
}
