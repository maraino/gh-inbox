package cmd

import (
	"context"
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/maraino/gh-inbox/internal/filter"
	"github.com/maraino/gh-inbox/internal/inbox"
	"github.com/maraino/gh-inbox/internal/render"
)

type listOptions struct {
	filters filter.Options
	summary bool
	json    bool
	limit   int
	rules   ruleFlags
	sort    sortFlags
}

func newListCmd(root *rootOptions, e *env) *cobra.Command {
	opts := &listOptions{}
	cmd := &cobra.Command{
		Use:   "list [flags]",
		Short: "List the notifications that match the filters",
		Long: `List the notifications in your inbox that match the filters, most recently
updated first, or in the order --sort gives. Without filters, it lists them
all.

--rule lists the notifications that rules from the rules file would act on,
and --rules those of every rule. See "gh inbox clean --help" for the rules.
` + filterHelp,
		Example: `  # How many notifications there are, by repository, type, reason and state
  gh inbox list --summary

  # Open pull requests waiting for your review, or your teams'
  gh inbox list --state open --review-requested @me

  # Pull requests by repository, and then by state
  gh inbox list --type pr --sort repo,state

  # CI notifications older than a week, as JSON
  gh inbox list --type ci --older-than 7d --json

  # What the rule old-subscribed would clean
  gh inbox list --rule old-subscribed`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runList(cmd.Context(), root, e, opts)
		},
	}

	flags := cmd.Flags()
	addFilterFlags(flags, &opts.filters)
	opts.sort.add(flags)
	opts.rules.add(flags, "List what the rule `name` from the rules file acts on, instead of filters",
		"List what every rule in the rules file acts on")
	flags.IntVar(&opts.limit, "limit", 0, "Show at most `n` notifications (default: all)")
	flags.BoolVar(&opts.json, "json", false, "Write the notifications as JSON")
	flags.BoolVar(&opts.summary, "summary", false, "Show counts by repository, type, reason and state")
	cmd.MarkFlagsMutuallyExclusive("summary", "json")
	cmd.MarkFlagsMutuallyExclusive("summary", "sort")
	cmd.MarkFlagsMutuallyExclusive("summary", "reverse")
	return cmd
}

func runList(ctx context.Context, root *rootOptions, e *env, opts *listOptions) error {
	if opts.limit < 0 {
		return errors.New("--limit can't be negative")
	}
	if err := opts.sort.check(); err != nil {
		return err
	}
	filters := []*filter.Options{&opts.filters}
	if opts.rules.given() {
		if !opts.filters.IsEmpty() {
			return errors.New("rules can't be combined with filters")
		}
		rules, err := opts.rules.load(e)
		if err != nil {
			return err
		}
		filters = ruleFilters(rules)
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
	// Only the JSON shows the teams whose review is requested.
	sels, err := selectEach(ctx, c, filters, done, e.now(), opts.json, p)
	if err != nil {
		return err
	}
	sel := merge(sels)
	matched := sel.matched

	// The table and JSON show the state and people of pull requests and
	// issues, so look them up when the filters didn't need them: before
	// sorting when the order needs them, and otherwise only for those shown.
	lookUp := func() error {
		err := lookUpDetails(ctx, c, matched, false, opts.json, p)
		p.clear()
		return err
	}
	needDetails := !opts.summary && !sel.detailed
	if needDetails && opts.sort.needsDetails() {
		if err := lookUp(); err != nil {
			return err
		}
		needDetails = false
	}
	opts.sort.sort(matched)
	if opts.limit > 0 && len(matched) > opts.limit {
		matched = matched[:opts.limit]
	}
	if needDetails {
		if err := lookUp(); err != nil {
			return err
		}
	}

	switch {
	case opts.json:
		err = render.JSON(e.out, matched)
	case opts.summary:
		err = render.Summary(e.out, matched)
		hintUnwatch(e, matched)
	case len(matched) == 0:
		fmt.Fprintln(e.errOut, "No notifications match.")
	default:
		err = render.Table(e.out, matched, e.isTTY, e.width, e.now())
	}
	if err != nil {
		return err
	}
	reportUnreadable(e.errOut, sel.unreadable)
	return nil
}

// hintUnwatch points to "gh inbox unwatch" when a person reading a summary
// has notifications from repositories they watch.
func hintUnwatch(e *env, matched []inbox.Notification) {
	if !e.showProgress {
		return
	}
	n := 0
	for _, m := range matched {
		if m.Reason == reasonWatching {
			n++
		}
	}
	if n > 0 {
		verb := "are"
		if n == 1 {
			verb = "is"
		}
		fmt.Fprintf(e.errOut, "\n%s, with reason %q, %s from repositories you watch. \"gh inbox unwatch\" shows which ones.\n",
			notifications(n), reasonWatching, verb)
	}
}
