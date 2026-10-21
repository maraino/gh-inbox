package cmd

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/maraino/gh-inbox/internal/github"
	"github.com/maraino/gh-inbox/internal/inbox"
)

var now = time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)

// fakeClient serves a fixed inbox and records the actions applied to it.
type fakeClient struct {
	notifications []inbox.Notification
	// details are the pull requests and issues that can be read, by
	// subject URL.
	details map[string]*inbox.Subject
	me      inbox.Identity
	// fail are the thread IDs that actions fail for.
	fail map[string]bool

	mu      sync.Mutex
	actions []string
}

func (c *fakeClient) Notifications(context.Context) ([]inbox.Notification, error) {
	return slices.Clone(c.notifications), nil
}

func (c *fakeClient) SubjectDetails(_ context.Context, ns []inbox.Notification, _ github.DetailsOptions) (map[string]*inbox.Subject, error) {
	found := make(map[string]*inbox.Subject)
	for _, n := range ns {
		if s, ok := c.details[n.SubjectURL]; ok {
			found[n.SubjectURL] = s
		}
	}
	return found, nil
}

func (c *fakeClient) Viewer(context.Context) (inbox.Identity, error) {
	if c.me.Login == "" {
		return inbox.Identity{}, errors.New("no viewer")
	}
	return c.me, nil
}

func (c *fakeClient) act(action, id string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.fail[id] {
		return errors.New("boom")
	}
	c.actions = append(c.actions, action+" "+id)
	return nil
}

func (c *fakeClient) MarkDone(_ context.Context, id string) error    { return c.act("done", id) }
func (c *fakeClient) MarkRead(_ context.Context, id string) error    { return c.act("read", id) }
func (c *fakeClient) Unsubscribe(_ context.Context, id string) error { return c.act("unsubscribe", id) }

// newFakeInbox returns a client with an inbox of four notifications:
//
//  1. a merged pull request assigned to me
//  2. an open pull request awaiting my team's review
//  3. a CI run
//  4. a pull request that can't be read
func newFakeInbox() *fakeClient {
	pr := func(id, repo, title, url string, updated time.Duration) inbox.Notification {
		return inbox.Notification{
			ID: id, Repo: repo, Type: inbox.TypePullRequest, Title: title, Reason: "review_requested",
			UpdatedAt: now.Add(-updated), SubjectURL: url,
		}
	}
	return &fakeClient{
		notifications: []inbox.Notification{
			pr("1", "acme/api", "Merged fix", "https://api.github.com/repos/acme/api/pulls/1", time.Hour),
			pr("2", "acme/web", "Open feature", "https://api.github.com/repos/acme/web/pulls/2", 2*time.Hour),
			{
				ID: "3", Repo: "acme/api", Type: inbox.TypeCheckSuite, Title: "CI failed", Reason: "ci_activity",
				Unread: true, UpdatedAt: now.Add(-10 * 24 * time.Hour),
			},
			pr("4", "acme/gone", "Lost PR", "https://api.github.com/repos/acme/gone/pulls/4", 3*time.Hour),
		},
		details: map[string]*inbox.Subject{
			"https://api.github.com/repos/acme/api/pulls/1": {Number: 1, State: inbox.StateMerged, Assignees: []string{"me"}},
			"https://api.github.com/repos/acme/web/pulls/2": {Number: 2, State: inbox.StateOpen, ReviewTeams: []string{"acme/backend"}},
		},
		me: inbox.Identity{Login: "me", Teams: []string{"acme/backend"}},
	}
}

type result struct {
	stdout, stderr string
	err            error
}

// run runs the command line with args against the fake client. The record
// of threads marked as done lives in cacheDir.
func run(t *testing.T, c *fakeClient, cacheDir string, args ...string) result {
	t.Helper()
	var stdout, stderr bytes.Buffer
	e := &env{
		out:       &stdout,
		errOut:    &stderr,
		now:       func() time.Time { return now },
		newClient: func(github.Options) (client, error) { return c, nil },
		cachePath: func(host string) (string, error) {
			return filepath.Join(cacheDir, host, "done.json"), nil
		},
	}
	cmd := newRootCmd(e)
	cmd.SetArgs(append([]string{"--hostname", "github.com"}, args...))
	err := cmd.ExecuteContext(t.Context())
	return result{stdout: stdout.String(), stderr: stderr.String(), err: err}
}

