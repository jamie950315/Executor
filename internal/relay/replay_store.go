package relay

import (
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

const maximumReplayEntries = 4096

var ErrReplayStorage = errors.New("Hub replay storage unavailable, full, corrupt or already consumed")

// FileReplayStore stores only hashed nonce identifiers and expiration times.
// Use a dedicated protected local state directory, never a shared/network FS.
type FileReplayStore struct {
	mu     sync.Mutex
	root   *os.Root
	lock   *os.File
	now    func() time.Time
	closed bool
}

func NewFileReplayStore(directory string, now func() time.Time) (*FileReplayStore, error) {
	if now == nil {
		now = time.Now
	}
	if err := os.Mkdir(directory, 0700); err != nil && !os.IsExist(err) {
		return nil, replayInitError("mkdir", err)
	}
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, replayInitError("directory-type", err)
	}
	if !privateReplayDirectory(info) {
		return nil, replayInitError("directory-mode", nil)
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, replayInitError("open-root", err)
	}
	if info, err := root.Lstat(".lock"); err == nil && !info.Mode().IsRegular() {
		root.Close()
		return nil, replayInitError("lock-type", nil)
	} else if err != nil && !os.IsNotExist(err) {
		root.Close()
		return nil, replayInitError("lock-stat", err)
	}
	lock, err := openHubLockFile(root, ".lock")
	if err != nil {
		root.Close()
		return nil, replayInitError("lock-open", err)
	}
	return &FileReplayStore{root: root, lock: lock, now: now}, nil
}

// Use an exclusive creator and a separate existing-file opener. This avoids
// concurrent O_CREATE resolution on a just-created directory entry while
// retaining rooted access and refusing symbolic-link lock files.
func openHubLockFile(root *os.Root, name string) (*os.File, error) {
	file, err := root.OpenFile(name, os.O_RDWR|os.O_CREATE|os.O_EXCL|os.O_SYNC, 0600)
	if err == nil {
		return file, nil
	}
	if !os.IsExist(err) {
		return nil, err
	}
	info, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, ErrReplayStorage
	}
	file, err = root.OpenFile(name, os.O_RDWR|os.O_SYNC, 0)
	if err != nil {
		return nil, err
	}
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		file.Close()
		return nil, ErrReplayStorage
	}
	return file, nil
}

func replayInitError(stage string, err error) error {
	// Report a syscall reason without leaking local paths or request data.
	var pathError *os.PathError
	if errors.As(err, &pathError) {
		err = pathError.Err
	}
	return fmt.Errorf("%w: %s (%v)", ErrReplayStorage, stage, err)
}

func validReplayKey(key string) bool {
	decoded, err := hex.DecodeString(key)
	return err == nil && len(decoded) == 32 && key == strings.ToLower(key)
}

func (s *FileReplayStore) Consume(key string, until time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	if s.closed || !validReplayKey(key) || until.Unix() <= now.Unix() || until.After(now.Add(65*time.Second)) {
		return ErrReplayStorage
	}
	release, err := lockReplayFile(s.lock)
	if err != nil {
		return ErrReplayStorage
	}
	defer release()
	dir, err := s.root.Open(".")
	if err != nil {
		return ErrReplayStorage
	}
	entries, err := dir.ReadDir(maximumReplayEntries + 2)
	dir.Close()
	if err != nil && err != io.EOF || len(entries) > maximumReplayEntries+1 {
		return ErrReplayStorage
	}
	count := 0
	for _, entry := range entries {
		name := entry.Name()
		if name == ".lock" {
			continue
		}
		if !validReplayKey(name) || !entry.Type().IsRegular() {
			return ErrReplayStorage
		}
		file, err := s.root.Open(name)
		if err != nil {
			return ErrReplayStorage
		}
		data, err := io.ReadAll(io.LimitReader(file, 32))
		file.Close()
		if err != nil || len(data) >= 32 {
			return ErrReplayStorage
		}
		expiry, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
		if err != nil || expiry <= 0 {
			return ErrReplayStorage
		}
		if expiry <= now.Unix() {
			if err := s.root.Remove(name); err != nil {
				return ErrReplayStorage
			}
			continue
		}
		if name == key {
			return ErrReplayStorage
		}
		count++
	}
	if count >= maximumReplayEntries {
		return ErrReplayStorage
	}
	file, err := s.root.OpenFile(key, os.O_CREATE|os.O_EXCL|os.O_WRONLY|os.O_SYNC, 0600)
	if err != nil {
		return ErrReplayStorage
	}
	_, writeErr := file.WriteString(strconv.FormatInt(until.Unix(), 10) + "\n")
	syncErr := file.Sync()
	closeErr := file.Close()
	// Leave a failed/partial record in place: ambiguous storage must fail closed.
	if writeErr != nil || syncErr != nil || closeErr != nil {
		return ErrReplayStorage
	}
	if err := syncReplayDirectory(s.root); err != nil {
		return ErrReplayStorage
	}
	return nil
}

func (s *FileReplayStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	a := s.lock.Close()
	b := s.root.Close()
	return errors.Join(a, b)
}
