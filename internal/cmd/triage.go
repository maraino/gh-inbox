package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/maraino/gh-inbox/internal/action"
	"github.com/maraino/gh-inbox/internal/filter"
	"github.com/maraino/gh-inbox/internal/inbox"
	"github.com/maraino/gh-inbox/internal/render"
)

type triageOptions struct {
	filters filter.Options
}

func newTriageCmd(root *rootOptions, e *env) *cobra.Command {
	opts := &triageOptions{}
	cmd := &cobra.Command{
		Use:   "triage [flags]",
		Short: "Go through the notifications that match the filters one at a time",
		Long: `Go through the notifications in your inbox that match the filters one at a
time, most recently updated first, and handle each with a key:

  o  open it in the browser
  d  mark it as done
  r  mark it as read
  u  unsubscribe from it
  s  skip it
  q  quit

Without filters, it goes through them all. Notifications marked as done are
added to the record "gh inbox clean" keeps.
` + filterHelp,
		Example: `  # Pull requests waiting for your review, or your teams'
  gh inbox triage --state open --review-requested @me

  # What's unread in some repositories
  gh inbox triage --repo 'acme/*' --unread`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runTriage(cmd.Context(), root, e, opts)
		},
	}
	addFilterFlags(cmd.Flags(), &opts.filters)
	return cmd
}

// triagePrompt lists the keys triage takes.
const triagePrompt = "[o]pen [d]one [r]ead [u]nsubscribe [s]kip [q]uit: "

// Keys that quit besides q: Ctrl-C and Ctrl-D, which don't send signals
// while the terminal is in raw mode.
const (
	keyCtrlC = 3
	keyCtrlD = 4
)

func runTriage(ctx context.Context, root *rootOptions, e *env, opts *triageOptions) error {
	if e.readKey == nil {
		return errors.New(`triage needs a terminal: use "gh inbox list" and "gh inbox clean" in scripts`)
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
	matched := sel.matched
	if !sel.detailed {
		err := lookUpDetails(ctx, c, matched, false, false, p)
		p.clear()
		if err != nil {
			return err
		}
	}
	if len(matched) == 0 {
		fmt.Fprintln(e.errOut, "No notifications match.")
		return nil
	}

	counts := make(map[action.Kind]int)
	var markedDone []inbox.Notification
	defer func() {
		if done != nil && len(markedDone) > 0 {
			if err := recordDone(done, markedDone, sel.listed); err != nil {
				fmt.Fprintf(e.errOut, "warning: saving the record of notifications marked as done: %v\n", err)
			}
		}
		fmt.Fprintln(e.out)
		handled := 0
		for _, kind := range []action.Kind{action.Done, action.Read, action.Unsubscribe} {
			if counts[kind] > 0 {
				fmt.Fprintf(e.out, "%s.\n", describePast(kind, counts[kind]))
				handled += counts[kind]
			}
		}
		if untouched := len(matched) - handled; untouched > 0 {
			fmt.Fprintf(e.out, "Left %s untouched.\n", notifications(untouched))
		}
		reportUnreadable(e.errOut, sel.unreadable)
	}()

	for i, n := range matched {
		fmt.Fprintf(e.out, "\n(%d/%d) %s\n%s\n", i+1, len(matched), render.Heading(n, e.now()), n.Title)
		url := root.webURL(n)
		fmt.Fprintln(e.out, url)
		fmt.Fprint(e.out, triagePrompt)

	keys:
		for {
			// Ctrl-C pressed while the terminal isn't in raw mode, such as
			// during a request, cancels ctx instead of being read as a key.
			if err := ctx.Err(); err != nil {
				fmt.Fprintln(e.out)
				return err
			}
			key, err := e.readKey()
			switch {
			case errors.Is(err, io.EOF):
				fmt.Fprintln(e.out, "quit")
				return nil
			case err != nil:
				fmt.Fprintln(e.out)
				return fmt.Errorf("reading a key: %w", err)
			}

			var kind action.Kind
			switch key {
			case 'o':
				if err := e.openBrowser(url); err != nil {
					fmt.Fprintf(e.out, "opening the browser failed: %v\n", err)
				} else {
					fmt.Fprintln(e.out, "opened")
				}
				fmt.Fprint(e.out, triagePrompt)
				continue
			case 's':
				fmt.Fprintln(e.out, "skipped")
				break keys
			case 'q', keyCtrlC, keyCtrlD:
				fmt.Fprintln(e.out, "quit")
				return nil
			case 'd':
				kind = action.Done
			case 'r':
				kind = action.Read
			case 'u':
				kind = action.Unsubscribe
			default:
				continue
			}

			if err := action.Apply(ctx, c, kind, n.ID); err != nil {
				fmt.Fprintf(e.out, "failed: %v\n", err)
				if ctx.Err() != nil {
					return ctx.Err()
				}
				fmt.Fprint(e.out, triagePrompt)
				continue
			}
			counts[kind]++
			if kind == action.Done {
				markedDone = append(markedDone, n)
			}
			fmt.Fprintln(e.out, describeOne(kind))
			break keys
		}
	}
	return nil
}

// describeOne says what an action did to a single notification, such as
// "marked as done".
func describeOne(kind action.Kind) string {
	switch kind {
	case action.Done:
		return "marked as done"
	case action.Read:
		return "marked as read"
	default:
		return "unsubscribed"
	}
}
