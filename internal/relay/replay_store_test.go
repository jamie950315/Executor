package relay

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestFileReplayChildHelper(t *testing.T) {
	dir := os.Getenv("EXECUTOR_REPLAY_TEST_DIR")
	if dir == "" {
		return
	}
	store, err := NewFileReplayStore(dir, time.Now)
	if err != nil {
		os.Exit(2)
	}
	err = store.Consume(strings.Repeat("e", 64), time.Now().Add(time.Minute))
	store.Close()
	if err != nil {
		os.Exit(3)
	}
	os.Exit(0)
}

func TestFileReplayAcrossProcesses(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "replay")
	commands := make([]*exec.Cmd, 6)
	for i := range commands {
		commands[i] = exec.Command(os.Args[0], "-test.run=^TestFileReplayChildHelper$")
		commands[i].Env = append(os.Environ(), "EXECUTOR_REPLAY_TEST_DIR="+dir)
		if err := commands[i].Start(); err != nil {
			t.Fatal(err)
		}
	}
	winners := 0
	for _, cmd := range commands {
		err := cmd.Wait()
		if err == nil {
			winners++
		} else if cmd.ProcessState.ExitCode() != 3 {
			t.Fatalf("child store failed: %v", err)
		}
	}
	if winners != 1 {
		t.Fatalf("cross-process winners=%d", winners)
	}
}

func TestFileReplaySurvivesReopenAndRejectsConcurrentDuplicates(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "replay")
	now := time.Unix(1700000000, 0)
	store, err := NewFileReplayStore(dir, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	key := strings.Repeat("a", 64)
	if err := store.Consume(key, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	store.Close()
	store, err = NewFileReplayStore(dir, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Consume(key, now.Add(time.Minute)); err == nil {
		t.Fatal("replay accepted after reopen")
	}
	second, err := NewFileReplayStore(dir, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	var accepted atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			s := store
			if i%2 == 1 {
				s = second
			}
			if s.Consume(strings.Repeat("b", 64), now.Add(time.Minute)) == nil {
				accepted.Add(1)
			}
		}(i)
	}
	wg.Wait()
	if accepted.Load() != 1 {
		t.Fatalf("accepted %d duplicate requests", accepted.Load())
	}
}

func TestFileReplayExpiryAndFailClosedOnCorruption(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "replay")
	now := time.Unix(1700000000, 0)
	store, err := NewFileReplayStore(dir, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	key := strings.Repeat("a", 64)
	if err := store.Consume(key, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Second)
	if err := store.Consume(strings.Repeat("b", 64), now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, key)); !os.IsNotExist(err) {
		t.Fatal("expired nonce not pruned")
	}
	if err := os.WriteFile(filepath.Join(dir, strings.Repeat("c", 64)), []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := store.Consume(strings.Repeat("d", 64), now.Add(time.Minute)); err == nil {
		t.Fatal("corrupt replay storage accepted")
	}
}

func TestFileReplayRejectsInvalidKeysAndClosedStore(t *testing.T) {
	now := time.Now()
	store, err := NewFileReplayStore(filepath.Join(t.TempDir(), "replay"), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"", "../escape", strings.Repeat("A", 64)} {
		if store.Consume(key, now.Add(time.Minute)) == nil {
			t.Fatal("invalid key accepted")
		}
	}
	store.Close()
	if store.Consume(strings.Repeat("a", 64), now.Add(time.Minute)) == nil {
		t.Fatal("closed store accepted")
	}
}

func TestFileReplayRejectsUnsafeDirectoryAndSymlinkEntry(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permissions/symlink fixture")
	}
	dir := filepath.Join(t.TempDir(), "replay")
	if err := os.Mkdir(dir, 0777); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0777); err != nil {
		t.Fatal(err)
	}
	if _, err := NewFileReplayStore(dir, time.Now); err == nil {
		t.Fatal("unprotected replay directory accepted")
	}
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	store, err := NewFileReplayStore(dir, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := os.Symlink(".lock", filepath.Join(dir, strings.Repeat("a", 64))); err != nil {
		t.Fatal(err)
	}
	if store.Consume(strings.Repeat("b", 64), time.Now().Add(time.Minute)) == nil {
		t.Fatal("symlink entry accepted")
	}
}
