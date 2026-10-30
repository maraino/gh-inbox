// Command fakeapi serves a made-up inbox for recording the demo. It answers
// the API requests gh inbox makes for the host github.localhost, which the
// GitHub client sends over plain HTTP to api.github.localhost; point
// HTTP_PROXY at it to receive them.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
)

type thread struct {
	id        string
	repo      string
	kind      string // PullRequest, Issue, CheckSuite or Release
	num       int
	title     string
	reason    string
	age       time.Duration
	unread    bool
	state     string // OPEN, CLOSED or MERGED
	draft     bool
	author    string
	assignees []string
	reviewers []string // users, or org/team
	labels    []string
}

const (
	hour = time.Hour
	day  = 24 * time.Hour
)

var threads = []thread{
	{repo: "acme/api", kind: "PullRequest", num: 412, title: "Add rate limiting to the public endpoints", reason: "review_requested", age: 2 * hour, unread: true, state: "OPEN", author: "lin", reviewers: []string{"acme/backend"}, labels: []string{"enhancement"}},
	{repo: "acme/api", kind: "PullRequest", num: 409, title: "Retry webhooks with exponential backoff", reason: "review_requested", age: 5 * hour, unread: true, state: "OPEN", author: "sam", reviewers: []string{"mona"}},
	{repo: "acme/api", kind: "Issue", num: 401, title: "500 error when the token has expired", reason: "assign", age: 1 * day, unread: true, state: "OPEN", author: "jo", assignees: []string{"mona"}, labels: []string{"bug"}},
	{repo: "acme/mobile", kind: "PullRequest", num: 540, title: "build(deps): bump okhttp from 4.12.0 to 5.0.1", reason: "review_requested", age: 1*day + 3*hour, state: "OPEN", author: "dependabot", reviewers: []string{"acme/backend"}, labels: []string{"dependencies"}},
	{repo: "acme/web", kind: "PullRequest", num: 1287, title: "Dark mode for the settings page", reason: "review_requested", age: 1*day + 6*hour, state: "OPEN", draft: true, author: "ari", reviewers: []string{"acme/frontend"}},
	{repo: "acme/api", kind: "PullRequest", num: 398, title: "Drop the v1 tokens endpoint", reason: "author", age: 2 * day, state: "MERGED", author: "mona"},
	{repo: "acme/infra", kind: "PullRequest", num: 77, title: "Move CI runners to arm64", reason: "subscribed", age: 3 * day, state: "MERGED", author: "kai"},
	{repo: "acme/mobile", kind: "Issue", num: 533, title: "Crash on launch on Android 15", reason: "mention", age: 3*day + 4*hour, state: "CLOSED", author: "ren"},
	{repo: "acme/web", kind: "PullRequest", num: 1270, title: "Fix flaky login test on Safari", reason: "mention", age: 4 * day, state: "CLOSED", author: "ari"},
	{repo: "acme/cli", kind: "PullRequest", num: 215, title: "Support config profiles", reason: "subscribed", age: 5 * day, state: "OPEN", draft: true, author: "lin"},
	{repo: "acme/docs", kind: "PullRequest", num: 156, title: "Document the new rate limits", reason: "review_requested", age: 6 * day, state: "OPEN", author: "jo", reviewers: []string{"acme/platform"}},
	{repo: "acme/web", kind: "Issue", num: 1279, title: "Search ignores accents", reason: "subscribed", age: 8 * day, state: "OPEN", author: "ren"},
	{repo: "acme/infra", kind: "PullRequest", num: 81, title: "Terraform: split the staging state", reason: "subscribed", age: 9 * day, state: "CLOSED", author: "kai"},
	{repo: "acme/api", kind: "CheckSuite", title: "CI workflow run failed for main branch", reason: "ci_activity", age: 10 * day},
	{repo: "acme/api", kind: "PullRequest", num: 387, title: "Use structured logging everywhere", reason: "subscribed", age: 12 * day, state: "MERGED", author: "sam"},
	{repo: "acme/web", kind: "CheckSuite", title: "Deploy workflow run failed for main branch", reason: "ci_activity", age: 14 * day},
	{repo: "acme/web", kind: "PullRequest", num: 1255, title: "Update the onboarding copy", reason: "subscribed", age: 15 * day, state: "MERGED", author: "ari"},
	{repo: "acme/cli", kind: "Release", title: "v2.4.0", reason: "subscribed", age: 18 * day},
	{repo: "acme/infra", kind: "Issue", num: 69, title: "Rotate the database credentials monthly", reason: "assign", age: 20 * day, state: "OPEN", author: "kai", assignees: []string{"mona"}},
	{repo: "acme/infra", kind: "CheckSuite", title: "Nightly workflow run failed for main branch", reason: "ci_activity", age: 21 * day},
	{repo: "acme/infra", kind: "PullRequest", num: 74, title: "Bump Terraform to 1.9", reason: "subscribed", age: 25 * day, state: "MERGED", author: "kai"},
	{repo: "acme/mobile", kind: "PullRequest", num: 528, title: "Remove the old analytics SDK", reason: "subscribed", age: 27 * day, state: "MERGED", author: "ren"},
	{repo: "acme/docs", kind: "Issue", num: 150, title: "Broken links in the API reference", reason: "subscribed", age: 30 * day, state: "CLOSED", author: "jo"},
	{repo: "acme/docs", kind: "PullRequest", num: 149, title: "Fix typos in the quickstart", reason: "subscribed", age: 33 * day, state: "MERGED", author: "jo"},
	{repo: "acme/web", kind: "PullRequest", num: 1240, title: "Lazy-load the dashboard charts", reason: "subscribed", age: 35 * day, state: "MERGED", author: "ari"},
	{repo: "acme/api", kind: "PullRequest", num: 376, title: "Add pagination to /v2/events", reason: "review_requested", age: 37 * day, state: "MERGED", author: "lin"},
	{repo: "acme/cli", kind: "Release", title: "v2.3.1", reason: "subscribed", age: 40 * day},
	{repo: "acme/cli", kind: "PullRequest", num: 212, title: "Add shell completion", reason: "subscribed", age: 45 * day, state: "MERGED", author: "lin"},
}

