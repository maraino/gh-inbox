package github

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/maraino/gh-inbox/internal/inbox"
)

func TestParseSubjectRef(t *testing.T) {
	tests := []struct {
		url    string
		want   subjectRef
		wantOK bool
	}{
		{
			url:    "https://api.github.com/repos/o/r/pulls/7",
			want:   subjectRef{url: "https://api.github.com/repos/o/r/pulls/7", owner: "o", repo: "r", num: 7, isPR: true},
			wantOK: true,
		},
		{
			url:    "https://ghe.example.com/api/v3/repos/o/r/issues/8",
			want:   subjectRef{url: "https://ghe.example.com/api/v3/repos/o/r/issues/8", owner: "o", repo: "r", num: 8},
			wantOK: true,
		},
		{url: "https://api.github.com/repos/o/r/releases/1"},
		{url: "https://api.github.com/repos/o/r/pulls/x"},
		{url: "https://api.github.com/repos/o/r/pulls/7/files"},
		{url: ""},
	}
	for _, tt := range tests {
		got, ok := parseSubjectRef(tt.url)
		if ok != tt.wantOK || got != tt.want {
			t.Errorf("parseSubjectRef(%q) = %+v, %v, want %+v, %v", tt.url, got, ok, tt.want, tt.wantOK)
		}
	}
}

func TestSubjectWebURL(t *testing.T) {
	tests := map[string]string{
		"https://api.github.com/repos/o/r/pulls/7":          "https://github.com/o/r/pull/7",
		"https://api.github.com/repos/o/r/issues/8":         "https://github.com/o/r/issues/8",
		"https://api.acme.ghe.com/repos/o/r/pulls/7":        "https://acme.ghe.com/o/r/pull/7",
		"https://ghe.example.com/api/v3/repos/o/r/issues/8": "https://ghe.example.com/o/r/issues/8",
		"https://api.github.com/repos/o/r/releases/1":       "",
		"": "",
	}
	for apiURL, want := range tests {
		if got := subjectWebURL(apiURL); got != want {
			t.Errorf("subjectWebURL(%q) = %q, want %q", apiURL, got, want)
		}
	}
}

// graphQLRequest is the body of a GraphQL request.
type graphQLRequest struct {
	Query     string         `json:"query"`
	Variables map[string]any `json:"variables"`
}

func TestSubjectDetails(t *testing.T) {
	notifications := []inbox.Notification{
		{ID: "1", Type: inbox.TypePullRequest, SubjectURL: "https://api.github.com/repos/o/r/pulls/7"},
		{ID: "2", Type: inbox.TypeIssue, SubjectURL: "https://api.github.com/repos/o/r/issues/8"},
		{ID: "3", Type: inbox.TypePullRequest, SubjectURL: "https://api.github.com/repos/o/gone/pulls/9"},
		// Duplicates and subjects without details aren't looked up.
		{ID: "4", Type: inbox.TypePullRequest, SubjectURL: "https://api.github.com/repos/o/r/pulls/7"},
		{ID: "5", Type: inbox.TypeRelease, SubjectURL: "https://api.github.com/repos/o/r/releases/1"},
	}
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req graphQLRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		if len(req.Variables) != 9 {
			t.Errorf("query has %d variables, want 9 (3 subjects)", len(req.Variables))
		}
		if !strings.Contains(req.Query, "fragment pr ") || !strings.Contains(req.Query, "fragment issue ") {
			t.Errorf("query lacks a fragment it uses:\n%s", req.Query)
		}
		if !strings.Contains(req.Query, "... on Team") {
			t.Errorf("query doesn't look up review teams:\n%s", req.Query)
		}
		w.Write([]byte(`{
			"data": {
				"s0": {"pullRequest": {
					"number": 7, "state": "MERGED", "url": "https://github.com/o/r/pull/7",
					"author": {"login": "alice"},
					"assignees": {"nodes": [{"login": "bob"}]},
					"reviewRequests": {"nodes": [
						{"requestedReviewer": {"__typename": "User", "login": "carol"}},
						{"requestedReviewer": {"__typename": "Team", "slug": "backend", "organization": {"login": "o"}}},
						{"requestedReviewer": null}
					]}
				}},
				"s1": {"issue": {
					"number": 8, "state": "OPEN", "url": "https://github.com/o/r/issues/8",
					"author": null, "assignees": {"nodes": []}
				}},
				"s2": null
			},
			"errors": [{"type": "NOT_FOUND", "path": ["s2"], "message": "Could not resolve to a Repository"}]
		}`))
	}))

	got, err := c.SubjectDetails(t.Context(), notifications, DetailsOptions{ReviewTeams: true})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]*inbox.Subject{
		"https://api.github.com/repos/o/r/pulls/7": {
			Number: 7, State: inbox.StateMerged, URL: "https://github.com/o/r/pull/7", Author: "alice",
			Assignees: []string{"bob"}, ReviewUsers: []string{"carol"}, ReviewTeams: []string{"o/backend"},
		},
		"https://api.github.com/repos/o/r/issues/8": {
			Number: 8, State: inbox.StateOpen, URL: "https://github.com/o/r/issues/8",
		},
	}
	if !reflect.DeepEqual(got, want) {
		gotJSON, _ := json.MarshalIndent(got, "", "  ")
		t.Errorf("SubjectDetails() = %s", gotJSON)
	}
}

