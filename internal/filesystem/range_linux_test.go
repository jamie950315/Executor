//go:build linux

package filesystem

import (
	"bytes"
	"os"
	"testing"
)

func TestLocalReadRangeReadsZeroSizeProcFile(t *testing.T) {
	info, err := os.Stat("/proc/self/cmdline")
	if err != nil {
		t.Skipf("procfs unavailable: %v", err)
	}
	if info.Size() != 0 {
		t.Fatalf("expected zero-size virtual file, got %d", info.Size())
	}
	data, err := os.ReadFile("/proc/self/cmdline")
	if err != nil || len(data) < 4 {
		t.Fatalf("procfs fixture data: bytes=%d error=%v", len(data), err)
	}
	result, err := NewLocalService().ReadFileRange("/proc/self/cmdline", 1, 2)
	if err != nil || !bytes.Equal(result.Data, data[1:3]) || !result.Truncated || result.EOF {
		t.Fatalf("procfs range: %#v, %v", result, err)
	}
}
