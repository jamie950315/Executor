package ipc

import (
	"encoding/json"
	"testing"
	"time"
)

func TestVerifierRetainsFutureDatedNonceUntilMessageExpires(t *testing.T) {
	key := []byte("01234567890123456789012345678901")
	window := 30 * time.Second
	now := time.Unix(1_760_000_000, 0)
	message := Message{ID: "future", Timestamp: now.Add(window).Unix(), Nonce: "future-nonce", Method: "test"}
	if err := Sign(&message, key); err != nil {
		t.Fatal(err)
	}
	verifier := NewVerifier(key, window)
	if err := verifier.Verify(message, now); err != nil {
		t.Fatalf("initial valid message: %v", err)
	}
	if err := verifier.Verify(message, now.Add(window+time.Second)); err == nil {
		t.Fatal("future-dated message replayed after cache eviction but before timestamp expiry")
	}
}

func TestSignVerifyAcceptsFreshMessageOnce(t *testing.T) {
	key := []byte("01234567890123456789012345678901")
	now := time.Unix(1000, 0).UTC()
	msg := Message{ID: "request-1", Timestamp: now.Unix(), Nonce: "unique-nonce", Method: "terminal.start", Params: json.RawMessage(`{"identity":"administrator"}`)}
	if err := Sign(&msg, key); err != nil {
		t.Fatal(err)
	}
	verifier := NewVerifier(key, time.Minute)
	if err := verifier.Verify(msg, now); err != nil {
		t.Fatalf("first Verify: %v", err)
	}
	if err := verifier.Verify(msg, now); err == nil {
		t.Fatal("replayed message was accepted")
	}
}

func TestVerifyRejectsTamperingAndStaleMessages(t *testing.T) {
	key := []byte("01234567890123456789012345678901")
	now := time.Unix(1000, 0).UTC()
	base := Message{ID: "request-1", Timestamp: now.Unix(), Nonce: "nonce", Method: "terminal.start", Params: json.RawMessage(`{}`)}
	if err := Sign(&base, key); err != nil {
		t.Fatal(err)
	}
	tampered := base
	tampered.Method = "filesystem.delete"
	if err := NewVerifier(key, time.Minute).Verify(tampered, now); err == nil {
		t.Fatal("tampered message was accepted")
	}
	stale := base
	stale.Timestamp = now.Add(-2 * time.Minute).Unix()
	if err := Sign(&stale, key); err != nil {
		t.Fatal(err)
	}
	if err := NewVerifier(key, time.Minute).Verify(stale, now); err == nil {
		t.Fatal("stale message was accepted")
	}
}

func TestSignRequiresStrongKeyAndMessageIdentity(t *testing.T) {
	tests := []Message{
		{Timestamp: 1, Nonce: "nonce", Method: "method"},
		{ID: "id", Timestamp: 1, Method: "method"},
		{ID: "id", Timestamp: 1, Nonce: "nonce"},
	}
	for _, msg := range tests {
		if err := Sign(&msg, []byte("short")); err == nil {
			t.Fatalf("Sign accepted invalid message/key: %#v", msg)
		}
	}
}