func TestToSubjectWithoutTeams(t *testing.T) {
	// Without the team fields, a team reviewer only has its type.
	var s apiSubject
	if err := json.Unmarshal([]byte(`{
		"number": 7, "state": "OPEN",
		"reviewRequests": {"nodes": [
			{"requestedReviewer": {"__typename": "User", "login": "carol"}},
			{"requestedReviewer": {"__typename": "Team"}}
		]}
	}`), &s); err != nil {
		t.Fatal(err)
	}
	got := s.toSubject()
	if !reflect.DeepEqual(got.ReviewUsers, []string{"carol"}) || got.ReviewTeams != nil {
		t.Errorf("toSubject() review users, teams = %q, %q, want [carol], none", got.ReviewUsers, got.ReviewTeams)
	}
}

func TestSubjectDetailsBatches(t *testing.T) {
	const total = 2*detailsBatchSize + 3
	var notifications []inbox.Notification
	for i := range total {
		notifications = append(notifications, inbox.Notification{
			Type:       inbox.TypeIssue,
			SubjectURL: fmt.Sprintf("https://api.github.com/repos/o/r/issues/%d", i+1),
		})
	}
	var queries atomic.Int32
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		queries.Add(1)
		var req graphQLRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		// GitHub rejects queries with unused fragments.
		if strings.Contains(req.Query, "... on Team") {
			t.Errorf("query looks up review teams without being asked to:\n%s", req.Query)
		}
		if strings.Contains(req.Query, "fragment pr ") {
			t.Errorf("query of issues only defines the pull request fragment:\n%s", req.Query)
		}
		data := make(map[string]any)
		for name, v := range req.Variables {
			if i, ok := strings.CutPrefix(name, "n"); ok {
				data["s"+i] = map[string]any{"issue": map[string]any{"number": v, "state": "CLOSED"}}
			}
		}
		json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))

	got, err := c.SubjectDetails(t.Context(), notifications, DetailsOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != total {
		t.Errorf("got details for %d subjects, want %d", len(got), total)
	}
	if n := queries.Load(); n != 3 {
		t.Errorf("made %d queries, want 3", n)
	}
}

func TestSubjectDetailsFailures(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		wantErr error
	}{
		{
			name: "unexpected GraphQL error",
			body: `{"data": {"s0": null}, "errors": [{"type": "INTERNAL", "message": "Something went wrong"}]}`,
		},
		{
			name: "rate limited",
			body: `{"errors": [{"type": "RATE_LIMITED", "message": "API rate limit exceeded"}]}`,
		},
		{
			name:    "missing read:org",
			body:    `{"data": {"s0": null}, "errors": [{"type": "INSUFFICIENT_SCOPES", "message": "The 'slug' field requires read:org"}]}`,
			wantErr: ErrMissingReadOrg,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Write([]byte(tt.body))
			}))
			notifications := []inbox.Notification{
				{Type: inbox.TypePullRequest, SubjectURL: "https://api.github.com/repos/o/r/pulls/7"},
			}
			_, err := c.SubjectDetails(t.Context(), notifications, DetailsOptions{ReviewTeams: true})
			if err == nil {
				t.Fatal("SubjectDetails() succeeded, want an error")
			}
			if tt.wantErr != nil && !errors.Is(err, tt.wantErr) {
				t.Errorf("SubjectDetails() error = %v, want %v", err, tt.wantErr)
			}
		})
	}
}
