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
	rules   ruleFlags
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
  p  go back to the previous one
  q  quit, as do Ctrl-C and Ctrl-D

Without filters, it goes through them all. --rule goes through the
notifications that rules from the rules file act on, and --rules through
those of every rule; see "gh inbox clean --help" for the rules.
Notifications marked as done are added to the record "gh inbox clean" keeps.
` + filterHelp,
		Example: `  # Pull requests waiting for your review, or your teams'
  gh inbox triage --state open --review-requested @me

  # What's unread in some repositories
  gh inbox triage --repo 'acme/*' --unread

  # What the rule old-subscribed would clean
  gh inbox triage --rule old-subscribed`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runTriage(cmd.Context(), root, e, opts)
		},
	}
	addFilterFlags(cmd.Flags(), &opts.filters)
	opts.rules.add(cmd.Flags(), "Go through what the rule `name` from the rules file acts on, instead of filters",
		"Go through what every rule in the rules file acts on")
	return cmd
}

// triagePrompt lists the keys triage takes.
const triagePrompt = "[o]pen [d]one [r]ead [u]nsubscribe [s]kip [p]revious [q]uit: "

func runTriage(ctx context.Context, root *rootOptions, e *env, opts *triageOptions) error {
	if e.openKeyboard == nil {
		return errors.New(`triage needs a terminal: use "gh inbox list" and "gh inbox clean" in scripts`)
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
	sels, err := selectEach(ctx, c, filters, done, e.now(), false, p)
	if err != nil {
		return err
	}
	sel := merge(sels)
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

	// Ctrl-C cancels ctx, which stops a request and quits.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	kb, err := e.openKeyboard(cancel)
	if err != nil {
		return fmt.Errorf("reading keys from the terminal: %w", err)
	}
	defer kb.Close()
	out := kb.Out(e.out)

	// handled is the last action applied to each notification; counts and
	// markedDone count every one, including those applied again after
	// going back.
	handled := make([]action.Kind, len(matched))
	counts := make(map[action.Kind]int)
	var markedDone []inbox.Notification
	defer func() {
		if done != nil && len(markedDone) > 0 {
			if err := recordDone(done, markedDone, sel.listed); err != nil {
				fmt.Fprintf(out, "warning: saving the record of notifications marked as done: %v\n", err)
			}
		}
		fmt.Fprintln(out)
		for _, kind := range []action.Kind{action.Done, action.Read, action.Unsubscribe} {
			if counts[kind] > 0 {
				fmt.Fprintf(out, "%s.\n", describePast(kind, counts[kind]))
			}
		}
		untouched := 0
		for _, kind := range handled {
			if kind == "" {
				untouched++
			}
		}
		if untouched > 0 {
			fmt.Fprintf(out, "Left %s untouched.\n", notifications(untouched))
		}
		reportUnreadable(out, sel.unreadable)
	}()

	for i := 0; i < len(matched); {
		n := matched[i]
		fmt.Fprintf(out, "\n(%d/%d) %s\n%s\n", i+1, len(matched), render.Heading(n, e.now()), n.Title)
		url := root.webURL(n)
		fmt.Fprintln(out, url)
		if handled[i] != "" {
			fmt.Fprintf(out, "Already %s.\n", describeOne(handled[i]))
		}
		fmt.Fprint(out, triagePrompt)

		next, err := triageOne(ctx, e, c, kb, out, n, url, func(kind action.Kind) {
			handled[i] = kind
			counts[kind]++
			if kind == action.Done {
				markedDone = append(markedDone, n)
			}
		})
		if err != nil || next == 0 {
			return err
		}
		switch {
		case next < 0 && i == 0:
			fmt.Fprintln(out, "This is the first one.")
		case next < 0:
			i--
		default:
			i++
		}
	}
	return nil
}

// triageOne reads keys until one moves to another notification, and returns
// 1 to move to the next one, -1 to the previous one, and 0 to quit. The
// applied is called with each action applied to n.
func triageOne(ctx context.Context, e *env, c client, kb keyboard, out io.Writer, n inbox.Notification, url string, applied func(action.Kind)) (int, error) {
	for {
		key, err := kb.ReadKey(ctx)
		switch {
		case errors.Is(err, io.EOF) || (err != nil && ctx.Err() != nil):
			fmt.Fprintln(out, "quit")
			return 0, nil
		case err != nil:
			fmt.Fprintln(out)
			return 0, fmt.Errorf("reading a key: %w", err)
		}

		var kind action.Kind
		switch key {
		case 'o':
			if err := e.openBrowser(url); err != nil {
				fmt.Fprintf(out, "opening the browser failed: %v\n", err)
			} else {
				fmt.Fprintln(out, "opened")
			}
			fmt.Fprint(out, triagePrompt)
			continue
		case 's':
			fmt.Fprintln(out, "skipped")
			return 1, nil
		case 'p':
			fmt.Fprintln(out, "previous")
			return -1, nil
		case 'q', keyCtrlC, keyCtrlD:
			fmt.Fprintln(out, "quit")
			return 0, nil
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
			if ctx.Err() != nil {
				fmt.Fprintln(out, "interrupted")
				return 0, nil
			}
			fmt.Fprintf(out, "failed: %v\n", err)
			fmt.Fprint(out, triagePrompt)
			continue
		}
		applied(kind)
		fmt.Fprintln(out, describeOne(kind))
		return 1, nil
	}
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
