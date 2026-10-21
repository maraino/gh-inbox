// Package github talks to the GitHub API: it lists notifications, looks up
// the pull requests and issues they are about, and acts on threads.
package github

import (
	"context"
	"encoding/json"
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

	// AuthToken and Transport replace gh's token and the default HTTP
	// transport. They are meant for tests.
	AuthToken string
	Transport http.RoundTripper
}

// Client is a GitHub API client. It is safe for concurrent use.
type Client struct {
	rest  *api.RESTClient
	gql   *api.GraphQLClient
	log   io.Writer
	sleep func(context.Context, time.Duration) error
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
	return &Client{rest: rest, gql: gql, log: opts.Log, sleep: sleep}, nil
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

// send makes a request without a body and discards the response.
func (c *Client) send(ctx context.Context, method, path string) error {
	return c.retry(ctx, func() error {
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
}

// get decodes the response of a REST endpoint into v. For paginated
// endpoints, it also returns the pagination links, such as "next" and "last".
func (c *Client) get(ctx context.Context, path string, v any) (links map[string]string, err error) {
	err = c.retry(ctx, func() error {
		resp, err := c.rest.RequestWithContext(ctx, http.MethodGet, path, nil)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
			return fmt.Errorf("decoding %s: %w", path, err)
		}
		links = parseLinks(resp.Header.Get("Link"))
		return nil
	})
	return links, err
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
