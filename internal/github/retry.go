package github

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/cli/go-gh/v2/pkg/api"
)

const (
	// maxAttempts is how many times a request is tried before giving up.
	maxAttempts = 5
	// baseDelay is the wait before the first retry; it doubles after each.
	baseDelay = time.Second
	// rateLimitDelay is the wait after hitting a secondary rate limit that
	// doesn't say how long to wait, as GitHub's documentation recommends.
	rateLimitDelay = time.Minute
	// maxDelay caps any single wait. A primary rate limit that resets later
	// than this fails instead of hanging.
	maxDelay = 2 * time.Minute
)

// retry calls op until it succeeds, fails with an error that isn't worth
// retrying, or has been tried maxAttempts times.
func (c *Client) retry(ctx context.Context, op func() error) error {
	for attempt := 1; ; attempt++ {
		err := op()
		if err == nil || attempt == maxAttempts {
			return err
		}
		delay, ok := retryDelay(err, attempt, time.Now())
		if !ok {
			return err
		}
		c.logf("retrying in %s (attempt %d of %d): %v", delay, attempt+1, maxAttempts, err)
		if err := c.sleep(ctx, delay); err != nil {
			return err
		}
	}
}

// retryDelay reports whether err is worth retrying, and how long to wait
// before the given attempt is retried.
func retryDelay(err error, attempt int, now time.Time) (time.Duration, bool) {
	backoff := baseDelay << (attempt - 1)

	if gqlErr, ok := errors.AsType[*api.GraphQLError](err); ok {
		for _, item := range gqlErr.Errors {
			if item.Type == "RATE_LIMITED" {
				return rateLimitDelay, true
			}
		}
		return 0, false
	}

	var httpErr *api.HTTPError
	if !errors.As(err, &httpErr) {
		return 0, false
	}
	switch httpErr.StatusCode {
	case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return backoff, true
	case http.StatusForbidden, http.StatusTooManyRequests:
		if wait, ok := rateLimitWait(httpErr.Headers, httpErr.StatusCode, now); ok {
			return wait, true
		}
		// Secondary rate limits don't always come with headers.
		if strings.Contains(strings.ToLower(httpErr.Message), "secondary rate limit") {
			return rateLimitDelay, true
		}
		return 0, false
	default:
		return 0, false
	}
}

// rateLimitWait returns how long to wait for a rate limit to lift, from the
// response headers. A 403 without rate-limit headers is a permission error,
// which isn't retried.
func rateLimitWait(h http.Header, status int, now time.Time) (time.Duration, bool) {
	if secs, err := strconv.Atoi(h.Get("Retry-After")); err == nil {
		return min(time.Duration(secs)*time.Second, maxDelay), true
	}
	if h.Get("X-RateLimit-Remaining") == "0" {
		reset, err := strconv.ParseInt(h.Get("X-RateLimit-Reset"), 10, 64)
		if err != nil {
			return rateLimitDelay, true
		}
		wait := time.Unix(reset, 0).Sub(now) + time.Second
		if wait > maxDelay {
			return 0, false
		}
		return max(wait, 0), true
	}
	if status == http.StatusTooManyRequests {
		return rateLimitDelay, true
	}
	return 0, false
}

// sleep waits for d, or until ctx is done.
func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
