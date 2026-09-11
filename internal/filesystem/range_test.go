package filesystem

import (
	"bytes"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"testing"
)

type recordingRangeReader struct {
	t        *testing.T
	data     []byte
	calls    int
	capacity int
	offset   int64
	err      error
}

func (r *recordingRangeReader) ReadAt(p []byte, offset int64) (int, error) {
	r.t.Helper()
	r.calls++
	r.capacity = len(p)
	r.offset = offset
	if len(p) > MaxReadLimit+1 {
		r.t.Fatalf("unbounded read requested %d bytes", len(p))
	}
	if r.err != nil {
		copy(p, "partial")
		return min(len(p), 7), r.err
	}
	if offset >= int64(len(r.data)) {
		return 0, io.EOF
	}
	n := copy(p, r.data[int(offset):])
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

func TestReadRangeAtBoundsIOAndDetectsEOFWithoutStat(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name      string
		data      string
		offset    int64
		limit     int
		want      string
		truncated bool
	}{
		{"empty", "", 0, 4, "", false},
		{"all", "abcdef", 0, 8, "abcdef", false},
		{"exact-end", "abcdef", 2, 4, "cdef", false},
		{"one-byte-lookahead", "abcdefg", 2, 4, "cdef", true},
		{"large-tail", "abcdefghijklmnop", 2, 4, "cdef", true},
		{"at-eof", "abc", 3, 4, "", false},
		{"past-eof", "abc", 12, 4, "", false},
		{"single-byte", "abc", 0, 1, "a", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			reader := &recordingRangeReader{t: t, data: []byte(test.data)}
			result, err := readRangeAt(reader, test.offset, test.limit)
			if err != nil {
				t.Fatal(err)
			}
			if string(result.Data) != test.want || result.OffsetBytes != test.offset || result.NextOffsetBytes != test.offset+int64(len(test.want)) || result.Truncated != test.truncated || result.EOF == test.truncated {
				t.Fatalf("range = %#v", result)
			}
			if reader.calls != 1 || reader.capacity != test.limit+1 || reader.offset != test.offset {
				t.Fatalf("IO calls=%d bytes=%d offset=%d", reader.calls, reader.capacity, reader.offset)
			}
		})
	}
}

func TestReadRangeAtRejectsInvalidBoundsBeforeIO(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		offset int64
		limit  int
	}{
		{-1, 4}, {0, 0}, {0, -1}, {0, MaxReadLimit + 1}, {math.MaxInt64, 1}, {math.MaxInt64 - 1, 1},
	} {
		reader := &recordingRangeReader{t: t}
		if _, err := readRangeAt(reader, test.offset, test.limit); err == nil || reader.calls != 0 {
			t.Errorf("offset=%d limit=%d error=%v calls=%d", test.offset, test.limit, err, reader.calls)
		}
	}
	reader := &recordingRangeReader{t: t}
	result, err := readRangeAt(reader, math.MaxInt64-2, 1)
	if err != nil || result.NextOffsetBytes != math.MaxInt64-2 || reader.calls != 1 {
		t.Fatalf("valid highest offset: %#v, %v", result, err)
	}
}

func TestReadRangeAtPreservesIOErrorsAndMaximumBound(t *testing.T) {
	t.Parallel()
	wantErr := errors.New("fixture read failed")
	reader := &recordingRangeReader{t: t, err: wantErr}
	if result, err := readRangeAt(reader, 0, 10); !errors.Is(err, wantErr) || len(result.Data) != 0 {
		t.Fatalf("partial read failure became success: %#v, %v", result, err)
	}
	reader = &recordingRangeReader{t: t, data: bytes.Repeat([]byte("x"), MaxReadLimit+100)}
	result, err := readRangeAt(reader, 0, MaxReadLimit)
	if err != nil || len(result.Data) != MaxReadLimit || !result.Truncated || reader.capacity != MaxReadLimit+1 {
		t.Fatalf("maximum bound: bytes=%d truncated=%v capacity=%d error=%v", len(result.Data), result.Truncated, reader.capacity, err)
	}
}

func TestLocalReadRangeUsesExactFileOffsetAndPreservesLegacyAPI(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "fixture.bin")
	data := []byte{0xff, 0x00, 0x10, 0x40, 0xe4, 0xb8, 0xad}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	service := NewLocalService()
	result, err := ReadRange(service, path, 2, 3)
	if err != nil || !bytes.Equal(result.Data, data[2:5]) || result.NextOffsetBytes != 5 || !result.Truncated {
		t.Fatalf("file range: %#v, %v", result, err)
	}
	legacy, err := service.ReadFile(path)
	if err != nil || !bytes.Equal(legacy, data) {
		t.Fatalf("legacy bytes changed: %v", err)
	}
	if _, err := service.ReadFileRange(filepath.Join(t.TempDir(), "missing"), 0, 1); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing file error=%v", err)
	}
}

type legacyOnlyReader struct{ calls int }

func (l *legacyOnlyReader) ReadFile(string) ([]byte, error) {
	l.calls++
	return []byte("legacy"), nil
}

func TestReadRangeLegacyServiceFailsExplicitlyWithoutUnboundedFallback(t *testing.T) {
	t.Parallel()
	legacy := &legacyOnlyReader{}
	if _, err := ReadRange(legacy, "/fixture", 0, 1); !errors.Is(err, ErrRangeUnavailable) || legacy.calls != 0 {
		t.Fatalf("legacy fallback: %v calls=%d", err, legacy.calls)
	}
}