func TestList(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		wantStdout string
		wantStderr string
	}{
		{
			name: "everything",
			args: []string{"list"},
			wantStdout: "acme/api\tpr\t-\treview_requested\t2026-10-20T11:00:00Z\tMerged fix\n" +
				"acme/web\tpr\t-\treview_requested\t2026-10-20T10:00:00Z\tOpen feature\n" +
				"acme/api\tci\t-\tci_activity\t2026-10-10T12:00:00Z\tCI failed\n" +
				"acme/gone\tpr\t-\treview_requested\t2026-10-20T09:00:00Z\tLost PR\n",
		},
		{
			name:       "filter on the notification",
			args:       []string{"list", "--type", "ci", "--older-than", "7d"},
			wantStdout: "acme/api\tci\t-\tci_activity\t2026-10-10T12:00:00Z\tCI failed\n",
		},
		{
			name:       "filter on details reports unreadable subjects",
			args:       []string{"list", "--state", "merged,closed"},
			wantStdout: "acme/api\tpr\tmerged\treview_requested\t2026-10-20T11:00:00Z\tMerged fix\n",
			wantStderr: "\nSkipped 1 notification whose pull request or issue couldn't be read:\n  acme/gone: Lost PR\n",
		},
		{
			name:       "filter on people with @me",
			args:       []string{"list", "--review-requested", "@me"},
			wantStdout: "acme/web\tpr\topen\treview_requested\t2026-10-20T10:00:00Z\tOpen feature\n",
			wantStderr: "\nSkipped 1 notification whose pull request or issue couldn't be read:\n  acme/gone: Lost PR\n",
		},
		{
			name:       "limit",
			args:       []string{"list", "--limit", "1"},
			wantStdout: "acme/api\tpr\t-\treview_requested\t2026-10-20T11:00:00Z\tMerged fix\n",
		},
		{
			name:       "nothing matches",
			args:       []string{"list", "--repo", "other/*"},
			wantStderr: "No notifications match.\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := run(t, newFakeInbox(), t.TempDir(), tt.args...)
			if got.err != nil {
				t.Fatal(got.err)
			}
			if got.stdout != tt.wantStdout {
				t.Errorf("stdout =\n%s\nwant\n%s", got.stdout, tt.wantStdout)
			}
			if got.stderr != tt.wantStderr {
				t.Errorf("stderr = %q, want %q", got.stderr, tt.wantStderr)
			}
		})
	}
}

func TestListFormats(t *testing.T) {
	got := run(t, newFakeInbox(), t.TempDir(), "list", "--summary", "--type", "ci")
	if got.err != nil {
		t.Fatal(got.err)
	}
	if want := "1 notification\n"; !bytes.HasPrefix([]byte(got.stdout), []byte(want)) {
		t.Errorf("--summary output doesn't start with %q:\n%s", want, got.stdout)
	}

	got = run(t, newFakeInbox(), t.TempDir(), "list", "--json", "--type", "ci")
	if got.err != nil {
		t.Fatal(got.err)
	}
	if want := `"title": "CI failed"`; !bytes.Contains([]byte(got.stdout), []byte(want)) {
		t.Errorf("--json output doesn't contain %q:\n%s", want, got.stdout)
	}

	if got := run(t, newFakeInbox(), t.TempDir(), "list", "--json", "--summary"); got.err == nil {
		t.Error("--json with --summary succeeded, want an error")
	}
}

func TestListErrors(t *testing.T) {
	for _, args := range [][]string{
		{"list", "--type", "pull"},
		{"list", "--older-than", "soon"},
		{"list", "--limit", "-1"},
		{"list", "--assigned-to", "acme/backend"},
		{"list", "extra"},
	} {
		if got := run(t, newFakeInbox(), t.TempDir(), args...); got.err == nil {
			t.Errorf("%v succeeded, want an error", args)
		}
	}
}

func TestParseAge(t *testing.T) {
	tests := []struct {
		in      string
		want    time.Duration
		wantErr bool
	}{
		{in: "36h", want: 36 * time.Hour},
		{in: "90m", want: 90 * time.Minute},
		{in: "7d", want: 7 * 24 * time.Hour},
		{in: "2w", want: 14 * 24 * time.Hour},
		{in: "0d", wantErr: true},
		{in: "-1h", wantErr: true},
		{in: "1.5d", wantErr: true},
		{in: "d", wantErr: true},
		{in: "soon", wantErr: true},
	}
	for _, tt := range tests {
		got, err := parseAge(tt.in)
		if (err != nil) != tt.wantErr || got != tt.want {
			t.Errorf("parseAge(%q) = %v, %v, want %v, error %v", tt.in, got, err, tt.want, tt.wantErr)
		}
	}
}
