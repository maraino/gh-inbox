package cmd

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/maraino/gh-inbox/internal/cache"
	"github.com/maraino/gh-inbox/internal/filter"
	"github.com/maraino/gh-inbox/internal/inbox"
)

// selection is the result of applying the filters to the inbox.
type selection struct {
	// listed is every notification the API listed.
	listed []inbox.Notification
	// matched are the notifications that match the filters.
	matched []inbox.Notification
	// unreadable are the pull requests and issues that pass the filters on
	// notifications, but whose details couldn't be read to apply the
	// others.
	unreadable []inbox.Notification
}

// selectNotifications lists the inbox and applies the filters. Threads the
// record says are done are left out; done can be nil to keep them.
func selectNotifications(ctx context.Context, c client, opts *filter.Options, done *cache.Store, now time.Time, p progress) (*selection, error) {
	env := filter.Env{Now: now}
	if opts.NeedsIdentity() {
		p.show("Looking up your user and teams…")
		me, err := c.Viewer(ctx)
		if err != nil {
			return nil, err
		}
		env.Me = me
	}
	filters, err := filter.New(opts, env)
	if err != nil {
		return nil, err
	}

	p.show("Listing notifications…")
	listed, err := c.Notifications(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing notifications: %w", err)
	}

	var candidates []inbox.Notification
	for _, n := range listed {
		if done != nil && done.IsDone(n.ID, n.UpdatedAt) {
			continue
		}
		if filters.MatchBasic(n) {
			candidates = append(candidates, n)
		}
	}

	if filters.NeedsDetails() {
		p.show("Looking up pull requests and issues…")
		details, err := c.SubjectDetails(ctx, candidates)
		if err != nil {
			return nil, err
		}
		for i := range candidates {
			candidates[i].Subject = details[candidates[i].SubjectURL]
		}
	}
	p.clear()

	sel := &selection{listed: listed}
	for _, n := range candidates {
		switch {
		case filters.Match(n):
			sel.matched = append(sel.matched, n)
		case filters.NeedsDetails() && n.Type.HasDetails() && n.Subject == nil:
			sel.unreadable = append(sel.unreadable, n)
		}
	}
	return sel, nil
}

// reportUnreadable tells which notifications were left out because their
// pull request or issue couldn't be read.
func reportUnreadable(w io.Writer, unreadable []inbox.Notification) {
	if len(unreadable) == 0 {
		return
	}
	fmt.Fprintf(w, "\nSkipped %s whose pull request or issue couldn't be read:\n", notifications(len(unreadable)))
	for _, n := range unreadable {
		fmt.Fprintf(w, "  %s: %s\n", n.Repo, n.Title)
	}
}

// progress shows a status line on a terminal, and nothing elsewhere.
type progress struct {
	w       io.Writer
	enabled bool
}

func (p progress) show(format string, args ...any) {
	if p.enabled {
		fmt.Fprintf(p.w, "\r\033[K"+format, args...)
	}
}

func (p progress) clear() {
	if p.enabled {
		fmt.Fprint(p.w, "\r\033[K")
	}
}

// notifications returns "1 notification" or "n notifications".
func notifications(n int) string {
	if n == 1 {
		return "1 notification"
	}
	return fmt.Sprintf("%d notifications", n)
}
