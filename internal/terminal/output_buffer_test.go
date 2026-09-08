package terminal

import (
	"bytes"
	"testing"
)

func TestOutputBufferMatchesTailAcrossWraps(t *testing.T) {
	for _, capacity := range []int{1, 7, 64} {
		buffer := newOutputBuffer(capacity)
		var full []byte
		for _, size := range []int{0, 3, 5, 1, 200, 2, 70} {
			chunk := bytes.Repeat([]byte{byte(size)}, size)
			full = append(full, chunk...)
			buffer.Append(chunk)
			for cursor := -1; cursor <= len(full)+1; cursor++ {
				start := min(max(cursor, max(0, len(full)-capacity)), len(full))
				got := buffer.Read(int64(cursor))
				if !bytes.Equal(got.Data, full[start:]) || got.StartCursor != int64(start) || got.NextCursor != int64(len(full)) || got.Truncated != (max(cursor, 0) < len(full)-capacity) {
					t.Fatalf("capacity=%d size=%d cursor=%d: %#v", capacity, size, cursor, got)
				}
			}
		}
	}
}

func BenchmarkOutputBufferAppend(b *testing.B) {
	buffer := newOutputBuffer(defaultOutputBufferSize)
	chunk := make([]byte, 4096)
	b.SetBytes(int64(len(chunk)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		buffer.Append(chunk)
	}
}

func BenchmarkOutputBufferRead(b *testing.B) {
	buffer := newOutputBuffer(defaultOutputBufferSize)
	buffer.Append(make([]byte, defaultOutputBufferSize))
	b.SetBytes(defaultOutputBufferSize)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = buffer.Read(0)
	}
}
