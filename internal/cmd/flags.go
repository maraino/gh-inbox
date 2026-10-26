package cmd

import (
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/pflag"

	"github.com/maraino/gh-inbox/internal/filter"
	"github.com/maraino/gh-inbox/internal/inbox"
)

// addFilterFlags adds the flags for the filters in o.
func addFilterFlags(fs *pflag.FlagSet, o *filter.Options) {
	fs.SortFlags = false

	fs.StringSliceVar(&o.Repos, "repo", nil, "Only repositories matching `owner/name`; * is a wildcard")
	fs.StringSliceVar(&o.Types, "type", nil, "Only subjects of `type`: "+strings.Join(inbox.TypeNames(), ", "))
	fs.StringSliceVar(&o.Reasons, "reason", nil, "Only notifications sent for `reason`, such as review_requested or subscribed")
	fs.StringSliceVar(&o.States, "state", nil, "Only pull requests and issues in `state`: open, closed, merged")
	fs.BoolVar(&o.Unread, "unread", false, "Only unread notifications")
	fs.BoolVar(&o.Read, "read", false, "Only read notifications")
	fs.Var((*ageValue)(&o.OlderThan), "older-than", "Only notifications without activity for `age`, such as 36h, 7d or 2w")
	fs.Var((*ageValue)(&o.NewerThan), "newer-than", "Only notifications with activity in the last `age`, such as 24h or 7d")
	fs.BoolVar(&o.Draft, "draft", false, "Only draft pull requests")
	fs.BoolVar(&o.NotDraft, "not-draft", false, "Only pull requests that aren't drafts, and issues")
	fs.StringSliceVar(&o.Labels, "label", nil, "Only pull requests and issues with the `label`")
	fs.StringSliceVar(&o.NotLabels, "not-label", nil, "Only pull requests and issues without the `label`")

	fs.StringSliceVar(&o.Authors, "author", nil, "Only pull requests and issues opened by `user`")
	fs.StringSliceVar(&o.NotAuthors, "not-author", nil, "Only pull requests and issues not opened by `user`")
	fs.StringSliceVar(&o.AssignedTo, "assigned-to", nil, "Only pull requests and issues assigned to `user`")
	fs.StringSliceVar(&o.NotAssignedTo, "not-assigned-to", nil, "Only pull requests and issues not assigned to `user`")
	fs.StringSliceVar(&o.ReviewRequested, "review-requested", nil,
		"Only pull requests whose review is requested from `user|team`")
	fs.StringSliceVar(&o.NoReviewRequested, "no-review-requested", nil,
		"Only pull requests and issues whose review isn't requested from `user|team`")
	fs.StringSliceVar(&o.Involves, "involves", nil,
		"Only pull requests and issues assigned to, or awaiting review from, `user|team`")
	fs.StringSliceVar(&o.NotInvolves, "not-involves", nil,
		"Only pull requests and issues neither assigned to, nor awaiting review from, `user|team`")
}

// filterHelp explains how the filters combine. It's appended to the help of
// the commands that take filters.
const filterHelp = `
Filters with several values, given as a comma-separated list or by repeating
the flag, match when any value matches. Negated filters, such as --not-author,
match when no value matches. A notification must match every filter given.

Filters on people take a login, or @me for you. Filters on review requests
also take a team, as org/team; for them, @me includes your teams. A review
request is dropped once the review is submitted, so --review-requested @me
matches the reviews you still owe.

Filters on the state or the people of a pull request or issue never match
other notifications, such as CI runs or releases, nor pull requests and issues
that can't be read anymore.`

// ageValue is a duration flag that also accepts days and weeks, such as "7d"
// or "2w".
type ageValue time.Duration

func (a *ageValue) String() string {
	if *a == 0 {
		return ""
	}
	return time.Duration(*a).String()
}

func (a *ageValue) Set(s string) error {
	d, err := parseAge(s)
	if err != nil {
		return err
	}
	*a = ageValue(d)
	return nil
}

func (a *ageValue) Type() string {
	return "age"
}

// parseAge parses a positive duration: a number of days ("7d") or weeks
// ("2w"), or a Go duration ("36h").
func parseAge(s string) (time.Duration, error) {
	const day = 24 * time.Hour
	var d time.Duration
	if n, unit, ok := cutUnit(s); ok {
		days, err := strconv.Atoi(n)
		if err != nil {
			return 0, errors.New("want a duration such as 36h, 7d or 2w")
		}
		d = time.Duration(days) * unit * day
	} else {
		var err error
		if d, err = time.ParseDuration(s); err != nil {
			return 0, errors.New("want a duration such as 36h, 7d or 2w")
		}
	}
	if d <= 0 {
		return 0, errors.New("the age must be positive")
	}
	return d, nil
}

// cutUnit splits "7d" into "7" and 1, and "2w" into "2" and 7.
func cutUnit(s string) (n string, days time.Duration, ok bool) {
	if n, ok := strings.CutSuffix(s, "d"); ok {
		return n, 1, true
	}
	if n, ok := strings.CutSuffix(s, "w"); ok {
		return n, 7, true
	}
	return "", 0, false
}
