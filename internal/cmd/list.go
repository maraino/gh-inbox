package cmd

import (
	"context"
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/maraino/gh-inbox/internal/filter"
	"github.com/maraino/gh-inbox/internal/render"
)

type listOptions struct {
	filters filter.Options
	summary bool
	json    bool
	limit   int
}

func newListCmd(root *rootOptions, e *env) *cobra.Command {
	opts := &listOptions{}
	cmd := &cobra.Command{
		Use:   "list [flags]",
		Short: "List the notifications that match the filters",
		Long: `List the notifications in your inbox that match the filters, most recently
updated first. Without filters, it lists them all.
` + filterHelp,
		Example: `  # How many notifications there are, by repository, type, reason and state
  gh inbox list --summary

  # Open pull requests waiting for your review, or your teams'
  gh inbox list --state open --review-requested @me

  # CI notifications older than a week, as JSON
  gh inbox list --type ci --older-than 7d --json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runList(cmd.Context(), root, e, opts)
		},
	}

	flags := cmd.Flags()
	addFilterFlags(flags, &opts.filters)
	flags.BoolVar(&opts.summary, "summary", false, "Show counts by repository, type, reason and state")
	flags.BoolVar(&opts.json, "json", false, "Write the notifications as JSON")
	flags.IntVar(&opts.limit, "limit", 0, "Show at most `n` notifications (default: all)")
	cmd.MarkFlagsMutuallyExclusive("summary", "json")
	return cmd
}

func runList(ctx context.Context, root *rootOptions, e *env, opts *listOptions) error {
	if opts.limit < 0 {
		return errors.New("--limit can't be negative")
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
	sel, err := selectNotifications(ctx, c, &opts.filters, done, e.now(), opts.json, p)
	if err != nil {
		return err
	}
	matched := sel.matched
	if opts.limit > 0 && len(matched) > opts.limit {
		matched = matched[:opts.limit]
	}

	// The table and JSON show the state and people of pull requests and
	// issues, so look them up when the filters didn't need them.
	if !opts.summary && !sel.detailed {
		err := lookUpDetails(ctx, c, matched, false, opts.json, p)
		p.clear()
		if err != nil {
			return err
		}
	}

	switch {
	case opts.json:
		err = render.JSON(e.out, matched)
	case opts.summary:
		err = render.Summary(e.out, matched)
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
