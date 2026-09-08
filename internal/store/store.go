package store

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"zonapropbot/internal/model"
)

// record is one JSONL line in seen.jsonl. Only ID and URL are required for
// deduplication; the rest aid debugging.
type record struct {
	ID      string    `json:"id"`
	URL     string    `json:"url"`
	AddedAt time.Time `json:"added_at"`
}

// Store persists the set of already-seen listing IDs in an append-only JSONL
// file, mirroring the role of seen.txt in the original Python bot. Each Add is
// fsynced so a crash mid-run cannot lose acknowledged IDs.
type Store struct {
	mu   sync.Mutex
	path string
	file *os.File
	seen map[string]struct{}
}

// Open loads (or creates) the store inside dir. A trailing corrupt line caused
// by a crash is tolerated; valid preceding lines are still loaded.
func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("store: create dir: %w", err)
	}
	path := filepath.Join(dir, "seen.jsonl")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("store: open %s: %w", path, err)
	}
	s := &Store{path: path, file: f, seen: make(map[string]struct{})}
	if err := s.load(); err != nil {
		f.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) load() error {
	scanner := bufio.NewScanner(s.file)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	var lastErr error
	for scanner.Scan() {
		var r record
		if err := json.Unmarshal(scanner.Bytes(), &r); err != nil {
			// Tolerate a truncated final line (crash mid-append).
			lastErr = err
			continue
		}
		if r.ID != "" {
			s.seen[r.ID] = struct{}{}
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("store: scan: %w", err)
	}
	_ = lastErr // non-fatal by design
	return nil
}

// Contains reports whether id has already been seen.
func (s *Store) Contains(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.seen[id]
	return ok
}

// Add records id as seen. Adding an already-seen id is a no-op. The record is
// fsynced before returning so it survives a crash.
func (s *Store) Add(l model.Listing) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.seen[l.ID]; ok {
		return nil
	}
	line, err := json.Marshal(record{ID: l.ID, URL: l.URL, AddedAt: time.Now().UTC()})
	if err != nil {
		return fmt.Errorf("store: marshal: %w", err)
	}
	line = append(line, '\n')
	if _, err := s.file.Write(line); err != nil {
		return fmt.Errorf("store: write: %w", err)
	}
	if err := s.file.Sync(); err != nil {
		return fmt.Errorf("store: sync: %w", err)
	}
	s.seen[l.ID] = struct{}{}
	return nil
}

// Close flushes and closes the underlying file.
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.file.Close()
}
