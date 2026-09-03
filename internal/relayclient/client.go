package relayclient

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"sync"
	"time"

	"github.com/jamie950315/executor/internal/relay"
)

const (
	maximumRelayMessageBytes = 16 << 20
	maximumRelayResultBytes  = 64 << 20
)

var ErrResultTooLarge = errors.New("relay result too large")

func reconnectDelay(attempt int, sample uint64) time.Duration {
	if attempt < 0 {
		attempt = 0
	}
	baseSeconds := 1 << min(attempt, 4)
	if attempt >= 5 {
		baseSeconds = 30
	}
	base := time.Duration(baseSeconds) * time.Second
	fraction := float64(sample) / float64(math.MaxUint64)
	factor := 0.8 + 0.4*fraction
	return time.Duration(float64(base) * factor)
}

func resultMessages(requestID string, result []byte, maximumMessageBytes, maximumResultBytes int) ([]relay.Envelope, error) {
	if requestID == "" || maximumMessageBytes <= 0 || maximumResultBytes <= 0 {
		return nil, errors.New("invalid relay result")
	}
	if len(result) > maximumResultBytes {
		return nil, ErrResultTooLarge
	}
	response, err := relay.NewEnvelope(relay.MessageTypeResponse, resultMessageID(requestID, "response"), relay.ResponsePayload{
		RequestID: requestID, Result: append(json.RawMessage(nil), result...),
	})
	if err == nil {
		encoded, marshalErr := json.Marshal(response)
		if marshalErr == nil {
			if len(encoded)+1 > maximumResultBytes {
				return nil, ErrResultTooLarge
			}
			if len(encoded) <= maximumMessageBytes {
				return []relay.Envelope{response}, nil
			}
		}
	}
	if len(result) == 0 {
		return nil, errors.New("invalid relay result")
	}
	chunkSize := (maximumMessageBytes - 160) * 3 / 4
	if chunkSize < 1 {
		return nil, errors.New("relay message bound is too small")
	}
	messages := make([]relay.Envelope, 0, (len(result)+chunkSize-1)/chunkSize)
	totalWireBytes := 0
	for offset, sequence := 0, uint64(0); offset < len(result); sequence++ {
		size := min(chunkSize, len(result)-offset)
		var envelope relay.Envelope
		for {
			final := offset+size == len(result)
			envelope, err = relay.NewEnvelope(relay.MessageTypeStreamChunk, resultMessageID(requestID, "chunk"), relay.StreamChunkPayload{
				RequestID: requestID, Sequence: sequence, Data: append([]byte(nil), result[offset:offset+size]...), Final: final,
			})
			if err != nil {
				return nil, errors.New("relay result serialization failed")
			}
			encoded, marshalErr := json.Marshal(envelope)
			if marshalErr == nil && len(encoded) <= maximumMessageBytes {
				if totalWireBytes+len(encoded)+1 > maximumResultBytes {
					return nil, ErrResultTooLarge
				}
				totalWireBytes += len(encoded) + 1
				break
			}
			if size == 1 {
				return nil, errors.New("relay message bound is too small")
			}
			size /= 2
		}
		messages = append(messages, envelope)
		offset += size
	}
	return messages, nil
}

func resultMessageID(requestID, kind string) string {
	digest := sha256.Sum256([]byte(requestID))
	return kind + "-" + hex.EncodeToString(digest[:])
}

type requestRegistry struct {
	mu      sync.Mutex
	cancels map[string]func()
}

func newRequestRegistry() *requestRegistry {
	return &requestRegistry{cancels: map[string]func(){}}
}

func (r *requestRegistry) Add(requestID string, cancel func()) bool {
	if requestID == "" || cancel == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.cancels[requestID]; exists {
		return false
	}
	r.cancels[requestID] = cancel
	return true
}

func (r *requestRegistry) Remove(requestID string) {
	r.mu.Lock()
	delete(r.cancels, requestID)
	r.mu.Unlock()
}

func (r *requestRegistry) Cancel(requestID string) bool {
	r.mu.Lock()
	cancel, exists := r.cancels[requestID]
	if exists {
		delete(r.cancels, requestID)
	}
	r.mu.Unlock()
	if exists {
		cancel()
	}
	return exists
}

func (r *requestRegistry) CancelAll() {
	r.mu.Lock()
	cancels := make([]func(), 0, len(r.cancels))
	for _, cancel := range r.cancels {
		cancels = append(cancels, cancel)
	}
	clear(r.cancels)
	r.mu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
}
