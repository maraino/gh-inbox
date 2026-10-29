package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"

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
	list        bool
	concurrency int
	rules       ruleFlags
	// actionSet reports whether --action was given.
	actionSet bool
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

Instead of filters and --action, --rule applies rules from a rules file, and
--rules applies every rule in it. The file is ~/.config/gh-inbox/rules.yml,
or gh-inbox/rules.yml under $XDG_CONFIG_HOME, unless --rules-file is given.
Each rule has a name, an action (done by default), and filters with the same
names and values as the flags:

  rules:
    - name: merged
      filters:
        state: [merged, closed]
    - name: stale-ci
      action: read
      filters:
        type: ci
        older-than: 7d
    - name: noisy-repos
      action: unsubscribe
      filters:
        repo: ["acme/infra", "acme/docs-*"]
        reason: subscribed

Rules apply in the order of the file, and a notification only goes to the
first rule it matches. A rule must have filters.
` + filterHelp,
		Example: `  # Show what marking merged and closed pull requests as done would do
  gh inbox clean --state merged,closed

  # Do it, and list the notifications marked as done
  gh inbox clean --state merged,closed --apply --list

  # Open pull requests not assigned to you nor awaiting review from you or your teams
  gh inbox clean --type pr --state open --not-involves @me --apply

  # Mark CI notifications older than a week as read
  gh inbox clean --type ci --older-than 7d --action read --apply

  # Stop following threads you only subscribed to in some repositories
  gh inbox clean --repo 'acme/*' --reason subscribed --action unsubscribe --apply

  # Show what every rule in the rules file would do
  gh inbox clean --rules

  # Apply two of them
  gh inbox clean --rule merged,stale-ci --apply`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			opts.actionSet = cmd.Flags().Changed("action")
			if opts.rules.given() {
				return runCleanRules(cmd.Context(), root, e, opts)
			}
			return runClean(cmd.Context(), root, e, opts)
		},
	}

	flags := cmd.Flags()
	addFilterFlags(flags, &opts.filters)
	flags.StringVar(&opts.action, "action", string(action.Done), "The `action` to apply: done, read or unsubscribe")
	flags.BoolVar(&opts.apply, "apply", false, "Apply the action; without it, only show what it would do")
	flags.BoolVar(&opts.all, "all", false, "Allow cleaning without filters, which acts on every notification")
	flags.BoolVar(&opts.list, "list", false, "List each notification, as \"gh inbox list\" does, instead of a summary")
	flags.IntVar(&opts.concurrency, "concurrency", 4, "How many requests to make at the same time")
	opts.rules.add(flags, "Apply the rule `name` from the rules file, instead of filters",
		"Apply every rule in the rules file")
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
	sel, err := selectNotifications(ctx, c, &opts.filters, done, e.now(), false, p)
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
		if err := opts.show(e, sel.matched); err != nil {
			return err
		}
		if opts.list {
			fmt.Fprintln(e.out, "\nRun again with --apply to do it.")
		} else {
			fmt.Fprintln(e.out, "\nRun again with --apply to do it, or add --list to see each notification.")
		}
		return nil
	}

	succeeded, failed := applyAction(ctx, c, kind, sel.matched, opts.concurrency, p)
	if kind == action.Done && done != nil {
		if err := recordDone(done, succeeded, sel.listed); err != nil {
			fmt.Fprintf(e.errOut, "warning: saving the record of notifications marked as done: %v\n", err)
		}
	}

	if len(succeeded) > 0 {
		if err := opts.showDone(e, "", kind, succeeded); err != nil {
			return err
		}
	}
	if len(failed) > 0 {
		reportFailed(e.errOut, "", kind, failed)
		return fmt.Errorf("%d of %d failed", len(failed), len(sel.matched))
	}
	return nil
}

// runCleanRules is clean with rules from the rules file instead of filters.
func runCleanRules(ctx context.Context, root *rootOptions, e *env, opts *cleanOptions) error {
	switch {
	case !opts.filters.IsEmpty() || opts.actionSet || opts.all:
		return errors.New("rules can't be combined with filters, --action or --all")
	case opts.concurrency < 1:
		return errors.New("--concurrency must be at least 1")
	}
	rules, err := opts.rules.load(e)
	if err != nil {
		return err
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
	sels, err := selectEach(ctx, c, ruleFilters(rules), done, e.now(), false, p)
	if err != nil {
		return err
	}
	defer reportUnreadable(e.errOut, unreadableOf(sels))

	if !opts.apply {
		for i, r := range rules {
			if i > 0 {
				fmt.Fprintln(e.out)
			}
			matched := sels[i].matched
			if len(matched) == 0 {
				fmt.Fprintf(e.out, "Rule %q: No notifications match.\n", r.name)
				continue
			}
			fmt.Fprintf(e.out, "Rule %q: Would %s.\n\n", r.name, describe(r.kind, len(matched)))
			if err := opts.show(e, matched); err != nil {
				return err
			}
		}
		fmt.Fprintln(e.out, "\nRun again with --apply to do it.")
		return nil
	}

	var allDone []inbox.Notification
	var failures, total int
	for i, r := range rules {
		if i > 0 && opts.list {
			fmt.Fprintln(e.out)
		}
		matched := sels[i].matched
		if len(matched) == 0 {
			fmt.Fprintf(e.out, "Rule %q: No notifications match.\n", r.name)
			continue
		}
		succeeded, failed := applyAction(ctx, c, r.kind, matched, opts.concurrency, p)
		if r.kind == action.Done {
			allDone = append(allDone, succeeded...)
		}
		if len(succeeded) > 0 {
			if err := opts.showDone(e, fmt.Sprintf("Rule %q: ", r.name), r.kind, succeeded); err != nil {
				return err
			}
		}
		if len(failed) > 0 {
			reportFailed(e.errOut, fmt.Sprintf("Rule %q: ", r.name), r.kind, failed)
		}
		failures += len(failed)
		total += len(matched)
	}
	if done != nil && len(allDone) > 0 {
		if err := recordDone(done, allDone, sels[0].listed); err != nil {
			fmt.Fprintf(e.errOut, "warning: saving the record of notifications marked as done: %v\n", err)
		}
	}
	if failures > 0 {
		return fmt.Errorf("%d of %d failed", failures, total)
	}
	return nil
}

// show writes the notifications clean would act on: each one with --list,
// and a summary otherwise.
func (o *cleanOptions) show(e *env, notifications []inbox.Notification) error {
	if o.list {
		return render.Table(e.out, notifications, e.isTTY, e.width, e.now())
	}
	return render.Summary(e.out, notifications)
}

// showDone reports the notifications the action succeeded on, after a
// heading that starts with prefix, and lists them with --list.
func (o *cleanOptions) showDone(e *env, prefix string, kind action.Kind, succeeded []inbox.Notification) error {
	if !o.list {
		fmt.Fprintf(e.out, "%s%s.\n", prefix, describePast(kind, len(succeeded)))
		return nil
	}
	fmt.Fprintf(e.out, "%s%s:\n\n", prefix, describePast(kind, len(succeeded)))
	return render.Table(e.out, succeeded, e.isTTY, e.width, e.now())
}

// applyAction applies the action to the notifications.
func applyAction(ctx context.Context, c client, kind action.Kind, notifications []inbox.Notification, concurrency int, p progress) (succeeded []inbox.Notification, failed []action.Failure) {
	runner := action.Runner{
		Client:      c,
		Kind:        kind,
		Concurrency: concurrency,
		Progress: func(finished, total int) {
			p.show("%d/%d", finished, total)
		},
	}
	succeeded, failed = runner.Run(ctx, notifications)
	p.clear()
	return succeeded, failed
}

// reportFailed lists the notifications the action failed on, after a
// heading that starts with prefix.
func reportFailed(w io.Writer, prefix string, kind action.Kind, failed []action.Failure) {
	fmt.Fprintf(w, "\n%sFailed to %s:\n", prefix, describe(kind, len(failed)))
	for _, f := range failed {
		fmt.Fprintf(w, "  %s: %s: %v\n", f.Notification.Repo, f.Notification.Title, f.Err)
	}
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
