package cmd

import (
	"context"
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/maraino/gh-inbox/internal/inbox"
)

// terminal returns a change to the environment that makes it read keys from
// a terminal where keys are typed, and records the URLs opened in the
// browser.
func terminal(keys string, opened *[]string) func(*env) {
	return func(e *env) {
		e.openKeyboard = func(interrupt func()) (keyboard, error) {
			return &fakeKeyboard{keys: strings.NewReader(keys), interrupt: interrupt}, nil
		}
		e.openBrowser = func(url string) error {
			*opened = append(*opened, url)
			return nil
		}
	}
}

// fakeKeyboard types keys one at a time. Ctrl-C interrupts, as on a
// terminal.
type fakeKeyboard struct {
	keys      *strings.Reader
	interrupt func()
}

func (k *fakeKeyboard) ReadKey(ctx context.Context) (byte, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	b, err := k.keys.ReadByte()
	if err != nil {
		return 0, io.EOF
	}
	if b == keyCtrlC {
		k.interrupt()
		return 0, ctx.Err()
	}
	return b, nil
}

func (k *fakeKeyboard) Out(w io.Writer) io.Writer { return w }
func (k *fakeKeyboard) Close() error              { return nil }

func TestTriage(t *testing.T) {
	c := newFakeInbox()
	cacheDir := t.TempDir()
	var opened []string
	// Open and mark as done the merged pull request, after a key that does
	// nothing; skip the open one; mark the CI run as read; quit.
	got := runEnv(t, c, cacheDir, terminal("xods rq", &opened), "triage")
	if got.err != nil {
		t.Fatal(got.err)
	}
	if want := []string{"done 1", "read 3"}; !slices.Equal(c.actions, want) {
		t.Errorf("actions = %v, want %v", c.actions, want)
	}
	if want := []string{"https://github.com/acme/api/pull/1"}; !slices.Equal(opened, want) {
		t.Errorf("opened %v, want %v", opened, want)
	}
	for _, want := range []string{
		"(1/4) acme/api #1 · pr · merged · review_requested · 1h ago\nMerged fix\nhttps://github.com/acme/api/pull/1\n",
		triagePrompt + "opened\n" + triagePrompt + "marked as done\n",
		"(3/4) acme/api · ci · ci_activity · 10d ago\nCI failed\nhttps://github.com/acme/api/actions\n",
		triagePrompt + "quit\n",
		"\nMarked 1 notification as done.\nMarked 1 notification as read.\nLeft 2 notifications untouched.\n",
	} {
		if !strings.Contains(got.stdout, want) {
			t.Errorf("stdout doesn't contain %q:\n%s", want, got.stdout)
		}
	}

	// The thread marked as done is left out the next time.
	got = run(t, c, cacheDir, "list")
	if strings.Contains(got.stdout, "Merged fix") {
		t.Errorf("list shows the thread triage marked as done:\n%s", got.stdout)
	}
}

func TestTriageFailure(t *testing.T) {
	c := newFakeInbox()
	c.fail = map[string]bool{"1": true}
	var opened []string
	// Marking as done fails, so it stays on the notification; the keys run
	// out after skipping it.
	got := runEnv(t, c, t.TempDir(), terminal("ds", &opened), "triage", "--repo", "acme/api")
	if got.err != nil {
		t.Fatal(got.err)
	}
	if len(c.actions) != 0 {
		t.Errorf("actions = %v, want none", c.actions)
	}
	for _, want := range []string{
		triagePrompt + "failed: boom\n" + triagePrompt + "skipped\n",
		triagePrompt + "quit\n",
		"Left 2 notifications untouched.\n",
	} {
		if !strings.Contains(got.stdout, want) {
			t.Errorf("stdout doesn't contain %q:\n%s", want, got.stdout)
		}
	}
}

func TestTriageNeedsTerminal(t *testing.T) {
	if got := run(t, newFakeInbox(), t.TempDir(), "triage"); got.err == nil {
		t.Error("triage without a terminal succeeded, want an error")
	}
}

