// Package github talks to the GitHub API: it lists notifications, looks up
// the pull requests and issues they are about, and acts on threads.
package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/cli/go-gh/v2/pkg/api"
)

// Options configure a Client.
type Options struct {
	// Host is the GitHub host, such as "github.com". Empty means the gh
	// default host.
	Host string
	// Log, when set, receives a line per API request and retry.
	Log io.Writer
	// CacheDir, when set, is where the list of notifications and the
	// details of pull requests and issues are kept between runs.
	CacheDir string

	// AuthToken and Transport replace gh's token and the default HTTP
	// transport. They are meant for tests.
	AuthToken string
	Transport http.RoundTripper
}

// Client is a GitHub API client. It is safe for concurrent use.
type Client struct {
	opts     api.ClientOptions
	rest     *api.RESTClient
	gql      *api.GraphQLClient
	log      io.Writer
	cacheDir string
	sleep    func(context.Context, time.Duration) error
	now      func() time.Time
}

// New returns a client that authenticates with gh's credentials for the
// host.
func New(opts Options) (*Client, error) {
	clientOpts := api.ClientOptions{
		Host:      opts.Host,
		AuthToken: opts.AuthToken,
		Transport: opts.Transport,
		Log:       opts.Log,
		// A redirect can turn a DELETE into a GET that succeeds without doing
		// anything, so report redirects instead of following them.
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	rest, err := api.NewRESTClient(clientOpts)
	if err != nil {
		return nil, err
	}
	gql, err := api.NewGraphQLClient(clientOpts)
	if err != nil {
		return nil, err
	}
	return &Client{
		opts: clientOpts, rest: rest, gql: gql, log: opts.Log, cacheDir: opts.CacheDir,
		sleep: sleep, now: time.Now,
	}, nil
}

// MarkDone marks a thread as done, which removes it from the inbox.
func (c *Client) MarkDone(ctx context.Context, threadID string) error {
	return c.send(ctx, http.MethodDelete, "notifications/threads/"+url.PathEscape(threadID))
}

// MarkRead marks a thread as read.
func (c *Client) MarkRead(ctx context.Context, threadID string) error {
	return c.send(ctx, http.MethodPatch, "notifications/threads/"+url.PathEscape(threadID))
}

// Unsubscribe mutes a thread until you comment on it or are mentioned.
func (c *Client) Unsubscribe(ctx context.Context, threadID string) error {
	return c.send(ctx, http.MethodDelete, "notifications/threads/"+url.PathEscape(threadID)+"/subscription")
}

// Unwatch stops watching a repository, given as "owner/name". Threads you
// take part in or are mentioned in still notify you.
func (c *Client) Unwatch(ctx context.Context, repo string) error {
	owner, name, ok := strings.Cut(repo, "/")
	if !ok || owner == "" || name == "" || strings.Contains(name, "/") {
		return fmt.Errorf("invalid repository %q: want owner/name", repo)
	}
	return c.send(ctx, http.MethodDelete, "repos/"+url.PathEscape(owner)+"/"+url.PathEscape(name)+"/subscription")
}

// send makes a request without a body and discards the response. A change
// to a thread makes the cached list of notifications out of date.
func (c *Client) send(ctx context.Context, method, path string) error {
	err := c.retry(ctx, func() error {
		resp, err := c.rest.RequestWithContext(ctx, method, path, nil)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 300 {
			return fmt.Errorf("%s %s: unexpected status %s", method, path, resp.Status)
		}
		return nil
	})
	if err == nil && strings.HasPrefix(path, "notifications/") {
		c.forgetList()
	}
	return err
}

// get decodes the response of a REST endpoint into v. For paginated
// endpoints, it also returns the pagination links, such as "next" and "last".
func (c *Client) get(ctx context.Context, path string, v any) (links map[string]string, err error) {
	h, err := c.getWith(ctx, c.rest, path, v)
	if err != nil {
		return nil, err
	}
	return parseLinks(h.Get("Link")), nil
}

// errNotModified is returned by getIfModified when the response hasn't
// changed.
var errNotModified = errors.New("not modified")

// getIfModified is get with an If-Modified-Since header, when since isn't
// empty. It returns errNotModified when the response hasn't changed, and
// otherwise the response headers.
func (c *Client) getIfModified(ctx context.Context, path, since string, v any) (http.Header, error) {
	rest := c.rest
	if since != "" {
		opts := c.opts
		opts.Headers = map[string]string{"If-Modified-Since": since}
		var err error
		if rest, err = api.NewRESTClient(opts); err != nil {
			return nil, err
		}
	}
	h, err := c.getWith(ctx, rest, path, v)
	var httpErr *api.HTTPError
	if errors.As(err, &httpErr) && httpErr.StatusCode == http.StatusNotModified {
		return nil, errNotModified
	}
	return h, err
}

// getWith decodes the response of a REST endpoint into v with the given
// client, and returns the response headers.
func (c *Client) getWith(ctx context.Context, rest *api.RESTClient, path string, v any) (h http.Header, err error) {
	err = c.retry(ctx, func() error {
		resp, err := rest.RequestWithContext(ctx, http.MethodGet, path, nil)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
			return fmt.Errorf("decoding %s: %w", path, err)
		}
		h = resp.Header
		return nil
	})
	return h, err
}

// parseLinks returns the URLs of a Link header by relation, such as
// "next".
func parseLinks(header string) map[string]string {
	links := make(map[string]string)
	for part := range strings.SplitSeq(header, ",") {
		target, params, ok := strings.Cut(strings.TrimSpace(part), ";")
		if !ok {
			continue
		}
		for param := range strings.SplitSeq(params, ";") {
			if rel, ok := strings.CutPrefix(strings.TrimSpace(param), "rel="); ok {
				links[strings.Trim(rel, `"`)] = strings.Trim(strings.TrimSpace(target), "<>")
			}
		}
	}
	return links
}

func (c *Client) logf(format string, args ...any) {
	if c.log != nil {
		fmt.Fprintf(c.log, format+"\n", args...)
	}
}
