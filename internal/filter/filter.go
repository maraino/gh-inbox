// Package filter decides which notifications a command acts on.
//
// A notification matches when it matches every filter given. A filter with
// several values matches when any of them does, and its negated form matches
// when none of them does.
package filter

import (
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/maraino/gh-inbox/internal/inbox"
)

// Me is the value that stands for the authenticated user, and for their
// teams where teams are allowed.
const Me = "@me"

// Options are the filters, as given on the command line.
type Options struct {
	Repos     []string
	Types     []string
	Reasons   []string
	States    []string
	Unread    bool
	Read      bool
	OlderThan time.Duration
	NewerThan time.Duration

	Authors           []string
	NotAuthors        []string
	AssignedTo        []string
	NotAssignedTo     []string
	ReviewRequested   []string
	NoReviewRequested []string
	Involves          []string
	NotInvolves       []string
}

// IsEmpty reports whether no filter is set.
func (o *Options) IsEmpty() bool {
	return len(o.Repos) == 0 && len(o.Types) == 0 && len(o.Reasons) == 0 && len(o.States) == 0 &&
		!o.Unread && !o.Read && o.OlderThan == 0 && o.NewerThan == 0 && len(o.people()) == 0
}

// NeedsIdentity reports whether a filter refers to the authenticated user.
func (o *Options) NeedsIdentity() bool {
	return slices.ContainsFunc(o.people(), func(p string) bool { return strings.EqualFold(p, Me) })
}

// people returns every value of the filters on people.
func (o *Options) people() []string {
	return slices.Concat(o.Authors, o.NotAuthors, o.AssignedTo, o.NotAssignedTo,
		o.ReviewRequested, o.NoReviewRequested, o.Involves, o.NotInvolves)
}

// Env is what the filters need besides the notifications.
type Env struct {
	// Now is the time --older-than and --newer-than count back from.
	Now time.Time
	// Me is the authenticated user. It's only needed when a filter refers
	// to @me.
	Me inbox.Identity
}

// Set is a compiled set of filters.
type Set struct {
	// basic filters only need the notification itself.
	basic []func(inbox.Notification) bool
	// detailed filters need the details of the pull request or issue.
	detailed []func(*inbox.Subject) bool
	// reviewTeams reports whether a detailed filter needs the teams whose
	// review is requested.
	reviewTeams bool
}

// New compiles the filters in o.
func New(o *Options, env Env) (*Set, error) {
	b := builder{env: env}
	b.repos(o.Repos)
	b.types(o.Types)
	b.reasons(o.Reasons)
	b.states(o.States)
	b.unread(o.Unread, o.Read)
	b.age(o.OlderThan, o.NewerThan)
	b.people("author", o.Authors, author)
	b.people("not-author", o.NotAuthors, author.not())
	b.people("assigned-to", o.AssignedTo, assignee)
	b.people("not-assigned-to", o.NotAssignedTo, assignee.not())
	b.people("review-requested", o.ReviewRequested, reviewer)
	b.people("no-review-requested", o.NoReviewRequested, reviewer.not())
	b.people("involves", o.Involves, involved)
	b.people("not-involves", o.NotInvolves, involved.not())
	if err := errors.Join(b.errs...); err != nil {
		return nil, err
	}
	return &b.set, nil
}

// NeedsDetails reports whether a filter needs the details of pull requests
// and issues.
func (s *Set) NeedsDetails() bool {
	return len(s.detailed) > 0
}

// NeedsReviewTeams reports whether a filter needs the teams whose review is
// requested, which are only looked up when needed: reading them needs the
// read:org scope.
func (s *Set) NeedsReviewTeams() bool {
	return s.reviewTeams
}

// MatchBasic reports whether n matches the filters that don't need details.
func (s *Set) MatchBasic(n inbox.Notification) bool {
	for _, match := range s.basic {
		if !match(n) {
			return false
		}
	}
	return true
}

// Match reports whether n matches every filter. A notification without
// details never matches a filter that needs them.
func (s *Set) Match(n inbox.Notification) bool {
	if !s.MatchBasic(n) {
		return false
	}
	if s.NeedsDetails() && n.Subject == nil {
		return false
	}
	for _, match := range s.detailed {
		if !match(n.Subject) {
			return false
		}
	}
	return true
}

// builder accumulates compiled filters and the errors in their values.
type builder struct {
	env  Env
	set  Set
	errs []error
}

func (b *builder) errorf(format string, args ...any) {
	b.errs = append(b.errs, fmt.Errorf(format, args...))
}

func (b *builder) repos(patterns []string) {
	if len(patterns) == 0 {
		return
	}
	lower := make([]string, len(patterns))
	for i, p := range patterns {
		if _, err := path.Match(p, ""); err != nil || strings.Count(p, "/") != 1 {
			b.errorf("--repo: invalid pattern %q: want owner/name, with optional * wildcards", p)
		}
		lower[i] = strings.ToLower(p)
	}
	b.set.basic = append(b.set.basic, func(n inbox.Notification) bool {
		repo := strings.ToLower(n.Repo)
		return slices.ContainsFunc(lower, func(p string) bool {
			ok, _ := path.Match(p, repo)
			return ok
		})
	})
}

func (b *builder) types(names []string) {
	if len(names) == 0 {
		return
	}
	types := make([]inbox.SubjectType, 0, len(names))
	for _, name := range names {
		t, err := inbox.ParseType(name)
		if err != nil {
			b.errorf("--type: %w", err)
		}
		types = append(types, t)
	}
	b.set.basic = append(b.set.basic, func(n inbox.Notification) bool {
		return slices.Contains(types, n.Type)
	})
}