func TestWebURL(t *testing.T) {
	o := &rootOptions{hostname: "github.com"}
	tests := []struct {
		n    inbox.Notification
		want string
	}{
		{inbox.Notification{Repo: "acme/api", URL: "https://github.com/acme/api/pull/1"}, "https://github.com/acme/api/pull/1"},
		{inbox.Notification{Repo: "acme/api", Type: inbox.TypeCheckSuite}, "https://github.com/acme/api/actions"},
		{
			inbox.Notification{Repo: "acme/api", Type: inbox.TypeCommit, SubjectURL: "https://api.github.com/repos/acme/api/commits/abc123"},
			"https://github.com/acme/api/commit/abc123",
		},
		{inbox.Notification{Repo: "acme/api", Type: inbox.TypeCommit}, "https://github.com/acme/api"},
	}
	for _, tt := range tests {
		if got := o.webURL(tt.n); got != tt.want {
			t.Errorf("webURL(%+v) = %q, want %q", tt.n, got, tt.want)
		}
	}
}

func TestTriagePrevious(t *testing.T) {
	c := newFakeInbox()
	cacheDir := t.TempDir()
	var opened []string
	// Going back from the first does nothing. Mark the merged pull request
	// as done, go back to it, mark it as read, then quit.
	got := runEnv(t, c, cacheDir, terminal("pdprq", &opened), "triage")
	if got.err != nil {
		t.Fatal(got.err)
	}
	if want := []string{"done 1", "read 1"}; !slices.Equal(c.actions, want) {
		t.Errorf("actions = %v, want %v", c.actions, want)
	}
	for _, want := range []string{
		triagePrompt + "previous\nThis is the first one.\n\n(1/4) ",
		"marked as done\n\n(2/4) ",
		triagePrompt + "previous\n\n(1/4) acme/api #1 · pr · merged · review_requested · 1h ago\nMerged fix\n" +
			"https://github.com/acme/api/pull/1\nAlready marked as done.\n" + triagePrompt + "marked as read\n",
		"\nMarked 1 notification as done.\nMarked 1 notification as read.\nLeft 3 notifications untouched.\n",
	} {
		if !strings.Contains(got.stdout, want) {
			t.Errorf("stdout doesn't contain %q:\n%s", want, got.stdout)
		}
	}

	// It was marked as done, so it's left out the next time.
	c.actions = nil
	got = runEnv(t, c, cacheDir, terminal("q", &opened), "triage")
	if !strings.Contains(got.stdout, "(1/3) acme/web #2") {
		t.Errorf("the thread marked as done wasn't left out:\n%s", got.stdout)
	}
}

func TestTriageInterrupt(t *testing.T) {
	c := newFakeInbox()
	var opened []string
	got := runEnv(t, c, t.TempDir(), terminal("s\x03d", &opened), "triage")
	if got.err != nil {
		t.Fatal(got.err)
	}
	if len(c.actions) != 0 {
		t.Errorf("actions = %v, want none", c.actions)
	}
	if want := "(2/4) acme/web #2"; !strings.Contains(got.stdout, want) || !strings.Contains(got.stdout, triagePrompt+"quit\n") {
		t.Errorf("stdout doesn't quit at notification 2:\n%s", got.stdout)
	}
}

func TestTriageRules(t *testing.T) {
	c := newFakeInbox()
	var opened []string
	got := runEnv(t, c, t.TempDir(), terminal("rq", &opened), "triage", "--rule", "stale-ci", "--rules-file", writeRules(t, testRules))
	if got.err != nil {
		t.Fatal(got.err)
	}
	if want := []string{"read 3"}; !slices.Equal(c.actions, want) {
		t.Errorf("actions = %v, want %v", c.actions, want)
	}
	if !strings.Contains(got.stdout, "(1/1) acme/api · ci") {
		t.Errorf("stdout doesn't show only the CI run:\n%s", got.stdout)
	}

	got = runEnv(t, c, t.TempDir(), terminal("q", &opened), "triage", "--rule", "stale-ci", "--type", "pr")
	if got.err == nil || !strings.Contains(got.err.Error(), "can't be combined") {
		t.Errorf("error = %v, want rules and filters refused", got.err)
	}
}

func TestTriageSort(t *testing.T) {
	c := newFakeInbox()
	var opened []string
	// Mark as done the first one, which is the CI run when sorted by type.
	got := runEnv(t, c, t.TempDir(), terminal("dq", &opened), "triage", "--sort", "type")
	if got.err != nil {
		t.Fatal(got.err)
	}
	if want := []string{"done 3"}; !slices.Equal(c.actions, want) {
		t.Errorf("actions = %v, want %v", c.actions, want)
	}
	if want := "(1/4) acme/api · ci · ci_activity"; !strings.Contains(got.stdout, want) {
		t.Errorf("stdout doesn't contain %q:\n%s", want, got.stdout)
	}
}
