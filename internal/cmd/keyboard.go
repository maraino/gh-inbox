package cmd

import (
	"bytes"
	"context"
	"io"
	"os"
	"sync"

	xterm "golang.org/x/term"
)

// keyboard reads key presses from a terminal.
type keyboard interface {
	// ReadKey waits for a key press. Keys pressed before it's called, such
	// as during a request, are ignored. It returns io.EOF when the input
	// ends, and ctx's error when ctx is done.
	ReadKey(ctx context.Context) (byte, error)
	// Out returns the writer to use instead of w while the keyboard is
	// open.
	Out(w io.Writer) io.Writer
	// Close gives the terminal back.
	Close() error
}

// Keys that quit besides q. In raw mode, Ctrl-C and Ctrl-D don't send
// signals.
const (
	keyCtrlC = 3
	keyCtrlD = 4
)

// openTerminalKeyboard puts the terminal on stdin in raw mode, so that keys
// are read without waiting for Enter and aren't shown. Pressing Ctrl-C calls
// interrupt, even while no key is awaited.
func openTerminalKeyboard(interrupt func()) (keyboard, error) {
	fd := int(os.Stdin.Fd())
	state, err := xterm.MakeRaw(fd)
	if err != nil {
		return nil, err
	}
	k := newKeyReader(os.Stdin, interrupt)
	k.restore = func() error { return xterm.Restore(fd, state) }
	return k, nil
}

// keyReader reads keys from r in the background, so that Ctrl-C can
// interrupt a request and keys pressed during one can be told apart from
// those answering a prompt.
type keyReader struct {
	keys    chan byte
	done    chan struct{}
	err     error // set before done is closed
	restore func() error
	once    sync.Once
}

func newKeyReader(r io.Reader, interrupt func()) *keyReader {
	k := &keyReader{keys: make(chan byte, 16), done: make(chan struct{})}
	go k.read(r, interrupt)
	return k
}

func (k *keyReader) read(r io.Reader, interrupt func()) {
	buf := make([]byte, 64)
	for {
		n, err := r.Read(buf)
		if key, ok := keyIn(buf[:n]); ok {
			if key == keyCtrlC {
				interrupt()
			}
			select {
			case k.keys <- key:
			default: // Nobody is reading them.
			}
		}
		if err != nil {
			k.err = err
			close(k.done)
			return
		}
	}
}

// keyIn returns the key pressed in what one read from a terminal returned.
// A key press is a single byte; more are an escape sequence, such as an
// arrow key or Alt with a key, or pasted text. Only Ctrl-C counts in them.
func keyIn(b []byte) (byte, bool) {
	switch {
	case len(b) == 1:
		return b[0], true
	case bytes.IndexByte(b, keyCtrlC) >= 0:
		return keyCtrlC, true
	default:
		return 0, false
	}
}

func (k *keyReader) ReadKey(ctx context.Context) (byte, error) {
	for drained := false; !drained; {
		select {
		case <-k.keys:
		default:
			drained = true
		}
	}
	select {
	case key := <-k.keys:
		return key, nil
	case <-k.done:
		return 0, k.err
	case <-ctx.Done():
		return 0, ctx.Err()
	}
}

// Out returns w with line feeds turned into the carriage return and line
// feed that a terminal in raw mode needs.
func (k *keyReader) Out(w io.Writer) io.Writer {
	return crlfWriter{w}
}

func (k *keyReader) Close() error {
	var err error
	k.once.Do(func() {
		if k.restore != nil {
			err = k.restore()
		}
	})
	return err
}

type crlfWriter struct {
	w io.Writer
}

func (c crlfWriter) Write(p []byte) (int, error) {
	if _, err := c.w.Write(bytes.ReplaceAll(p, []byte("\n"), []byte("\r\n"))); err != nil {
		return 0, err
	}
	return len(p), nil
}
