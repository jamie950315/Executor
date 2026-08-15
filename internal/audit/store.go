package audit

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type Event struct {
	Time      time.Time `json:"time"`
	Actor     string    `json:"actor,omitempty"`
	Tool      string    `json:"tool"`
	SessionID string    `json:"session_id,omitempty"`
	Identity  string    `json:"identity,omitempty"`
	CWD       string    `json:"cwd,omitempty"`
	Outcome   string    `json:"outcome,omitempty"`
	Detail    string    `json:"detail,omitempty"`
}

type Store struct {
	path      string
	retention time.Duration
	mu        sync.Mutex
}

func Open(path string, retention time.Duration) (*Store, error) {
	if retention <= 0 {
		return nil, errors.New("retention must be positive")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	if err := file.Close(); err != nil {
		return nil, err
	}
	_ = os.Chmod(path, 0o600)
	return &Store{path: path, retention: retention}, nil
}

func (s *Store) Append(event Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if event.Time.IsZero() {
		event.Time = time.Now().UTC()
	}
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(s.path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = file.Write(append(data, '\n'))
	return err
}

func (s *Store) List(limit int) ([]Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	events, err := s.read()
	if err != nil {
		return nil, err
	}
	if limit > 0 && len(events) > limit {
		events = events[len(events)-limit:]
	}
	return events, nil
}

func (s *Store) Prune(now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	events, err := s.read()
	if err != nil {
		return err
	}
	cutoff := now.Add(-s.retention)
	kept := events[:0]
	for _, event := range events {
		if !event.Time.Before(cutoff) {
			kept = append(kept, event)
		}
	}
	tmp := s.path + ".tmp"
	file, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(file)
	for _, event := range kept {
		if err := enc.Encode(event); err != nil {
			_ = file.Close()
			_ = os.Remove(tmp)
			return err
		}
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, s.path)
}

func (s *Store) read() ([]Event, error) {
	file, err := os.Open(s.path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	var events []Event
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var event Event
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, scanner.Err()
}
