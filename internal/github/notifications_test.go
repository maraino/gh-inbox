package github

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/maraino/gh-inbox/internal/inbox"
)

func TestNotifications(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/notifications" || r.URL.Query().Get("all") != "true" {
			t.Errorf("unexpected request %s", r.URL)
		}
		w.Write([]byte(`[{
			"id": "1", "unread": true, "reason": "review_requested", "updated_at": "2026-10-01T10:00:00Z",
			"subject": {"title": "Fix it", "url": "https://api.github.com/repos/o/r/pulls/7", "type": "PullRequest"},
			"repository": {"full_name": "o/r"}
		}]`))
	}))

	got, err := c.Notifications(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	want := inbox.Notification{
		ID: "1", Repo: "o/r", Type: inbox.TypePullRequest, Title: "Fix it", Reason: "review_requested",
		Unread: true, UpdatedAt: time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC),
		SubjectURL: "https://api.github.com/repos/o/r/pulls/7", Number: 7,
		URL: "https://github.com/o/r/pull/7",
	}
	if len(got) != 1 || got[0].ID != want.ID || got[0].Repo != want.Repo || got[0].Type != want.Type ||
		got[0].Title != want.Title || got[0].Reason != want.Reason || got[0].Unread != want.Unread ||
		!got[0].UpdatedAt.Equal(want.UpdatedAt) || got[0].SubjectURL != want.SubjectURL ||
		got[0].Number != want.Number || got[0].URL != want.URL {
		t.Errorf("Notifications() = %+v, want [%+v]", got, want)
	}
}

// pagedInbox serves pages of notifications with IDs given by pages. With
// withLast, the links say which page is the last one.
func pagedInbox(t *testing.T, pages [][]string, withLast bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page := 1
		if p := r.URL.Query().Get("page"); p != "" {
			var err error
			if page, err = strconv.Atoi(p); err != nil || page < 1 || page > len(pages) {
				t.Errorf("unexpected page %q", p)
				http.NotFound(w, r)
				return
			}
		}
		link := func(rel string, page int) string {
			return fmt.Sprintf(`<https://api.github.com/notifications?all=true&per_page=50&page=%d>; rel="%s"`, page, rel)
		}
		var links []string
		if page < len(pages) {
			links = append(links, link("next", page+1))
			if withLast {
				links = append(links, link("last", len(pages)))
			}
		}
		if len(links) > 0 {
			w.Header().Set("Link", strings.Join(links, ", "))
		}
		fmt.Fprint(w, "[")
		for i, id := range pages[page-1] {
			if i > 0 {
				fmt.Fprint(w, ",")
			}
			fmt.Fprintf(w, `{"id": %q}`, id)
		}
		fmt.Fprint(w, "]")
	})
}

func TestNotificationsPages(t *testing.T) {
	pages := [][]string{{"1", "2"}, {"3", "4"}, {"4", "5"}, {"6"}}
	want := []string{"1", "2", "3", "4", "5", "6"}
	for _, withLast := range []bool{true, false} {
		t.Run(fmt.Sprintf("withLast=%v", withLast), func(t *testing.T) {
			c, _ := newTestClient(t, pagedInbox(t, pages, withLast))
			got, err := c.Notifications(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			ids := make([]string, len(got))
			for i, n := range got {
				ids[i] = n.ID
			}
			if !slices.Equal(ids, want) {
				t.Errorf("Notifications() IDs = %v, want %v, in order and without duplicates", ids, want)
			}
		})
	}
}

func TestPageURLs(t *testing.T) {
	const base = "https://api.github.com/notifications?all=true&per_page=50&page="
	tests := []struct {
		name   string
		links  map[string]string
		want   []string
		wantOK bool
	}{
		{name: "one page", links: map[string]string{}, wantOK: true},
		{
			name:  "three pages",
			links: map[string]string{"next": base + "2", "last": base + "3"},
			// The query keys come out sorted.
			want: []string{
				"https://api.github.com/notifications?all=true&page=2&per_page=50",
				"https://api.github.com/notifications?all=true&page=3&per_page=50",
			},
			wantOK: true,
		},
		{name: "no last", links: map[string]string{"next": base + "2"}},
		{name: "unexpected next", links: map[string]string{"next": base + "3", "last": base + "5"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := pageURLs(tt.links)
			if ok != tt.wantOK || !slices.Equal(got, tt.want) {
				t.Errorf("pageURLs() = %v, %v, want %v, %v", got, ok, tt.want, tt.wantOK)
			}
		})
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

func TestViewerWithoutReadOrg(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/user" {
			w.Write([]byte(`{"login": "me"}`))
			return
		}
		w.WriteHeader(http.StatusForbidden)
	}))
	if _, err := c.Viewer(t.Context()); !errors.Is(err, errMissingReadOrg) {
		t.Errorf("Viewer() error = %v, want %v", err, errMissingReadOrg)
	}
}
