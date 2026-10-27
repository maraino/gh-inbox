package github

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/maraino/gh-inbox/internal/inbox"
)

// withCache makes c cache in a temporary directory, at a time tests can
// move.
func withCache(t *testing.T, c *Client) *time.Time {
	t.Helper()
	c.cacheDir = t.TempDir()
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	c.now = func() time.Time { return now }
	return &now
}

func TestNotificationsCache(t *testing.T) {
	const lastModified = "Mon, 20 Oct 2026 11:00:00 GMT"
	var requests, conditional atomic.Int32
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusResetContent)
			return
		}
		requests.Add(1)
		if r.Header.Get("If-Modified-Since") == lastModified {
			conditional.Add(1)
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("Last-Modified", lastModified)
		w.Write([]byte(`[{"id": "1", "updated_at": "2026-10-20T10:00:00Z", "unread": true,
			"subject": {"title": "Fix", "type": "PullRequest", "url": "https://api.github.com/repos/o/r/pulls/7"},
			"repository": {"full_name": "o/r"}}]`))
	}))
	now := withCache(t, c)

	list := func() []inbox.Notification {
		t.Helper()
		got, err := c.Notifications(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	first := list()
	if len(first) != 1 || first[0].URL != "https://github.com/o/r/pull/7" {
		t.Fatalf("Notifications() = %+v", first)
	}

	// Nothing new: the API answers 304 and the list comes from the cache.
	if got := list(); !reflect.DeepEqual(got, first) || conditional.Load() != 1 {
		t.Errorf("second list = %+v, with %d conditional requests, want the first list and 1", got, conditional.Load())
	}

	// A change to a thread forgets the list.
	if err := c.MarkRead(t.Context(), "1"); err != nil {
		t.Fatal(err)
	}
	list()
	if conditional.Load() != 1 {
		t.Errorf("listing after marking as read made a conditional request")
	}

	// An old list isn't reused, even if the API says nothing is new.
	*now = now.Add(listMaxAge + time.Minute)
	list()
	if conditional.Load() != 1 || requests.Load() != 4 {
		t.Errorf("listing with an old cache made %d conditional requests of %d, want 1 of 4", conditional.Load(), requests.Load())
	}

	// Refreshing ignores the cached list, but still forgets it after a
	// change, so that a later run doesn't reuse it.
	c.refresh = true
	list()
	if conditional.Load() != 1 {
		t.Errorf("listing while refreshing made a conditional request")
	}
	if err := c.MarkRead(t.Context(), "1"); err != nil {
		t.Fatal(err)
	}
	c.refresh = false
	list()
	if conditional.Load() != 1 {
		t.Errorf("listing after a change made while refreshing made a conditional request")
	}
}

func TestNotificationsCacheUnreadable(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Last-Modified", "Mon, 20 Oct 2026 11:00:00 GMT")
		w.Write([]byte(`[]`))
	}))
	withCache(t, c)
	if err := os.WriteFile(filepath.Join(c.cacheDir, listCacheFile), []byte("garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Notifications(t.Context()); err != nil {
		t.Fatalf("a broken cache made listing fail: %v", err)
	}
}

func TestSubjectDetailsCache(t *testing.T) {
	var queries atomic.Int32
	var lastQuery atomic.Value
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		queries.Add(1)
		var req graphQLRequest
		if err := decodeJSON(r, &req); err != nil {
			t.Error(err)
		}
		lastQuery.Store(req.Query)
		// Answer for whichever subjects are asked: s0 is a pull request,
		// and o/gone can't be read.
		data := map[string]any{}
		for k, v := range req.Variables {
			if !strings.HasPrefix(k, "r") {
				continue
			}
			alias := "s" + k[1:]
			if v == "gone" {
				data[alias] = nil
				continue
			}
			data[alias] = map[string]any{"pullRequest": map[string]any{"number": 7, "state": "OPEN"}}
		}
		writeJSON(w, map[string]any{"data": data})
	}))
	now := withCache(t, c)

	updated := time.Date(2026, 10, 20, 10, 0, 0, 0, time.UTC)
	notifications := []inbox.Notification{
		{ID: "1", Type: inbox.TypePullRequest, SubjectURL: "https://api.github.com/repos/o/r/pulls/7", UpdatedAt: updated},
		{ID: "2", Type: inbox.TypePullRequest, SubjectURL: "https://api.github.com/repos/o/gone/pulls/9", UpdatedAt: updated},
	}
	lookUp := func(opts DetailsOptions) map[string]*inbox.Subject {
		t.Helper()
		got, err := c.SubjectDetails(t.Context(), notifications, opts)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}

	first := lookUp(DetailsOptions{})
	if len(first) != 1 || queries.Load() != 1 {
		t.Fatalf("first lookup = %v after %d queries, want 1 subject after 1", first, queries.Load())
	}

	// Without new activity, both come from the cache, including the one
	// that can't be read.
	if got := lookUp(DetailsOptions{}); !reflect.DeepEqual(got, first) || queries.Load() != 1 {
		t.Errorf("second lookup = %v after %d queries, want the first after 1", got, queries.Load())
	}

	// Wanting the teams looks them up again.
	lookUp(DetailsOptions{ReviewTeams: true})
	if queries.Load() != 2 || !strings.Contains(lastQuery.Load().(string), "... on Team") {
		t.Errorf("looking up teams made %d queries, want 2 with teams", queries.Load())
	}
	// Then they're cached, which also serves a lookup without teams.
	lookUp(DetailsOptions{})
	if queries.Load() != 2 {
		t.Errorf("made %d queries, want 2", queries.Load())
	}

	// New activity on one subject looks up only that one.
	notifications[0].UpdatedAt = updated.Add(time.Minute)
	lookUp(DetailsOptions{})
	if q := lastQuery.Load().(string); queries.Load() != 3 || strings.Count(q, "repository(") != 1 {
		t.Errorf("after new activity, made %d queries, the last for %d subjects, want 3 and 1", queries.Load(), strings.Count(q, "repository("))
	}

	// Old details aren't reused.
	*now = now.Add(detailsMaxAge + time.Minute)
	lookUp(DetailsOptions{})
	if q := lastQuery.Load().(string); queries.Load() != 4 || strings.Count(q, "repository(") != 2 {
		t.Errorf("with old details, made %d queries, the last for %d subjects, want 4 and 2", queries.Load(), strings.Count(q, "repository("))
	}
}

func decodeJSON(r *http.Request, v any) error {
	return json.NewDecoder(r.Body).Decode(v)
}

func writeJSON(w http.ResponseWriter, v any) {
	_ = json.NewEncoder(w).Encode(v) // The test fails on what's missing.
}
