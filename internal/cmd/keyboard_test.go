package cmd

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
	"time"
)

func TestKeyIn(t *testing.T) {
	tests := []struct {
		in     string
		want   byte
		wantOK bool
	}{
		{in: "d", want: 'd', wantOK: true},
		{in: "\x03", want: keyCtrlC, wantOK: true},
		{in: ""},
		// Alt+u, an arrow key, and pasted text aren't keys.
		{in: "\x1bu"},
		{in: "\x1b[A"},
		{in: "dddd"},
		// Ctrl-C counts anywhere.
		{in: "ab\x03", want: keyCtrlC, wantOK: true},
	}
	for _, tt := range tests {
		got, ok := keyIn([]byte(tt.in))
		if got != tt.want || ok != tt.wantOK {
			t.Errorf("keyIn(%q) = %q, %v, want %q, %v", tt.in, got, ok, tt.want, tt.wantOK)
		}
	}
}

// chunkReader returns one chunk per read, waiting for each on a channel.
type chunkReader chan string

func (r chunkReader) Read(p []byte) (int, error) {
	s, ok := <-r
	if !ok {
		return 0, io.EOF
	}
	return copy(p, s), nil
}

func TestKeyReader(t *testing.T) {
	chunks := make(chunkReader)
	interrupted := make(chan struct{}, 1)
	k := newKeyReader(chunks, func() { interrupted <- struct{}{} })
	ctx := t.Context()

	// Keys pressed while nothing waits are dropped when a key is awaited.
	chunks <- "d"
	waitFor(t, func() bool { return len(k.keys) == 1 })
	read := make(chan byte)
	go func() {
		key, err := k.ReadKey(ctx)
		if err != nil {
			t.Error(err)
		}
		read <- key
	}()
	waitFor(t, func() bool { return len(k.keys) == 0 }) // ReadKey dropped the d.
	chunks <- "\x1bu"
	chunks <- "s"
	if key := <-read; key != 's' {
		t.Errorf("ReadKey() = %q, want s", key)
	}

	// Ctrl-C interrupts even when no key is awaited.
	chunks <- "\x03"
	<-interrupted

	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := k.ReadKey(canceled); !errors.Is(err, context.Canceled) {
		t.Errorf("ReadKey() with a canceled context = %v", err)
	}

	close(chunks)
	if _, err := k.ReadKey(ctx); !errors.Is(err, io.EOF) {
		t.Errorf("ReadKey() at the end = %v, want EOF", err)
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	for range 1000 {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("timed out")
}

func TestCRLFWriter(t *testing.T) {
	var buf bytes.Buffer
	n, err := crlfWriter{&buf}.Write([]byte("a\nb\n"))
	if err != nil || n != 4 || buf.String() != "a\r\nb\r\n" {
		t.Errorf("Write() = %d, %v, wrote %q", n, err, buf.String())
	}
}
