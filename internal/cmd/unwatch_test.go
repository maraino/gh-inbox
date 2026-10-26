package cmd

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/maraino/gh-inbox/internal/inbox"
)

// newWatchingInbox returns the fake inbox with notifications from watched
// repositories: two from acme/infra, one from acme/docs and one from
// other/lib.
func newWatchingInbox() *fakeClient {
	c := newFakeInbox()
	for i, repo := range []string{"acme/infra", "acme/infra", "acme/docs", "other/lib"} {
		c.notifications = append(c.notifications, inbox.Notification{
			ID: "w" + string(rune('1'+i)), Repo: repo, Type: inbox.TypeRelease, Title: "Release",
			Reason: "subscribed", UpdatedAt: now.Add(-time.Duration(i+1) * time.Hour),
		})
	}
	return c
}

func TestUnwatchList(t *testing.T) {
	got := run(t, newWatchingInbox(), t.TempDir(), "unwatch")
	if got.err != nil {
		t.Fatal(got.err)
	}
	want := "4 notifications come from 3 repositories you watch:\n\n" +
		"  2  acme/infra\n  1  acme/docs\n  1  other/lib\n\n" +
		"Run \"gh inbox unwatch owner/name... --apply\" to stop watching some of them.\n"
	if got.stdout != want {
		t.Errorf("stdout =\n%s\nwant\n%s", got.stdout, want)
	}

	got = run(t, newFakeInbox(), t.TempDir(), "unwatch")
	if got.err != nil || got.stdout != "" || got.stderr != "No notifications come from repositories you watch.\n" {
		t.Errorf("without watched repositories: %+v", got)
	}
}

func TestUnwatchDryRun(t *testing.T) {
	c := newWatchingInbox()
	got := run(t, c, t.TempDir(), "unwatch", "acme/*", "new/repo", "ACME/infra")
	if got.err != nil {
		t.Fatal(got.err)
	}
	if len(c.actions) != 0 {
		t.Errorf("a dry run applied actions: %v", c.actions)
	}
	want := "Would stop watching 3 repositories:\n\n  2  acme/infra\n  1  acme/docs\n  0  new/repo\n\nRun again with --apply to do it.\n"
	if got.stdout != want {
		t.Errorf("stdout =\n%s\nwant\n%s", got.stdout, want)
	}
}

func TestUnwatchApply(t *testing.T) {
	c := newWatchingInbox()
	c.fail = map[string]bool{"other/lib": true}
	got := run(t, c, t.TempDir(), "unwatch", "--apply", "acme/*", "other/lib")
	if got.err == nil || got.err.Error() != "1 of 3 failed" {
		t.Errorf("error = %v, want 1 of 3 failed", got.err)
	}
	slices.Sort(c.actions)
	if want := []string{"unwatch acme/docs", "unwatch acme/infra"}; !slices.Equal(c.actions, want) {
		t.Errorf("actions = %v, want %v", c.actions, want)
	}
	for _, want := range []string{
		"Stopped watching acme/infra.\nStopped watching acme/docs.\n",
		"gh inbox clean --repo acme/infra,acme/docs,other/lib --reason subscribed --apply\n",
	} {
		if !strings.Contains(got.stdout, want) {
			t.Errorf("stdout doesn't contain %q:\n%s", want, got.stdout)
		}
	}
	if want := "Failed to stop watching other/lib: boom\n"; got.stderr != want {
		t.Errorf("stderr = %q, want %q", got.stderr, want)
	}
}

func TestUnwatchErrors(t *testing.T) {
	for _, tt := range []struct {
		args []string
		want string
	}{
		{[]string{"--apply"}, "name the repositories"},
		{[]string{"acme"}, "invalid repository"},
		{[]string{"acme/"}, "invalid repository"},
		{[]string{"a/b/c"}, "invalid repository"},
		{[]string{"nope/*"}, "matches no repository"},
	} {
		c := newWatchingInbox()
		got := run(t, c, t.TempDir(), append([]string{"unwatch"}, tt.args...)...)
		if got.err == nil || !strings.Contains(got.err.Error(), tt.want) {
			t.Errorf("%v: error = %v, want it to contain %q", tt.args, got.err, tt.want)
		}
		if len(c.actions) != 0 {
			t.Errorf("%v: applied actions: %v", tt.args, c.actions)
		}
	}
}