type server struct {
	mu      sync.Mutex
	start   time.Time
	threads map[string]*thread
	order   []string
}

func main() {
	addr := flag.String("addr", "127.0.0.1:8765", "address to listen on")
	flag.Parse()

	s := &server{start: time.Now().Truncate(time.Minute), threads: map[string]*thread{}}
	for i := range threads {
		t := &threads[i]
		t.id = fmt.Sprint(1000 + i)
		s.threads[t.id] = t
		s.order = append(s.order, t.id)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /notifications", s.notifications)
	mux.HandleFunc("GET /user", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]string{"login": "mona"})
	})
	mux.HandleFunc("GET /user/teams", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, []any{team("acme", "backend"), team("acme", "platform")})
	})
	mux.HandleFunc("PATCH /notifications/threads/{id}", s.markRead)
	mux.HandleFunc("DELETE /notifications/threads/{id}", s.markDone)
	mux.HandleFunc("DELETE /notifications/threads/{id}/subscription", s.noContent)
	mux.HandleFunc("DELETE /repos/{owner}/{repo}/subscription", s.noContent)
	mux.HandleFunc("POST /graphql", s.graphql)
	log.Printf("listening on %s", *addr)
	log.Fatal(http.ListenAndServe(*addr, mux))
}

func team(org, slug string) map[string]any {
	return map[string]any{"slug": slug, "organization": map[string]string{"login": org}}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func (s *server) notifications(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	list := []any{}
	for _, id := range s.order {
		t := s.threads[id]
		subjectURL := ""
		switch t.kind {
		case "PullRequest":
			subjectURL = fmt.Sprintf("https://api.github.com/repos/%s/pulls/%d", t.repo, t.num)
		case "Issue":
			subjectURL = fmt.Sprintf("https://api.github.com/repos/%s/issues/%d", t.repo, t.num)
		case "Release":
			subjectURL = fmt.Sprintf("https://api.github.com/repos/%s/releases/%s", t.repo, t.id)
		}
		list = append(list, map[string]any{
			"id":         t.id,
			"unread":     t.unread,
			"reason":     t.reason,
			"updated_at": s.start.Add(-t.age).UTC().Format(time.RFC3339),
			"subject":    map[string]any{"title": t.title, "url": subjectURL, "type": t.kind},
			"repository": map[string]any{"full_name": t.repo},
		})
	}
	writeJSON(w, list)
}

// markDone removes the thread, as the inbox on github.com does.
func (s *server) markDone(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := r.PathValue("id")
	delete(s.threads, id)
	for i, o := range s.order {
		if o == id {
			s.order = append(s.order[:i], s.order[i+1:]...)
			break
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) markRead(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t := s.threads[r.PathValue("id")]; t != nil {
		t.unread = false
	}
	w.WriteHeader(http.StatusResetContent)
}

func (s *server) noContent(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusNoContent)
}

// fieldRE finds the aliased lookups in a details query, such as
// "s0: repository(owner: $o0, name: $r0) { pullRequest(number: $n0)".
var fieldRE = regexp.MustCompile(`(s\d+): repository\([^)]*\) \{ (pullRequest|issue)\(number: \$n(\d+)\)`)

func (s *server) graphql(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Query     string         `json:"query"`
		Variables map[string]any `json:"variables"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	data := map[string]any{}
	for _, m := range fieldRE.FindAllStringSubmatch(req.Query, -1) {
		alias, field, i := m[1], m[2], m[3]
		repo := fmt.Sprintf("%v/%v", req.Variables["o"+i], req.Variables["r"+i])
		num := fmt.Sprint(req.Variables["n"+i])
		data[alias] = map[string]any{field: s.subject(repo, num)}
	}
	writeJSON(w, map[string]any{"data": data})
}

// subject returns the GraphQL details of a pull request or issue.
func (s *server) subject(repo, num string) any {
	for i := range threads {
		t := &threads[i]
		if t.repo != repo || fmt.Sprint(t.num) != num {
			continue
		}
		var reviewers []any
		for _, rv := range t.reviewers {
			if org, slug, ok := strings.Cut(rv, "/"); ok {
				reviewers = append(reviewers, map[string]any{"requestedReviewer": map[string]any{
					"__typename": "Team", "slug": slug, "organization": map[string]string{"login": org},
				}})
			} else {
				reviewers = append(reviewers, map[string]any{"requestedReviewer": map[string]any{"__typename": "User", "login": rv}})
			}
		}
		return map[string]any{
			"number":         t.num,
			"state":          t.state,
			"isDraft":        t.draft,
			"url":            fmt.Sprintf("https://github.com/%s/%d", repo, t.num),
			"author":         map[string]string{"login": t.author},
			"labels":         map[string]any{"nodes": names("name", t.labels)},
			"assignees":      map[string]any{"nodes": names("login", t.assignees)},
			"reviewRequests": map[string]any{"nodes": reviewers},
		}
	}
	return nil
}

func names(key string, values []string) []any {
	nodes := []any{}
	for _, v := range values {
		nodes = append(nodes, map[string]string{key: v})
	}
	return nodes
}
