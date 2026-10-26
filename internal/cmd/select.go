package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"time"

	"github.com/maraino/gh-inbox/internal/cache"
	"github.com/maraino/gh-inbox/internal/filter"
	"github.com/maraino/gh-inbox/internal/github"
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
	// detailed reports whether the details of pull requests and issues were
	// looked up.
	detailed bool
}

// selectNotifications lists the inbox and applies the filters. Threads the
// record says are done are left out; done can be nil to keep them. With
// wantTeams, looking up pull requests also looks up the teams whose review
// is requested, when the token can read them.
func selectNotifications(ctx context.Context, c client, opts *filter.Options, done *cache.Store, now time.Time, wantTeams bool, p progress) (*selection, error) {
	sels, err := selectEach(ctx, c, []*filter.Options{opts}, done, now, wantTeams, p)
	if err != nil {
		return nil, err
	}
	return sels[0], nil
}

// selectEach is selectNotifications for several sets of filters, in order:
// the inbox is listed once, and a notification is only selected by the first
// set it matches.
func selectEach(ctx context.Context, c client, opts []*filter.Options, done *cache.Store, now time.Time, wantTeams bool, p progress) ([]*selection, error) {
	env := filter.Env{Now: now}
	if slices.ContainsFunc(opts, (*filter.Options).NeedsIdentity) {
		p.show("Looking up your user and teams…")
		me, err := c.Viewer(ctx)
		if err != nil {
			return nil, err
		}
		env.Me = me
	}
	sets := make([]*filter.Set, len(opts))
	var needDetails, needTeams bool
	for i, o := range opts {
		s, err := filter.New(o, env)
		if err != nil {
			return nil, err
		}
		sets[i] = s
		needDetails = needDetails || s.NeedsDetails()
		needTeams = needTeams || s.NeedsReviewTeams()
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
		if slices.ContainsFunc(sets, func(s *filter.Set) bool { return s.MatchBasic(n) }) {
			candidates = append(candidates, n)
		}
	}

	if needDetails {
		if err := lookUpDetails(ctx, c, candidates, needTeams, wantTeams, p); err != nil {
			return nil, err
		}
	}
	p.clear()

	sels := make([]*selection, len(sets))
	selected := make(map[string]bool)
	for i, s := range sets {
		sel := &selection{listed: listed, detailed: needDetails}
		for _, n := range candidates {
			switch {
			case selected[n.ID]:
			case s.Match(n):
				sel.matched = append(sel.matched, n)
				selected[n.ID] = true
			case s.NeedsDetails() && n.Type.HasDetails() && n.Subject == nil:
				sel.unreadable = append(sel.unreadable, n)
			}
		}
		sels[i] = sel
	}
	return sels, nil
}

// lookUpDetails sets the details of the pull requests and issues in
// notifications. Those that couldn't be read keep a nil Subject. The teams
// whose review is requested are looked up when needTeams, or when wantTeams
// and the token can read them.
func lookUpDetails(ctx context.Context, c client, notifications []inbox.Notification, needTeams, wantTeams bool, p progress) error {
	p.show("Looking up pull requests and issues…")
	details, err := c.SubjectDetails(ctx, notifications, github.DetailsOptions{ReviewTeams: needTeams || wantTeams})
	if err != nil && !needTeams && errors.Is(err, github.ErrMissingReadOrg) {
		details, err = c.SubjectDetails(ctx, notifications, github.DetailsOptions{})
	}
	if err != nil {
		return err
	}
	for i := range notifications {
		notifications[i].Subject = details[notifications[i].SubjectURL]
	}
	return nil
}

// merge returns the notifications that any of the selections matched, in
// the order of the inbox.
func merge(sels []*selection) *selection {
	if len(sels) == 1 {
		return sels[0]
	}
	// The matched notifications have details that the listed ones don't.
	matched := make(map[string]inbox.Notification)
	for _, sel := range sels {
		for _, n := range sel.matched {
			matched[n.ID] = n
		}
	}
	merged := &selection{listed: sels[0].listed, detailed: sels[0].detailed, unreadable: unreadableOf(sels)}
	for _, n := range sels[0].listed {
		if m, ok := matched[n.ID]; ok {
			merged.matched = append(merged.matched, m)
		}
	}
	return merged
}

// unreadableOf returns the unreadable notifications of the selections, each
// once, leaving out those another selection matched.
func unreadableOf(sels []*selection) []inbox.Notification {
	seen := make(map[string]bool)
	for _, sel := range sels {
		for _, n := range sel.matched {
			seen[n.ID] = true
		}
	}
	var unreadable []inbox.Notification
	for _, sel := range sels {
		for _, n := range sel.unreadable {
			if !seen[n.ID] {
				seen[n.ID] = true
				unreadable = append(unreadable, n)
			}
		}
	}
	return unreadable
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
