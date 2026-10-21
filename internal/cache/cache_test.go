package cache

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "github.com", "done.json")
	t0 := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)

	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if s.IsDone("1", t0) {
		t.Error("IsDone() = true in an empty record")
	}
	s.MarkDone("1", t0)
	s.MarkDone("2", t0)
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}

	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if !s.IsDone("1", t0) {
		t.Error("IsDone() = false for a thread marked as done")
	}
	if s.IsDone("1", t0.Add(time.Second)) {
		t.Error("IsDone() = true for a thread with activity after it was marked as done")
	}

	s.Retain(func(id string) bool { return id == "1" })
	if s.IsDone("2", t0) {
		t.Error("IsDone() = true for a thread Retain dropped")
	}
}

func TestOpen(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}

	if _, err := Open(write("corrupt.json", "{")); err == nil {
		t.Error("Open() of a corrupt file succeeded, want an error")
	}

	s, err := Open(write("old.json", `{"version": 0, "threads": {"1": "2026-10-20T12:00:00Z"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if s.IsDone("1", time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)) {
		t.Error("IsDone() = true for a record of another version, want it ignored")
	}
}

func TestDefaultPath(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "/state")
	got, err := DefaultPath("github.com")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join("/state", "gh-inbox", "github.com", "done.json"); got != want {
		t.Errorf("DefaultPath() = %q, want %q", got, want)
	}
}
