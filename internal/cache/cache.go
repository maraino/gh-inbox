// Package cache keeps a local record of the threads marked as done.
//
// The notifications API keeps listing a thread after it's marked as done,
// until there's new activity on it, and it has no way to tell such a thread
// apart from a read one. The record lets later runs skip those threads. It's
// only an optimization: without it, threads are marked as done again, which
// is harmless.
package cache

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// version is the version of the file format.
const version = 1

// Store is the record of the threads marked as done. It isn't safe for
// concurrent use.
type Store struct {
	path    string
	threads map[string]time.Time
}

type file struct {
	Version int `json:"version"`
	// Threads maps thread IDs to the time of their latest activity when
	// they were marked as done.
	Threads map[string]time.Time `json:"threads"`
}

// DefaultPath returns where the record for a host is kept:
// $XDG_STATE_HOME/gh-inbox/HOST/done.json, or
// ~/.local/state/gh-inbox/HOST/done.json.
func DefaultPath(host string) (string, error) {
	return XDGPath("XDG_STATE_HOME", filepath.Join(".local", "state"), "gh-inbox", host, "done.json")
}

// XDGPath joins elem to the directory in the environment variable xdgVar,
// or to fallback under the home directory when it isn't set.
func XDGPath(xdgVar, fallback string, elem ...string) (string, error) {
	dir := os.Getenv(xdgVar)
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		dir = filepath.Join(home, fallback)
	}
	return filepath.Join(append([]string{dir}, elem...)...), nil
}

// Open reads the record at path. A missing file is an empty record.
func Open(path string) (*Store, error) {
	s := &Store{path: path, threads: make(map[string]time.Time)}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	var f file
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	if f.Version != version {
		// A record from another version is only an optimization lost.
		return s, nil
	}
	if f.Threads != nil {
		s.threads = f.Threads
	}
	return s, nil
}

// IsDone reports whether the thread was marked as done and has had no
// activity since.
func (s *Store) IsDone(threadID string, updatedAt time.Time) bool {
	doneAt, ok := s.threads[threadID]
	return ok && !updatedAt.After(doneAt)
}

// MarkDone records that the thread was marked as done when its latest
// activity was at updatedAt.
func (s *Store) MarkDone(threadID string, updatedAt time.Time) {
	s.threads[threadID] = updatedAt
}

// Retain forgets the threads for which keep returns false, such as threads
// no longer in the inbox.
func (s *Store) Retain(keep func(threadID string) bool) {
	for id := range s.threads {
		if !keep(id) {
			delete(s.threads, id)
		}
	}
}

// Save writes the record. It replaces the file atomically, so a failed save
// leaves the previous record intact.
func (s *Store) Save() error {
	data, err := json.Marshal(file{Version: version, Threads: s.threads})
	if err != nil {
		return err
	}
	return WriteFileAtomic(s.path, data)
}

// WriteFileAtomic replaces the file at path with data, creating its
// directory if needed. A failed write leaves the previous file intact.
func WriteFileAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+"-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
