package cmd

import (
	"cmp"
	"fmt"
	"slices"
	"strings"

	"github.com/spf13/pflag"

	"github.com/maraino/gh-inbox/internal/inbox"
)

// sortKeys compare notifications by each key --sort takes.
var sortKeys = map[string]func(a, b inbox.Notification) int{
	"updated": func(a, b inbox.Notification) int { return b.UpdatedAt.Compare(a.UpdatedAt) },
	"repo": func(a, b inbox.Notification) int {
		return strings.Compare(strings.ToLower(a.Repo), strings.ToLower(b.Repo))
	},
	"type":   func(a, b inbox.Notification) int { return cmp.Compare(a.Type.ShortName(), b.Type.ShortName()) },
	"reason": func(a, b inbox.Notification) int { return cmp.Compare(a.Reason, b.Reason) },
	"state":  func(a, b inbox.Notification) int { return cmp.Compare(stateRank(a), stateRank(b)) },
}

// stateRank orders the states of pull requests and issues: open, draft,
// merged, closed, and unknown.
func stateRank(n inbox.Notification) int {
	switch {
	case n.Subject == nil:
		return 4
	case n.Subject.State == inbox.StateOpen && n.Subject.Draft:
		return 1
	case n.Subject.State == inbox.StateOpen:
		return 0
	case n.Subject.State == inbox.StateMerged:
		return 2
	case n.Subject.State == inbox.StateClosed:
		return 3
	default:
		return 4
	}
}

// sortFlags are the flags that order the notifications.
type sortFlags struct {
	keys    []string
	reverse bool
}

func (f *sortFlags) add(fs *pflag.FlagSet) {
	fs.StringSliceVar(&f.keys, "sort", nil,
		"Sort by `key`: updated (newest first), repo, type, reason or state; later keys break ties (default: updated)")
	fs.BoolVar(&f.reverse, "reverse", false, "Reverse the order")
}

// given reports whether an order was chosen.
func (f *sortFlags) given() bool {
	return len(f.keys) > 0 || f.reverse
}

// check reports a key --sort doesn't take.
func (f *sortFlags) check() error {
	for _, k := range f.keys {
		if sortKeys[strings.ToLower(k)] == nil {
			return fmt.Errorf("invalid --sort key %q: want updated, repo, type, reason or state", k)
		}
	}
	return nil
}

// needsDetails reports whether sorting needs the details of pull requests
// and issues.
func (f *sortFlags) needsDetails() bool {
	return slices.ContainsFunc(f.keys, func(k string) bool { return strings.EqualFold(k, "state") })
}

// sort orders the notifications by the keys, and then by the most recently
// updated. Without keys, it keeps the order of the inbox, which is the most
// recently updated first.
func (f *sortFlags) sort(notifications []inbox.Notification) {
	if len(f.keys) > 0 {
		compare := make([]func(a, b inbox.Notification) int, 0, len(f.keys)+1)
		for _, k := range f.keys {
			compare = append(compare, sortKeys[strings.ToLower(k)])
		}
		compare = append(compare, sortKeys["updated"])
		slices.SortStableFunc(notifications, func(a, b inbox.Notification) int {
			for _, c := range compare {
				if r := c(a, b); r != 0 {
					return r
				}
			}
			return 0
		})
	}
	if f.reverse {
		slices.Reverse(notifications)
	}
}
