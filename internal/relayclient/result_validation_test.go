package relayclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/jamie950315/executor/internal/config"
	"github.com/jamie950315/executor/internal/relay"
)

func invalidResultFixtures() []struct {
	name string
	data []byte
} {
	return []struct {
		name string
		data []byte
	}{
		{name: "nil", data: nil},
		{name: "empty", data: []byte{}},
		{name: "whitespace", data: []byte(" \t\r\n")},
		{name: "truncated_json", data: []byte(`{"content":`)},
		{name: "trailing_json", data: []byte(`{} {}`)},
		{name: "plain_text", data: []byte("invalid-result-private-marker")},
		{name: "large_invalid_json", data: bytes.Repeat([]byte("invalid-result-private-marker"), 80)},
		{name: "invalid_utf8", data: []byte{'"', 0xff, '"'}},
	}
}

func TestResultMessagesRejectInvalidJSONBeforeEitherEncodingPath(t *testing.T) {
	t.Parallel()
	for _, fixture := range invalidResultFixtures() {
		for _, bound := range []int{300, 4096} {
			t.Run(fmt.Sprintf("%s/bound_%d", fixture.name, bound), func(t *testing.T) {
				messages, err := resultMessages("invalid-result", fixture.data, bound, 64*1024)
				if err == nil {
					t.Fatalf("invalid result accepted: emitted %d message(s)", len(messages))
				}
				if len(messages) != 0 {
					t.Fatalf("invalid result emitted %d message(s) with error %v", len(messages), err)
				}
			})
		}
	}
}

func TestResultMessagesPreserveValidJSONIncludingEmptyValues(t *testing.T) {
	t.Parallel()
	multilingual, err := json.Marshal(strings.Repeat("中文🙂", 256))
	if err != nil {
		t.Fatal(err)
	}
	fixtures := []struct {
		name string
		data []byte
	}{
		{name: "null", data: []byte(" null \n")},
		{name: "empty_string", data: []byte(`""`)},
		{name: "empty_object", data: []byte(`{}`)},
		{name: "empty_array", data: []byte(`[]`)},
		{name: "false", data: []byte(`false`)},
		{name: "zero", data: []byte(`0`)},
		{name: "empty_file", data: []byte(`{"content":"","encoding":"base64","size":0,"returnedBytes":0,"eof":true}`)},
		{name: "multilingual", data: multilingual},
	}
	for _, fixture := range fixtures {
		for _, bound := range []int{300, 4096} {
			t.Run(fmt.Sprintf("%s/bound_%d", fixture.name, bound), func(t *testing.T) {
				const requestID = "valid-result"
				messages, err := resultMessages(requestID, fixture.data, bound, 64*1024)
				if err != nil || len(messages) == 0 {
					t.Fatalf("valid result rejected: count=%d err=%v", len(messages), err)
				}
				var rebuilt []byte
				for sequence, message := range messages {
					wire, err := json.Marshal(message)
					if err != nil || len(wire) > bound {
						t.Fatalf("message exceeded bound: size=%d err=%v", len(wire), err)
					}
					switch message.Type {
					case relay.MessageTypeResponse:
						payload, err := relay.DecodePayload[relay.ResponsePayload](message)
						if err != nil || len(messages) != 1 || payload.RequestID != requestID || payload.Failure != nil || len(payload.Result) == 0 {
							t.Fatalf("invalid successful response: %#v err=%v", payload, err)
						}
						rebuilt = payload.Result
					case relay.MessageTypeStreamChunk:
						payload, err := relay.DecodePayload[relay.StreamChunkPayload](message)
						if err != nil || payload.RequestID != requestID || payload.Sequence != uint64(sequence) || payload.Final != (sequence == len(messages)-1) {
							t.Fatalf("invalid chunk metadata: %#v err=%v", payload, err)
						}
						rebuilt = append(rebuilt, payload.Data...)
					default:
						t.Fatalf("unexpected message type: %s", message.Type)
					}
				}
				var got, want bytes.Buffer
				if err := json.Compact(&got, rebuilt); err != nil {
					t.Fatalf("reconstructed result is invalid JSON: %v", err)
				}
				if err := json.Compact(&want, fixture.data); err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(got.Bytes(), want.Bytes()) {
					t.Fatal("result changed during encoding")
				}
				if fixture.name == "multilingual" && bound == 300 && len(messages) < 2 {
					t.Fatal("large valid result did not exercise chunking")
				}
			})
		}
	}
}

