package filesystem

import (
	"errors"
	"fmt"
	"io"
	"math"
	"os"
)

const (
	DefaultReadLimit = 64 << 10
	MaxReadLimit     = 1 << 20
)

var ErrRangeUnavailable = errors.New("filesystem range reads are unavailable; update the Executor helper")

// RangeReader is an optional extension; existing Service implementations retain
// their ReadFile API. A missing extension never falls back to an unbounded read.
type RangeReader interface {
	ReadFileRange(path string, offset int64, limit int) (ReadRangeResult, error)
}

// ReadRangeResult describes raw bytes. EOF is determined by reading at most one
// extra byte, so it also works for virtual files whose reported size is zero.
// Each call observes the current file; pagination is not a snapshot of a file
// that another process is modifying.
type ReadRangeResult struct {
	Data            []byte `json:"data"`
	OffsetBytes     int64  `json:"offset_bytes"`
	NextOffsetBytes int64  `json:"next_offset_bytes"`
	Truncated       bool   `json:"truncated"`
	EOF             bool   `json:"eof"`
}

func ValidateReadRange(offset int64, limit int) error {
	if offset < 0 {
		return errors.New("filesystem offset must be non-negative bytes")
	}
	if limit < 1 || limit > MaxReadLimit {
		return fmt.Errorf("filesystem limit must be between 1 and %d bytes", MaxReadLimit)
	}
	if offset > math.MaxInt64-int64(limit)-1 {
		return errors.New("filesystem byte range overflows the file offset")
	}
	return nil
}

func ReadRange(service any, path string, offset int64, limit int) (ReadRangeResult, error) {
	if err := ValidateReadRange(offset, limit); err != nil {
		return ReadRangeResult{}, err
	}
	reader, ok := service.(RangeReader)
	if !ok {
		return ReadRangeResult{}, ErrRangeUnavailable
	}
	return reader.ReadFileRange(path, offset, limit)
}

func (s *LocalService) ReadFileRange(path string, offset int64, limit int) (ReadRangeResult, error) {
	if err := ValidateReadRange(offset, limit); err != nil {
		return ReadRangeResult{}, err
	}
	file, err := os.Open(path)
	if err != nil {
		return ReadRangeResult{}, err
	}
	defer file.Close()
	return readRangeAt(file, offset, limit)
}

func readRangeAt(reader io.ReaderAt, offset int64, limit int) (ReadRangeResult, error) {
	if err := ValidateReadRange(offset, limit); err != nil {
		return ReadRangeResult{}, err
	}
	data := make([]byte, limit+1)
	n, err := reader.ReadAt(data, offset)
	if err != nil && !errors.Is(err, io.EOF) {
		return ReadRangeResult{}, err
	}
	truncated := n > limit
	if truncated {
		n = limit
	}
	return ReadRangeResult{
		Data: data[:n], OffsetBytes: offset, NextOffsetBytes: offset + int64(n),
		Truncated: truncated, EOF: !truncated,
	}, nil
}