// reasons are the reasons GitHub gives for a notification.
var reasons = []string{
	"approval_requested", "assign", "author", "ci_activity", "comment", "invitation", "manual",
	"member_feature_requested", "mention", "review_requested", "security_advisory_credit",
	"security_alert", "state_change", "subscribed", "team_mention",
}

func (b *builder) reasons(values []string) {
	if len(values) == 0 {
		return
	}
	for _, r := range values {
		if !slices.Contains(reasons, r) {
			b.errorf("--reason: unknown reason %q: valid reasons are %s", r, strings.Join(reasons, ", "))
		}
	}
	b.set.basic = append(b.set.basic, func(n inbox.Notification) bool {
		return slices.Contains(values, n.Reason)
	})
}

func (b *builder) states(names []string) {
	if len(names) == 0 {
		return
	}
	states := make([]inbox.State, 0, len(names))
	for _, name := range names {
		s, err := inbox.ParseState(name)
		if err != nil {
			b.errorf("--state: %w", err)
		}
		states = append(states, s)
	}
	b.set.detailed = append(b.set.detailed, func(s *inbox.Subject) bool {
		return slices.Contains(states, s.State)
	})
}

func (b *builder) unread(unread, read bool) {
	switch {
	case unread && read:
		b.errorf("--unread and --read can't be used together")
	case unread || read:
		b.set.basic = append(b.set.basic, func(n inbox.Notification) bool {
			return n.Unread == unread
		})
	}
}

func (b *builder) age(olderThan, newerThan time.Duration) {
	switch {
	case olderThan < 0:
		b.errorf("--older-than: the age can't be negative")
	case olderThan > 0:
		cutoff := b.env.Now.Add(-olderThan)
		b.set.basic = append(b.set.basic, func(n inbox.Notification) bool {
			return n.UpdatedAt.Before(cutoff)
		})
	}
	switch {
	case newerThan < 0:
		b.errorf("--newer-than: the age can't be negative")
	case newerThan > 0 && olderThan >= newerThan:
		b.errorf("--newer-than must be longer than --older-than, or nothing matches")
	case newerThan > 0:
		cutoff := b.env.Now.Add(-newerThan)
		b.set.basic = append(b.set.basic, func(n inbox.Notification) bool {
			return n.UpdatedAt.After(cutoff)
		})
	}
}

// person is a user or a set of teams, as given to a filter on people.
type person struct {
	// user is a login, or "" when only teams are given.
	user string
	// teams are "org/team" names.
	teams []string
}

// relation is how a person relates to a pull request or issue.
type relation struct {
	match func(person, *inbox.Subject) bool
	// teams reports whether the relation can apply to a team.
	teams bool
	// negate turns a filter on the relation into one on its absence.
	negate bool
}

// The relations the filters on people match. Teams can be requested
// reviewers, but not authors or assignees.
var (
	author   = relation{match: isAuthor}
	assignee = relation{match: isAssignee}
	reviewer = relation{match: isReviewer, teams: true}
	involved = relation{match: involves, teams: true}
)

// not returns the negated relation, which matches subjects that match none
// of the people given.
func (r relation) not() relation {
	r.negate = !r.negate
	return r
}

// people adds a filter on the people related to a subject.
func (b *builder) people(flag string, values []string, r relation) {
	if len(values) == 0 {
		return
	}
	people := make([]person, 0, len(values))
	for _, v := range values {
		p, err := b.parsePerson(v, r.teams)
		if err != nil {
			b.errorf("--%s: %w", flag, err)
		}
		people = append(people, p)
		b.set.reviewTeams = b.set.reviewTeams || (r.teams && len(p.teams) > 0)
	}
	b.set.detailed = append(b.set.detailed, func(s *inbox.Subject) bool {
		matched := slices.ContainsFunc(people, func(p person) bool { return r.match(p, s) })
		return matched != r.negate
	})
}

func (b *builder) parsePerson(v string, allowTeams bool) (person, error) {
	switch {
	case strings.EqualFold(v, Me):
		if b.env.Me.Login == "" {
			return person{}, errors.New("@me needs the authenticated user")
		}
		return person{user: b.env.Me.Login, teams: b.env.Me.Teams}, nil
	case strings.Contains(v, "/"):
		org, team, _ := strings.Cut(v, "/")
		if !allowTeams {
			return person{}, fmt.Errorf("%q is a team, which isn't allowed here", v)
		}
		if org == "" || team == "" || strings.Contains(team, "/") {
			return person{}, fmt.Errorf("invalid team %q: want org/team", v)
		}
		return person{teams: []string{v}}, nil
	case v == "" || strings.ContainsAny(v, " \t"):
		return person{}, fmt.Errorf("invalid user %q", v)
	default:
		return person{user: v}, nil
	}
}

func isAuthor(p person, s *inbox.Subject) bool {
	return p.user != "" && strings.EqualFold(p.user, s.Author)
}

func isAssignee(p person, s *inbox.Subject) bool {
	return containsFold(s.Assignees, p.user)
}

func isReviewer(p person, s *inbox.Subject) bool {
	return containsFold(s.ReviewUsers, p.user) ||
		slices.ContainsFunc(p.teams, func(t string) bool { return containsFold(s.ReviewTeams, t) })
}

func involves(p person, s *inbox.Subject) bool {
	return isAssignee(p, s) || isReviewer(p, s)
}

// containsFold reports whether list contains v, ignoring case. An empty v is
// never contained.
func containsFold(list []string, v string) bool {
	return v != "" && slices.ContainsFunc(list, func(s string) bool { return strings.EqualFold(s, v) })
}
