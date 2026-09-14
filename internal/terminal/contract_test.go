package terminal

import (
	"bytes"
	"encoding/json"
	"testing"
)

type contractLimitedReader interface {
	ReadLimited(string, int64, int) (OutputChunk, error)
}

func contractJSON(t *testing.T, value any) map[string]any {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	return fields
}

func TestTerminalContractLimitedReadPreservesBytesAndLossSemantics(t *testing.T) {
	b := newOutputBuffer(71)
	data := bytes.Repeat([]byte{0x00, 0xff, 0xe8, 0xaa, 0x9e}, 30)
	b.Append(data[:120])
	b.Append(data[120:])
	m := &Manager{sessions: map[string]*sessionState{"s": {output: b, running: true}}}
	reader, ok := any(m).(contractLimitedReader)
	if !ok {
		t.Fatal("terminal manager has no bounded read API")
	}
	var got []byte
	cursor := int64(0)
	for page := 0; ; page++ {
		chunk, err := reader.ReadLimited("s", cursor, 13)
		if err != nil {
			t.Fatal(err)
		}
		fields := contractJSON(t, chunk)
		if len(chunk.Data) > 13 || chunk.NextCursor != chunk.StartCursor+int64(len(chunk.Data)) || chunk.Truncated != (page == 0) {
			t.Fatalf("incorrect pagination/loss: %#v", chunk)
		}
		if fields["encoding"] != "base64" || fields["returnedBytes"] != float64(len(chunk.Data)) || fields["sessionRunning"] != true {
			t.Fatalf("incorrect output metadata: %#v", fields)
		}
		got = append(got, chunk.Data...)
		cursor = chunk.NextCursor
		if fields["hasMore"] == false {
			break
		}
		if page > 10 {
			t.Fatal("pagination failed to advance")
		}
	}
	if !bytes.Equal(got, data[len(data)-71:]) {
		t.Fatal("paginated bytes differ from retained tail")
	}
	for _, tc := range []struct {
		cursor int64
		limit  int
	}{{-1, 32}, {0, -1}, {0, 1048577}} {
		if _, err := reader.ReadLimited("s", tc.cursor, tc.limit); err == nil {
			t.Fatalf("accepted invalid read %#v", tc)
		}
	}
}

func TestTerminalContractLimitedReadCopiesOnlyRequestedBytes(t *testing.T) {
	b := newOutputBuffer(8 << 20)
	b.Append(bytes.Repeat([]byte{'a'}, 8<<20))
	m := &Manager{sessions: map[string]*sessionState{"s": {output: b}}}
	reader, ok := any(m).(contractLimitedReader)
	if !ok {
		t.Fatal("terminal manager has no bounded read API")
	}
	chunk, err := reader.ReadLimited("s", 0, 32)
	if err != nil {
		t.Fatal(err)
	}
	if len(chunk.Data) != 32 || cap(chunk.Data) != 32 || chunk.Truncated || contractJSON(t, chunk)["hasMore"] != true {
		t.Fatalf("read did not allocate a bounded non-lossy page: len=%d cap=%d metadata=%#v", len(chunk.Data), cap(chunk.Data), contractJSON(t, chunk))
	}
}
