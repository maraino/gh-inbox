package github

import (
	"encoding/json"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/maraino/gh-inbox/internal/inbox"
)

func TestNotifications(t *testing.T) {
	pages := map[string]string{
		"1": `[{
			"id": "1", "unread": true, "reason": "review_requested", "updated_at": "2026-10-01T10:00:00Z",
			"subject": {"title": "Fix it", "url": "https://api.github.com/repos/o/r/pulls/7", "type": "PullRequest"},
			"repository": {"full_name": "o/r"}
		}]`,
		"2": `[{
			"id": "2", "unread": false, "reason": "ci_activity", "updated_at": "2026-10-02T10:00:00Z",
			"subject": {"title": "CI failed", "url": "", "type": "CheckSuite"},
			"repository": {"full_name": "o/r"}
		}]`,
	}
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/notifications" || r.URL.Query().Get("all") != "true" {
			t.Errorf("unexpected request %s", r.URL)
		}
		page := r.URL.Query().Get("page")
		if page == "" {
			page = "1"
			w.Header().Set("Link", `<https://api.github.com/notifications?all=true&page=2>; rel="next"`)
		}
		w.Write([]byte(pages[page]))
	}))

	got, err := c.Notifications(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	want := []inbox.Notification{
		{
			ID: "1", Repo: "o/r", Type: inbox.TypePullRequest, Title: "Fix it", Reason: "review_requested",
			Unread: true, UpdatedAt: time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC),
			SubjectURL: "https://api.github.com/repos/o/r/pulls/7",
		},
		{
			ID: "2", Repo: "o/r", Type: inbox.TypeCheckSuite, Title: "CI failed", Reason: "ci_activity",
			UpdatedAt: time.Date(2026, 10, 2, 10, 0, 0, 0, time.UTC),
		},
	}
	if !slices.EqualFunc(got, want, func(a, b inbox.Notification) bool {
		return a.ID == b.ID && a.Repo == b.Repo && a.Type == b.Type && a.Title == b.Title &&
			a.Reason == b.Reason && a.Unread == b.Unread && a.UpdatedAt.Equal(b.UpdatedAt) &&
			a.SubjectURL == b.SubjectURL
	}) {
		gotJSON, _ := json.Marshal(got)
		t.Errorf("Notifications() = %s", gotJSON)
	}
}

func TestViewer(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/user":
			w.Write([]byte(`{"login": "me"}`))
		case r.URL.Path == "/user/teams" && r.URL.Query().Get("page") == "":
			w.Header().Set("Link", `<https://api.github.com/user/teams?page=2>; rel="next"`)
			w.Write([]byte(`[{"slug": "backend", "organization": {"login": "acme"}}]`))
		case r.URL.Path == "/user/teams":
			w.Write([]byte(`[{"slug": "security", "organization": {"login": "acme"}}]`))
		default:
			http.NotFound(w, r)
		}
	}))

	got, err := c.Viewer(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if got.Login != "me" || !slices.Equal(got.Teams, []string{"acme/backend", "acme/security"}) {
		t.Errorf("Viewer() = %+v", got)
	}
}
