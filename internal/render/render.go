// Package render writes notifications as a table, a summary or JSON.
package render

import (
	"cmp"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"text/tabwriter"
	"time"

	"github.com/cli/go-gh/v2/pkg/tableprinter"

	"github.com/maraino/gh-inbox/internal/inbox"
)

// Table writes a row per notification. On a terminal, the columns are
// aligned and fit in width; otherwise they are separated by tabs.
func Table(w io.Writer, notifications []inbox.Notification, isTTY bool, width int, now time.Time) error {
	tp := tableprinter.New(w, isTTY, width)
	tp.AddHeader([]string{"REPO", "NUMBER", "TYPE", "STATE", "REASON", "UPDATED", "TITLE"})
	for _, n := range notifications {
		tp.AddField(n.Repo)
		tp.AddField(number(n))
		tp.AddField(n.Type.ShortName())
		tp.AddField(state(n))
		tp.AddField(n.Reason)
		tp.AddField(updated(n.UpdatedAt, isTTY, now))
		tp.AddField(n.Title)
		tp.EndRow()
	}
	return tp.Render()
}

// maxRepos is how many repositories a summary lists.
const maxRepos = 15

// Summary writes how many notifications there are by repository, type,
// reason and state.
func Summary(w io.Writer, notifications []inbox.Notification) error {
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	if len(notifications) == 1 {
		fmt.Fprintln(tw, "1 notification")
	} else {
		fmt.Fprintf(tw, "%d notifications\n", len(notifications))
	}
	if len(notifications) == 0 {
		return tw.Flush()
	}

	groups := []struct {
		title string
		key   func(inbox.Notification) string
		limit int
	}{
		{title: "By repository", key: func(n inbox.Notification) string { return n.Repo }, limit: maxRepos},
		{title: "By type", key: func(n inbox.Notification) string { return n.Type.ShortName() }},
		{title: "By reason", key: func(n inbox.Notification) string { return n.Reason }},
		{title: "By state", key: func(n inbox.Notification) string {
			if n.Subject == nil {
				return "(none)"
			}
			return string(n.Subject.State)
		}},
	}
	for _, g := range groups {
		counts := countBy(notifications, g.key)
		fmt.Fprintf(tw, "\n%s\n", g.title)
		shown := counts
		if g.limit > 0 && len(counts) > g.limit {
			shown = counts[:g.limit]
		}
		for _, c := range shown {
			fmt.Fprintf(tw, "  %d\t%s\n", c.n, c.key)
		}
		if len(shown) < len(counts) {
			fmt.Fprintf(tw, "  …\tand %d more\n", len(counts)-len(shown))
		}
	}
	return tw.Flush()
}

type count struct {
	key string
	n   int
}

// countBy counts the notifications by key, most common first.
func countBy(notifications []inbox.Notification, key func(inbox.Notification) string) []count {
	byKey := make(map[string]int)
	for _, n := range notifications {
		byKey[key(n)]++
	}
	counts := make([]count, 0, len(byKey))
	for k, n := range byKey {
		counts = append(counts, count{key: k, n: n})
	}
	slices.SortFunc(counts, func(a, b count) int {
		return cmp.Or(cmp.Compare(b.n, a.n), cmp.Compare(a.key, b.key))
	})
	return counts
}

type jsonNotification struct {
	ID         string       `json:"id"`
	Repository string       `json:"repository"`
	Type       string       `json:"type"`
	Number     int          `json:"number,omitempty"`
	Title      string       `json:"title"`
	Reason     string       `json:"reason"`
	Unread     bool         `json:"unread"`
	UpdatedAt  time.Time    `json:"updated_at"`
	URL        string       `json:"url,omitempty"`
	Subject    *jsonSubject `json:"subject,omitempty"`
}

type jsonSubject struct {
	Number      int      `json:"number"`
	State       string   `json:"state"`
	URL         string   `json:"url"`
	Author      string   `json:"author"`
	Assignees   []string `json:"assignees"`
	ReviewUsers []string `json:"review_requested_users"`
	ReviewTeams []string `json:"review_requested_teams"`
}

// JSON writes the notifications as a JSON array.
func JSON(w io.Writer, notifications []inbox.Notification) error {
	out := make([]jsonNotification, len(notifications))
	for i, n := range notifications {
		out[i] = jsonNotification{
			ID:         n.ID,
			Repository: n.Repo,
			Type:       n.Type.ShortName(),
			Number:     n.Number,
			Title:      n.Title,
			Reason:     n.Reason,
			Unread:     n.Unread,
			UpdatedAt:  n.UpdatedAt,
			URL:        n.URL,
		}
		if s := n.Subject; s != nil {
			out[i].Subject = &jsonSubject{
				Number:      s.Number,
				State:       string(s.State),
				URL:         s.URL,
				Author:      s.Author,
				Assignees:   nonNil(s.Assignees),
				ReviewUsers: nonNil(s.ReviewUsers),
				ReviewTeams: nonNil(s.ReviewTeams),
			}
		}
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

// nonNil returns s, or an empty slice for nil, so JSON has [] and not null.
func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// number returns the number of the notification's pull request or issue,
// such as "#7", or "-" for other types.
func number(n inbox.Notification) string {
	if n.Number == 0 {
		return "-"
	}
	return fmt.Sprintf("#%d", n.Number)
}

// state returns the state of the notification's subject, or "-" when it's
// unknown.
func state(n inbox.Notification) string {
	if n.Subject == nil {
		return "-"
	}
	return string(n.Subject.State)
}

// updated formats a time as how long ago it was on a terminal, and in RFC
// 3339 otherwise.
func updated(t time.Time, isTTY bool, now time.Time) string {
	if !isTTY {
		return t.UTC().Format(time.RFC3339)
	}
	switch d := now.Sub(t); {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 365*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	default:
		return fmt.Sprintf("%dy ago", int(d.Hours()/24/365))
	}
}
