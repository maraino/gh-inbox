package cmd

import (
	"context"
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/maraino/gh-inbox/internal/action"
	"github.com/maraino/gh-inbox/internal/cache"
	"github.com/maraino/gh-inbox/internal/filter"
	"github.com/maraino/gh-inbox/internal/inbox"
	"github.com/maraino/gh-inbox/internal/render"
)

type cleanOptions struct {
	filters     filter.Options
	action      string
	apply       bool
	all         bool
	concurrency int
}

func newCleanCmd(root *rootOptions, e *env) *cobra.Command {
	opts := &cleanOptions{}
	cmd := &cobra.Command{
		Use:   "clean [flags]",
		Short: "Mark the notifications that match the filters as done or read, or unsubscribe from them",
		Long: `Mark the notifications in your inbox that match the filters as done or as
read, or unsubscribe from them.

Without --apply, clean only shows what it would do. It refuses to clean every
notification unless --all is given.

Marking as done removes notifications from the inbox; they stay under the
"Done" filter on github.com, and come back when there's new activity. The API
can't tell done notifications apart from read ones, so gh inbox keeps a local
record of the ones it marked as done and skips them until they have new
activity. --no-cache ignores the record.
` + filterHelp,
		Example: `  # Show what marking merged and closed pull requests as done would do
  gh inbox clean --state merged,closed

  # Do it
  gh inbox clean --state merged,closed --apply

  # Open pull requests not assigned to you nor awaiting review from you or your teams
  gh inbox clean --type pr --state open --not-involves @me --apply

  # Mark CI notifications older than a week as read
  gh inbox clean --type ci --older-than 7d --action read --apply

  # Stop following threads you only subscribed to in some repositories
  gh inbox clean --repo 'acme/*' --reason subscribed --action unsubscribe --apply`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runClean(cmd.Context(), root, e, opts)
		},
	}

	flags := cmd.Flags()
	addFilterFlags(flags, &opts.filters)
	flags.StringVar(&opts.action, "action", string(action.Done), "The `action` to apply: done, read or unsubscribe")
	flags.BoolVar(&opts.apply, "apply", false, "Apply the action; without it, only show what it would do")
	flags.BoolVar(&opts.all, "all", false, "Allow cleaning without filters, which acts on every notification")
	flags.IntVar(&opts.concurrency, "concurrency", 4, "How many requests to make at the same time")
	return cmd
}

func runClean(ctx context.Context, root *rootOptions, e *env, opts *cleanOptions) error {
	kind, err := action.ParseKind(opts.action)
	if err != nil {
		return err
	}
	if opts.filters.IsEmpty() && !opts.all {
		return errors.New("no filters given: use filters to choose the notifications, or --all to clean every one")
	}
	if opts.concurrency < 1 {
		return errors.New("--concurrency must be at least 1")
	}

	c, err := root.client(e)
	if err != nil {
		return err
	}
	done, err := root.openCache(e)
	if err != nil {
		return err
	}
	p := progress{e.errOut, e.showProgress}
	sel, err := selectNotifications(ctx, c, &opts.filters, done, e.now(), p)
	if err != nil {
		return err
	}
	defer reportUnreadable(e.errOut, sel.unreadable)

	if len(sel.matched) == 0 {
		fmt.Fprintln(e.errOut, "No notifications match.")
		return nil
	}
	if !opts.apply {
		fmt.Fprintf(e.out, "Would %s.\n\n", describe(kind, len(sel.matched)))
		if err := render.Summary(e.out, sel.matched); err != nil {
			return err
		}
		fmt.Fprintln(e.out, "\nRun again with --apply to do it, or use \"gh inbox list\" with the same filters to see each notification.")
		return nil
	}

	runner := action.Runner{
		Client:      c,
		Kind:        kind,
		Concurrency: opts.concurrency,
		Progress: func(finished, total int) {
			p.show("%d/%d", finished, total)
		},
	}
	succeeded, failed := runner.Run(ctx, sel.matched)
	p.clear()

	if kind == action.Done && done != nil {
		if err := recordDone(done, succeeded, sel.listed); err != nil {
			fmt.Fprintf(e.errOut, "warning: saving the record of notifications marked as done: %v\n", err)
		}
	}

	if len(succeeded) > 0 {
		fmt.Fprintf(e.out, "%s.\n", describePast(kind, len(succeeded)))
	}
	if len(failed) > 0 {
		fmt.Fprintf(e.errOut, "\nFailed to %s:\n", describe(kind, len(failed)))
		for _, f := range failed {
			fmt.Fprintf(e.errOut, "  %s: %s: %v\n", f.Notification.Repo, f.Notification.Title, f.Err)
		}
		return fmt.Errorf("%d of %d failed", len(failed), len(sel.matched))
	}
	return nil
}

// recordDone adds the threads marked as done to the record, and forgets the
// ones no longer in the inbox.
func recordDone(done *cache.Store, succeeded, listed []inbox.Notification) error {
	for _, n := range succeeded {
		done.MarkDone(n.ID, n.UpdatedAt)
	}
	inInbox := make(map[string]bool, len(listed))
	for _, n := range listed {
		inInbox[n.ID] = true
	}
	done.Retain(func(id string) bool { return inInbox[id] })
	return done.Save()
}

// describe returns what applying the action to n notifications does, such
// as "mark 3 notifications as done".
func describe(kind action.Kind, n int) string {
	switch kind {
	case action.Done:
		return "mark " + notifications(n) + " as done"
	case action.Read:
		return "mark " + notifications(n) + " as read"
	default:
		return "unsubscribe from " + notifications(n)
	}
}

// describePast is describe in the past tense, capitalized to start a
// sentence, such as "Marked 3 notifications as done".
func describePast(kind action.Kind, n int) string {
	switch kind {
	case action.Done:
		return "Marked " + notifications(n) + " as done"
	case action.Read:
		return "Marked " + notifications(n) + " as read"
	default:
		return "Unsubscribed from " + notifications(n)
	}
}
