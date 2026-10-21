package filter

import (
	"testing"
	"time"

	"github.com/maraino/gh-inbox/internal/inbox"
)

var (
	now = time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	me  = inbox.Identity{Login: "me", Teams: []string{"acme/backend"}}
)

// notifications used by the tests, by name.
var notifications = map[string]inbox.Notification{
	"merged pr for me": {
		Repo: "acme/api", Type: inbox.TypePullRequest, Reason: "review_requested", Unread: true,
		UpdatedAt: now.Add(-48 * time.Hour),
		Subject: &inbox.Subject{
			State: inbox.StateMerged, Author: "alice", Assignees: []string{"Me"}, ReviewUsers: []string{"bob"},
		},
	},
	"open pr for my team": {
		Repo: "acme/web", Type: inbox.TypePullRequest, Reason: "team_mention",
		UpdatedAt: now.Add(-time.Hour),
		Subject: &inbox.Subject{
			State: inbox.StateOpen, Author: "bob", ReviewTeams: []string{"acme/backend"},
		},
	},
	"closed issue": {
		Repo: "other/tool", Type: inbox.TypeIssue, Reason: "subscribed",
		UpdatedAt: now.Add(-30 * 24 * time.Hour),
		Subject:   &inbox.Subject{State: inbox.StateClosed, Author: "me"},
	},
	"unreadable pr": {
		Repo: "acme/gone", Type: inbox.TypePullRequest, Reason: "review_requested",
		UpdatedAt: now.Add(-time.Hour),
	},
	"ci run": {
		Repo: "acme/api", Type: inbox.TypeCheckSuite, Reason: "ci_activity", Unread: true,
		UpdatedAt: now.Add(-10 * 24 * time.Hour),
	},
}

func TestMatch(t *testing.T) {
	tests := []struct {
		name string
		opts Options
		want []string
	}{
		{
			name: "no filters",
			want: []string{"merged pr for me", "open pr for my team", "closed issue", "unreadable pr", "ci run"},
		},
		{
			name: "repo wildcard",
			opts: Options{Repos: []string{"ACME/*"}},
			want: []string{"merged pr for me", "open pr for my team", "unreadable pr", "ci run"},
		},
		{
			name: "repos are ORed",
			opts: Options{Repos: []string{"acme/web", "other/tool"}},
			want: []string{"open pr for my team", "closed issue"},
		},
		{
			name: "type",
			opts: Options{Types: []string{"ci", "issue"}},
			want: []string{"closed issue", "ci run"},
		},
		{
			name: "reason",
			opts: Options{Reasons: []string{"review_requested"}},
			want: []string{"merged pr for me", "unreadable pr"},
		},
		{
			name: "state skips subjects without details",
			opts: Options{States: []string{"merged", "closed"}},
			want: []string{"merged pr for me", "closed issue"},
		},
		{
			name: "unread",
			opts: Options{Unread: true},
			want: []string{"merged pr for me", "ci run"},
		},
		{
			name: "read",
			opts: Options{Read: true},
			want: []string{"open pr for my team", "closed issue", "unreadable pr"},
		},
		{
			name: "older than",
			opts: Options{OlderThan: 7 * 24 * time.Hour},
			want: []string{"closed issue", "ci run"},
		},
		{
			name: "author",
			opts: Options{Authors: []string{"@me"}},
			want: []string{"closed issue"},
		},
		{
			name: "not author",
			opts: Options{NotAuthors: []string{"alice", "bob"}},
			want: []string{"closed issue"},
		},
		{
			name: "assigned to me ignores case",
			opts: Options{AssignedTo: []string{"@me"}},
			want: []string{"merged pr for me"},
		},
		{
			name: "not assigned to me",
			opts: Options{NotAssignedTo: []string{"@me"}},
			want: []string{"open pr for my team", "closed issue"},
		},
		{
			name: "review requested from me includes my teams",
			opts: Options{ReviewRequested: []string{"@me"}},
			want: []string{"open pr for my team"},
		},
		{
			name: "review requested from a team",
			opts: Options{ReviewRequested: []string{"acme/backend"}},
			want: []string{"open pr for my team"},
		},
		{
			name: "review requested from a user",
			opts: Options{ReviewRequested: []string{"bob"}},
			want: []string{"merged pr for me"},
		},
		{
			name: "no review requested from me",
			opts: Options{NoReviewRequested: []string{"@me"}},
			want: []string{"merged pr for me", "closed issue"},
		},
		{
			name: "involves me",
			opts: Options{Involves: []string{"@me"}},
			want: []string{"merged pr for me", "open pr for my team"},
		},
		{
			name: "not involves me",
			opts: Options{NotInvolves: []string{"@me"}},
			want: []string{"closed issue"},
		},
		{
			name: "filters are ANDed",
			opts: Options{Repos: []string{"acme/*"}, States: []string{"open", "merged"}, NotAssignedTo: []string{"@me"}},
			want: []string{"open pr for my team"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			set, err := New(&tt.opts, Env{Now: now, Me: me})
			if err != nil {
				t.Fatal(err)
			}
			want := make(map[string]bool)
			for _, name := range tt.want {
				want[name] = true
			}
			for name, n := range notifications {
				if got := set.Match(n); got != want[name] {
					t.Errorf("Match(%q) = %v, want %v", name, got, want[name])
				}
			}
		})
	}
}

