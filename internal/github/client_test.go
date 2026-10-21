package github

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

// rewriteTransport sends every request to a test server, whatever its host.
type rewriteTransport struct {
	target *url.URL
}

func (t rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.URL.Scheme = t.target.Scheme
	req.URL.Host = t.target.Host
	return http.DefaultTransport.RoundTrip(req)
}

// newTestClient returns a client whose requests go to handler. Retries don't
// wait; the delays they would have waited are appended to *slept.
func newTestClient(t *testing.T, handler http.Handler) (c *Client, slept *[]time.Duration) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	target, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	c, err = New(Options{Host: "github.com", AuthToken: "test-token", Transport: rewriteTransport{target}})
	if err != nil {
		t.Fatal(err)
	}
	slept = new([]time.Duration)
	c.sleep = func(_ context.Context, d time.Duration) error {
		*slept = append(*slept, d)
		return nil
	}
	return c, slept
}

func TestNextPage(t *testing.T) {
	tests := []struct {
		link string
		want string
	}{
		{link: "", want: ""},
		{
			link: `<https://api.github.com/notifications?page=2>; rel="next", <https://api.github.com/notifications?page=5>; rel="last"`,
			want: "https://api.github.com/notifications?page=2",
		},
		{
			link: `<https://api.github.com/notifications?page=1>; rel="prev", <https://api.github.com/notifications?page=1>; rel="first"`,
			want: "",
		},
	}
	for _, tt := range tests {
		if got := nextPage(tt.link); got != tt.want {
			t.Errorf("nextPage(%q) = %q, want %q", tt.link, got, tt.want)
		}
	}
}

func TestThreadActions(t *testing.T) {
	tests := []struct {
		name       string
		call       func(*Client, context.Context, string) error
		wantMethod string
		wantPath   string
	}{
		{name: "done", call: (*Client).MarkDone, wantMethod: http.MethodDelete, wantPath: "/notifications/threads/42"},
		{name: "read", call: (*Client).MarkRead, wantMethod: http.MethodPatch, wantPath: "/notifications/threads/42"},
		{name: "unsubscribe", call: (*Client).Unsubscribe, wantMethod: http.MethodDelete, wantPath: "/notifications/threads/42/subscription"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotMethod, gotPath string
			c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotMethod, gotPath = r.Method, r.URL.Path
				w.WriteHeader(http.StatusNoContent)
			}))
			if err := tt.call(c, t.Context(), "42"); err != nil {
				t.Fatal(err)
			}
			if gotMethod != tt.wantMethod || gotPath != tt.wantPath {
				t.Errorf("request = %s %s, want %s %s", gotMethod, gotPath, tt.wantMethod, tt.wantPath)
			}
		})
	}
}

func TestMarkDoneDoesNotFollowRedirects(t *testing.T) {
	var requests int
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		http.Redirect(w, r, "/elsewhere", http.StatusMovedPermanently)
	}))
	if err := c.MarkDone(t.Context(), "42"); err == nil {
		t.Error("MarkDone succeeded after a redirect, want an error")
	}
	if requests != 1 {
		t.Errorf("server got %d requests, want 1", requests)
	}
}

func TestRetries(t *testing.T) {
	tests := []struct {
		name         string
		responses    []func(http.ResponseWriter)
		wantErr      bool
		wantRequests int
		wantSlept    []time.Duration
	}{
		{
			name: "server error then success",
			responses: []func(http.ResponseWriter){
				status(http.StatusBadGateway),
				status(http.StatusNoContent),
			},
			wantRequests: 2,
			wantSlept:    []time.Duration{time.Second},
		},
		{
			name: "secondary rate limit with Retry-After",
			responses: []func(http.ResponseWriter){
				func(w http.ResponseWriter) {
					w.Header().Set("Retry-After", "7")
					w.WriteHeader(http.StatusForbidden)
				},
				status(http.StatusNoContent),
			},
			wantRequests: 2,
			wantSlept:    []time.Duration{7 * time.Second},
		},
		{
			name: "secondary rate limit without headers",
			responses: []func(http.ResponseWriter){
				func(w http.ResponseWriter) {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusForbidden)
					w.Write([]byte(`{"message": "You have exceeded a secondary rate limit."}`))
				},
				status(http.StatusNoContent),
			},
			wantRequests: 2,
			wantSlept:    []time.Duration{rateLimitDelay},
		},
		{
			name:         "permission error isn't retried",
			responses:    []func(http.ResponseWriter){status(http.StatusForbidden)},
			wantErr:      true,
			wantRequests: 1,
		},
		{
			name:         "not found isn't retried",
			responses:    []func(http.ResponseWriter){status(http.StatusNotFound)},
			wantErr:      true,
			wantRequests: 1,
		},
		{
			name: "gives up after maxAttempts",
			responses: []func(http.ResponseWriter){
				status(http.StatusServiceUnavailable),
				status(http.StatusServiceUnavailable),
				status(http.StatusServiceUnavailable),
				status(http.StatusServiceUnavailable),
				status(http.StatusServiceUnavailable),
			},
			wantErr:      true,
			wantRequests: maxAttempts,
			wantSlept:    []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var requests int
			c, slept := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				tt.responses[requests](w)
				requests++
			}))
			err := c.MarkDone(t.Context(), "42")
			if (err != nil) != tt.wantErr {
				t.Fatalf("MarkDone() error = %v, wantErr %v", err, tt.wantErr)
			}
			if requests != tt.wantRequests {
				t.Errorf("server got %d requests, want %d", requests, tt.wantRequests)
			}
			if !equalDurations(*slept, tt.wantSlept) {
				t.Errorf("slept %v, want %v", *slept, tt.wantSlept)
			}
		})
	}
}

func TestRateLimitWait(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	header := func(kv ...string) http.Header {
		h := http.Header{}
		for i := 0; i < len(kv); i += 2 {
			h.Set(kv[i], kv[i+1])
		}
		return h
	}
	tests := []struct {
		name     string
		header   http.Header
		status   int
		want     time.Duration
		wantWait bool
	}{
		{name: "retry after", header: header("Retry-After", "30"), status: 429, want: 30 * time.Second, wantWait: true},
		{name: "retry after is capped", header: header("Retry-After", "3600"), status: 429, want: maxDelay, wantWait: true},
		{
			name:   "primary limit resets soon",
			header: header("X-RateLimit-Remaining", "0", "X-RateLimit-Reset", "1000010"),
			status: 403, want: 11 * time.Second, wantWait: true,
		},
		{
			name:   "primary limit resets too late",
			header: header("X-RateLimit-Remaining", "0", "X-RateLimit-Reset", "1003600"),
			status: 403,
		},
		{name: "429 without headers", header: header(), status: 429, want: rateLimitDelay, wantWait: true},
		{name: "403 without headers", header: header(), status: 403},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := rateLimitWait(tt.header, tt.status, now)
			if ok != tt.wantWait || got != tt.want {
				t.Errorf("rateLimitWait() = %v, %v, want %v, %v", got, ok, tt.want, tt.wantWait)
			}
		})
	}
}

func status(code int) func(http.ResponseWriter) {
	return func(w http.ResponseWriter) { w.WriteHeader(code) }
}

func equalDurations(a, b []time.Duration) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
