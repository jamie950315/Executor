package ipc

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"
)

type Message struct {
	ID        string          `json:"id"`
	Timestamp int64           `json:"timestamp"`
	Nonce     string          `json:"nonce"`
	Method    string          `json:"method"`
	Params    json.RawMessage `json:"params,omitempty"`
	MAC       string          `json:"mac"`
}

func Sign(message *Message, key []byte) error {
	if err := validate(message, key); err != nil {
		return err
	}
	mac, err := calculate(*message, key)
	if err != nil {
		return err
	}
	message.MAC = mac
	return nil
}

type Verifier struct {
	key    []byte
	window time.Duration
	mu     sync.Mutex
	seen   map[string]time.Time
}

func NewVerifier(key []byte, window time.Duration) *Verifier {
	return &Verifier{key: append([]byte(nil), key...), window: window, seen: make(map[string]time.Time)}
}

func (v *Verifier) Verify(message Message, now time.Time) error {
	if err := validate(&message, v.key); err != nil {
		return err
	}
	if message.MAC == "" {
		return errors.New("missing message MAC")
	}
	messageTime := time.Unix(message.Timestamp, 0)
	delta := now.Sub(messageTime)
	if delta < 0 {
		delta = -delta
	}
	if v.window <= 0 || delta > v.window {
		return errors.New("message timestamp outside acceptance window")
	}
	want, err := calculate(message, v.key)
	if err != nil {
		return err
	}
	if len(want) != len(message.MAC) || subtle.ConstantTimeCompare([]byte(want), []byte(message.MAC)) != 1 {
		return errors.New("invalid message MAC")
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	for nonce, expiresAt := range v.seen {
		if expiresAt.Before(now) {
			delete(v.seen, nonce)
		}
	}
	if _, exists := v.seen[message.Nonce]; exists {
		return errors.New("replayed message")
	}
	// Future-dated messages remain acceptable past a window from first receipt.
	// Retain their nonce for the entire timestamp acceptance period.
	v.seen[message.Nonce] = messageTime.Add(v.window)
	return nil
}

func validate(message *Message, key []byte) error {
	if len(key) < 32 {
		return errors.New("IPC key must contain at least 32 bytes")
	}
	if message == nil || message.ID == "" || message.Timestamp == 0 || message.Nonce == "" || message.Method == "" {
		return errors.New("incomplete IPC message identity")
	}
	return nil
}

func calculate(message Message, key []byte) (string, error) {
	canonical := struct {
		ID        string          `json:"id"`
		Timestamp int64           `json:"timestamp"`
		Nonce     string          `json:"nonce"`
		Method    string          `json:"method"`
		Params    json.RawMessage `json:"params,omitempty"`
	}{message.ID, message.Timestamp, message.Nonce, message.Method, message.Params}
	data, err := json.Marshal(canonical)
	if err != nil {
		return "", fmt.Errorf("marshal IPC message: %w", err)
	}
	h := hmac.New(sha256.New, key)
	_, _ = h.Write(data)
	return base64.RawURLEncoding.EncodeToString(h.Sum(nil)), nil
}