func TestNeedsDetails(t *testing.T) {
	tests := []struct {
		opts Options
		want bool
	}{
		{opts: Options{}, want: false},
		{opts: Options{Repos: []string{"o/r"}, Types: []string{"pr"}, Unread: true}, want: false},
		{opts: Options{States: []string{"open"}}, want: true},
		{opts: Options{NotInvolves: []string{"alice"}}, want: true},
	}
	for _, tt := range tests {
		set, err := New(&tt.opts, Env{Now: now, Me: me})
		if err != nil {
			t.Fatal(err)
		}
		if got := set.NeedsDetails(); got != tt.want {
			t.Errorf("NeedsDetails() with %+v = %v, want %v", tt.opts, got, tt.want)
		}
	}
}

func TestMatchBasic(t *testing.T) {
	set, err := New(&Options{Repos: []string{"acme/api"}, States: []string{"open"}}, Env{Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if !set.MatchBasic(notifications["merged pr for me"]) {
		t.Error("MatchBasic() = false, want true: only the repo filter applies")
	}
	if set.Match(notifications["merged pr for me"]) {
		t.Error("Match() = true, want false: the state doesn't match")
	}
}

func TestNewErrors(t *testing.T) {
	tests := []struct {
		name string
		opts Options
		env  Env
	}{
		{name: "bad repo pattern", opts: Options{Repos: []string{"acme/[x"}}},
		{name: "repo without owner", opts: Options{Repos: []string{"api"}}},
		{name: "unknown type", opts: Options{Types: []string{"PullRequest"}}},
		{name: "unknown reason", opts: Options{Reasons: []string{"review-requested"}}},
		{name: "unknown state", opts: Options{States: []string{"draft"}}},
		{name: "read and unread", opts: Options{Read: true, Unread: true}},
		{name: "negative age", opts: Options{OlderThan: -time.Hour}},
		{name: "team as assignee", opts: Options{AssignedTo: []string{"acme/backend"}}},
		{name: "team as author", opts: Options{Authors: []string{"acme/backend"}}},
		{name: "invalid team", opts: Options{ReviewRequested: []string{"acme/"}}},
		{name: "empty user", opts: Options{Involves: []string{""}}},
		{name: "@me without identity", opts: Options{Involves: []string{"@me"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := New(&tt.opts, tt.env); err == nil {
				t.Error("New() succeeded, want an error")
			}
		})
	}
}

func TestOptions(t *testing.T) {
	if o := (Options{}); !o.IsEmpty() {
		t.Error("IsEmpty() = false for no filters")
	}
	if o := (Options{NotInvolves: []string{"alice"}}); o.IsEmpty() {
		t.Error("IsEmpty() = true with a filter on people")
	}
	if o := (Options{ReviewRequested: []string{"alice"}}); o.NeedsIdentity() {
		t.Error("NeedsIdentity() = true without @me")
	}
	if o := (Options{Authors: []string{"@ME"}}); !o.NeedsIdentity() {
		t.Error("NeedsIdentity() = false with @ME")
	}
}
