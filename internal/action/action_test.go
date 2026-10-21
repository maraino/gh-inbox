package action

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/maraino/gh-inbox/internal/inbox"
)

// fakeClient records the actions applied, and fails for the threads in
// fail.
type fakeClient struct {
	mu    sync.Mutex
	calls []string
	fail  map[string]bool

	inFlight, maxInFlight atomic.Int32
}

func (c *fakeClient) record(action, threadID string) error {
	n := c.inFlight.Add(1)
	defer c.inFlight.Add(-1)
	for {
		m := c.maxInFlight.Load()
		if n <= m || c.maxInFlight.CompareAndSwap(m, n) {
			break
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, action+" "+threadID)
	if c.fail[threadID] {
		return errors.New("failed")
	}
	return nil
}

func (c *fakeClient) MarkDone(_ context.Context, id string) error { return c.record("done", id) }
func (c *fakeClient) MarkRead(_ context.Context, id string) error { return c.record("read", id) }
func (c *fakeClient) Unsubscribe(_ context.Context, id string) error {
	return c.record("unsubscribe", id)
}

func notifications(n int) []inbox.Notification {
	ns := make([]inbox.Notification, n)
	for i := range ns {
		ns[i].ID = strconv.Itoa(i + 1)
	}
	return ns
}

func ids(ns []inbox.Notification) []string {
	out := make([]string, len(ns))
	for i, n := range ns {
		out[i] = n.ID
	}
	return out
}

func TestRun(t *testing.T) {
	client := &fakeClient{fail: map[string]bool{"2": true, "4": true}}
	var progress []int
	r := Runner{
		Client:      client,
		Kind:        Done,
		Concurrency: 3,
		Progress:    func(finished, _ int) { progress = append(progress, finished) },
	}

	succeeded, failed := r.Run(t.Context(), notifications(6))

	if got, want := ids(succeeded), []string{"1", "3", "5", "6"}; !slices.Equal(got, want) {
		t.Errorf("succeeded = %v, want %v", got, want)
	}
	var failedIDs []string
	for _, f := range failed {
		failedIDs = append(failedIDs, f.Notification.ID)
	}
	if want := []string{"2", "4"}; !slices.Equal(failedIDs, want) {
		t.Errorf("failed = %v, want %v", failedIDs, want)
	}
	if len(client.calls) != 6 {
		t.Errorf("made %d calls, want 6", len(client.calls))
	}
	if want := []int{1, 2, 3, 4, 5, 6}; !slices.Equal(progress, want) {
		t.Errorf("progress = %v, want %v", progress, want)
	}
	if n := client.maxInFlight.Load(); n > 3 {
		t.Errorf("%d calls ran at the same time, want at most 3", n)
	}
}

func TestRunKinds(t *testing.T) {
	for _, kind := range []Kind{Done, Read, Unsubscribe} {
		client := &fakeClient{}
		r := Runner{Client: client, Kind: kind}
		r.Run(t.Context(), notifications(1))
		if want := []string{string(kind) + " 1"}; !slices.Equal(client.calls, want) {
			t.Errorf("%s: calls = %v, want %v", kind, client.calls, want)
		}
	}
}

func TestRunCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	client := &fakeClient{}
	r := Runner{Client: client, Kind: Done}

	succeeded, failed := r.Run(ctx, notifications(3))

	if len(succeeded) != 0 || len(failed) != 3 || len(client.calls) != 0 {
		t.Errorf("Run() with a canceled context: %d succeeded, %d failed, %d calls; want 0, 3, 0",
			len(succeeded), len(failed), len(client.calls))
	}
	for _, f := range failed {
		if !errors.Is(f.Err, context.Canceled) {
			t.Errorf("failure error = %v, want context.Canceled", f.Err)
		}
	}
}

func TestParseKind(t *testing.T) {
	for _, name := range []string{"done", "read", "unsubscribe"} {
		if _, err := ParseKind(name); err != nil {
			t.Errorf("ParseKind(%q) error = %v", name, err)
		}
	}
	if _, err := ParseKind("delete"); err == nil {
		t.Error("ParseKind(\"delete\") succeeded, want an error")
	}
}
