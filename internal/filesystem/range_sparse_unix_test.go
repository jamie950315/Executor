//go:build darwin || linux

package filesystem

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLocalReadRangeReadsSparseFileWithoutLoadingWholeFile(t *testing.T) {
	t.Parallel()
	file, err := os.Create(filepath.Join(t.TempDir(), "sparse"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	const offset = int64(8 << 30)
	if err := file.Truncate(offset + 4); err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteAt([]byte("tail"), offset); err != nil {
		t.Fatal(err)
	}
	result, err := NewLocalService().ReadFileRange(file.Name(), offset, 4)
	if err != nil || string(result.Data) != "tail" || !result.EOF || result.NextOffsetBytes != offset+4 {
		t.Fatalf("sparse file range: %#v, %v", result, err)
	}
}
