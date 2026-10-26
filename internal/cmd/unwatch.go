package cmd

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"slices"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/maraino/gh-inbox/internal/inbox"
)

// reasonWatching is the reason GitHub gives for notifications from
// repositories you watch.
const reasonWatching = "subscribed"

type unwatchOptions struct {
	apply bool
}

func newUnwatchCmd(root *rootOptions, e *env) *cobra.Command {
	opts := &unwatchOptions{}
	cmd := &cobra.Command{
		Use:   "unwatch [owner/name...] [flags]",
		Short: "Stop watching the repositories that fill the inbox",
		Long: `Stop watching repositories, so that only the threads you take part in or
are mentioned in notify you.

Notifications sent because you watch a repository have the reason
"subscribed". Without arguments, unwatch lists the repositories they come
from, most first. With arguments, it shows which of them it would stop
watching, and does it with --apply. An argument can have * wildcards, which
match the repositories in the inbox with "subscribed" notifications.

The notifications already in the inbox stay there: use "gh inbox clean"
to clean them up.`,
		Example: `  # Which repositories you watch fill the inbox
  gh inbox unwatch

  # Stop watching two of them
  gh inbox unwatch acme/infra acme/docs --apply

  # Then mark their notifications as done
  gh inbox clean --repo acme/infra,acme/docs --reason subscribed --apply`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runUnwatch(cmd.Context(), root, e, opts, args)
		},
	}
	cmd.Flags().BoolVar(&opts.apply, "apply", false, "Stop watching the repositories; without it, only show what it would do")
	return cmd
}

// watched is a repository and how many "subscribed" notifications it has in
// the inbox.
type watched struct {
	repo string
	n    int
}

func runUnwatch(ctx context.Context, root *rootOptions, e *env, opts *unwatchOptions, args []string) error {
	for _, a := range args {
		if _, err := path.Match(a, ""); err != nil || strings.Count(a, "/") != 1 || strings.HasPrefix(a, "/") || strings.HasSuffix(a, "/") {
			return fmt.Errorf("invalid repository %q: want owner/name, with optional * wildcards", a)
		}
	}
	if len(args) == 0 && opts.apply {
		return errors.New("name the repositories to stop watching: run without --apply to see which ones fill the inbox")
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
	p.show("Listing notifications…")
	listed, err := c.Notifications(ctx)
	p.clear()
	if err != nil {
		return fmt.Errorf("listing notifications: %w", err)
	}
	var inInbox []inbox.Notification
	for _, n := range listed {
		if done == nil || !done.IsDone(n.ID, n.UpdatedAt) {
			inInbox = append(inInbox, n)
		}
	}
	repos := watchedRepos(inInbox)

	if len(args) == 0 {
		if len(repos) == 0 {
			fmt.Fprintln(e.errOut, "No notifications come from repositories you watch.")
			return nil
		}
		verb := "come"
		if total(repos) == 1 {
			verb = "comes"
		}
		fmt.Fprintf(e.out, "%s %s from %s you watch:\n\n", notifications(total(repos)), verb, repositories(len(repos)))
		if err := writeWatched(e.out, repos); err != nil {
			return err
		}
		fmt.Fprintln(e.out, "\nRun \"gh inbox unwatch owner/name... --apply\" to stop watching some of them.")
		return nil
	}

	selected, err := selectWatched(repos, args)
	if err != nil {
		return err
	}
	if !opts.apply {
		fmt.Fprintf(e.out, "Would stop watching %s:\n\n", repositories(len(selected)))
		if err := writeWatched(e.out, selected); err != nil {
			return err
		}
		fmt.Fprintln(e.out, "\nRun again with --apply to do it.")
		return nil
	}

	var failed int
	for _, w := range selected {
		if err := c.Unwatch(ctx, w.repo); err != nil {
			fmt.Fprintf(e.errOut, "Failed to stop watching %s: %v\n", w.repo, err)
			failed++
			continue
		}
		fmt.Fprintf(e.out, "Stopped watching %s.\n", w.repo)
	}
	if left := total(selected); left > 0 {
		names := make([]string, len(selected))
		for i, w := range selected {
			names[i] = w.repo
		}
		fmt.Fprintf(e.out, "\nTheir %s stay in the inbox. To clean them up:\n  gh inbox clean --repo %s --reason %s --apply\n",
			notifications(left), strings.Join(names, ","), reasonWatching)
	}
	if failed > 0 {
		return fmt.Errorf("%d of %d failed", failed, len(selected))
	}
	return nil
}

// watchedRepos counts the "subscribed" notifications by repository, most
// first.
func watchedRepos(notifications []inbox.Notification) []watched {
	counts := make(map[string]int)
	for _, n := range notifications {
		if n.Reason == reasonWatching {
			counts[n.Repo]++
		}
	}
	repos := make([]watched, 0, len(counts))
	for repo, n := range counts {
		repos = append(repos, watched{repo: repo, n: n})
	}
	slices.SortFunc(repos, func(a, b watched) int {
		return cmp.Or(cmp.Compare(b.n, a.n), cmp.Compare(a.repo, b.repo))
	})
	return repos
}

// selectWatched returns the repositories the arguments name. A name without
// wildcards is selected even when it has no notifications in the inbox; a
// pattern must match one that has.
func selectWatched(repos []watched, args []string) ([]watched, error) {
	var selected []watched
	seen := make(map[string]bool)
	add := func(w watched) {
		if !seen[strings.ToLower(w.repo)] {
			seen[strings.ToLower(w.repo)] = true
			selected = append(selected, w)
		}
	}
	for _, a := range args {
		if !strings.Contains(a, "*") {
			w := watched{repo: a}
			if i := slices.IndexFunc(repos, func(r watched) bool { return strings.EqualFold(r.repo, a) }); i >= 0 {
				w = repos[i]
			}
			add(w)
			continue
		}
		pattern := strings.ToLower(a)
		matched := false
		for _, r := range repos {
			if ok, _ := path.Match(pattern, strings.ToLower(r.repo)); ok {
				add(r)
				matched = true
			}
		}
		if !matched {
			return nil, fmt.Errorf("%q matches no repository with %q notifications in the inbox", a, reasonWatching)
		}
	}
	return selected, nil
}

func writeWatched(w io.Writer, repos []watched) error {
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	for _, r := range repos {
		fmt.Fprintf(tw, "  %d\t%s\n", r.n, r.repo)
	}
	return tw.Flush()
}

func total(repos []watched) int {
	n := 0
	for _, r := range repos {
		n += r.n
	}
	return n
}

// repositories returns "1 repository" or "n repositories".
func repositories(n int) string {
	if n == 1 {
		return "1 repository"
	}
	return fmt.Sprintf("%d repositories", n)
}