func TestResultMessagesKeepOversizeFailurePrecedence(t *testing.T) {
	t.Parallel()
	messages, err := resultMessages("oversize-result", bytes.Repeat([]byte("x"), 128), 300, 64)
	if !errors.Is(err, ErrResultTooLarge) || len(messages) != 0 {
		t.Fatalf("oversize result: messages=%d err=%v", len(messages), err)
	}
}

func TestConnectionWritesInvalidResultFailureAndRemainsUsable(t *testing.T) {
	t.Parallel()
	for _, fixture := range invalidResultFixtures() {
		t.Run(fixture.name, func(t *testing.T) {
			socket := newFakeSocket()
			connection := newConnection(nil, socket, config.Config{})
			const requestID = "invalid-result-request"
			if err := connection.writeResult(context.Background(), requestID, fixture.data); err != nil {
				t.Fatalf("failed to return an explicit failure: %v", err)
			}
			if len(socket.writes) != 1 {
				t.Fatalf("invalid result produced %d writes, want one failure", len(socket.writes))
			}
			wire := nextFakeWrite(t, socket)
			envelope := decodeEnvelopeForTest(t, wire)
			payload, err := relay.DecodePayload[relay.ResponsePayload](envelope)
			if err != nil || envelope.Type != relay.MessageTypeResponse || payload.RequestID != requestID || payload.Failure == nil || payload.Failure.Code != "invalid_result" || len(payload.Result) != 0 {
				t.Fatalf("invalid result failure: %#v err=%v", payload, err)
			}
			if bytes.Contains(wire, []byte("invalid-result-private-marker")) {
				t.Fatal("failure disclosed rejected result content")
			}
			select {
			case <-socket.closed:
				t.Fatal("invalid result closed an otherwise usable connection")
			default:
			}

			if err := connection.writeResult(context.Background(), "next-valid-request", json.RawMessage(`{"ok":true}`)); err != nil {
				t.Fatal(err)
			}
			next := decodeEnvelopeForTest(t, nextFakeWrite(t, socket))
			nextPayload, err := relay.DecodePayload[relay.ResponsePayload](next)
			if err != nil || nextPayload.RequestID != "next-valid-request" || nextPayload.Failure != nil || string(nextPayload.Result) != `{"ok":true}` {
				t.Fatalf("subsequent result: %#v err=%v", nextPayload, err)
			}
			if len(socket.writes) != 0 {
				t.Fatal("unexpected duplicate response")
			}
		})
	}
}

type resultWriteErrorSocket struct {
	*fakeSocket
	err      error
	attempts int
}

func (s *resultWriteErrorSocket) Write(context.Context, []byte) error {
	s.attempts++
	return s.err
}

func TestConnectionPropagatesResultWriteFailureWithoutRetry(t *testing.T) {
	t.Parallel()
	for _, fixture := range []struct {
		name string
		data json.RawMessage
	}{
		{name: "invalid_result", data: nil},
		{name: "valid_result", data: json.RawMessage(`{"ok":true}`)},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			transportError := errors.New("test transport failure")
			socket := &resultWriteErrorSocket{fakeSocket: newFakeSocket(), err: transportError}
			connection := newConnection(nil, socket, config.Config{})
			if err := connection.writeResult(context.Background(), "write-failure-request", fixture.data); !errors.Is(err, transportError) {
				t.Fatalf("write error = %v, want transport failure", err)
			}
			if socket.attempts != 1 {
				t.Fatalf("write attempts = %d, want 1", socket.attempts)
			}
		})
	}
}
