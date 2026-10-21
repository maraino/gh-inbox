// Package inbox defines the notification model shared by the other packages.
package inbox

import (
	"fmt"
	"slices"
	"strings"
	"time"
)

// SubjectType is the kind of thing a notification is about, as the GitHub API
// names it.
type SubjectType string

// The subject types GitHub sends notifications for.
const (
	TypePullRequest SubjectType = "PullRequest"
	TypeIssue       SubjectType = "Issue"
	TypeRelease     SubjectType = "Release"
	TypeCheckSuite  SubjectType = "CheckSuite"
	TypeDiscussion  SubjectType = "Discussion"
	TypeAdvisory    SubjectType = "RepositoryAdvisory"
	TypeDependabot  SubjectType = "RepositoryDependabotAlertsThread"
	TypeCommit      SubjectType = "Commit"
)

// typeNames maps the short names used on the command line to subject types.
var typeNames = map[string]SubjectType{
	"pr":         TypePullRequest,
	"issue":      TypeIssue,
	"release":    TypeRelease,
	"ci":         TypeCheckSuite,
	"discussion": TypeDiscussion,
	"advisory":   TypeAdvisory,
	"dependabot": TypeDependabot,
	"commit":     TypeCommit,
}

// TypeNames returns the short names ParseType accepts, sorted.
func TypeNames() []string {
	names := make([]string, 0, len(typeNames))
	for name := range typeNames {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// ParseType returns the subject type with the given short name, such as "pr".
func ParseType(name string) (SubjectType, error) {
	if t, ok := typeNames[strings.ToLower(name)]; ok {
		return t, nil
	}
	return "", fmt.Errorf("unknown type %q: valid types are %s", name, strings.Join(TypeNames(), ", "))
}

// ShortName returns the command-line name of t, or the API name for types
// without one.
func (t SubjectType) ShortName() string {
	for name, typ := range typeNames {
		if typ == t {
			return name
		}
	}
	return string(t)
}

// HasDetails reports whether subjects of type t have details that can be
// looked up: a state, an author, assignees and requested reviewers.
func (t SubjectType) HasDetails() bool {
	return t == TypePullRequest || t == TypeIssue
}

// State is the state of a pull request or issue.
type State string

// The states of pull requests and issues. Only pull requests can be merged.
const (
	StateOpen   State = "open"
	StateClosed State = "closed"
	StateMerged State = "merged"
)

// ParseState returns the state with the given name.
func ParseState(name string) (State, error) {
	switch s := State(strings.ToLower(name)); s {
	case StateOpen, StateClosed, StateMerged:
		return s, nil
	default:
		return "", fmt.Errorf("unknown state %q: valid states are open, closed, merged", name)
	}
}

// Notification is one thread in the notifications inbox.
type Notification struct {
	// ID is the thread ID, used to mark it as done or read.
	ID string
	// Repo is the repository the notification is from, as "owner/name".
	Repo   string
	Type   SubjectType
	Title  string
	Reason string
	Unread bool
	// UpdatedAt is the time of the latest activity on the thread.
	UpdatedAt time.Time
	// SubjectURL is the API URL of the subject. It's empty for some types,
	// such as CI runs.
	SubjectURL string
	// Subject holds the details of a pull request or issue. It's nil until
	// they are looked up, and when they couldn't be.
	Subject *Subject
}

// Subject holds the details of a pull request or issue.
type Subject struct {
	Number int
	State  State
	// URL is the web URL of the pull request or issue.
	URL       string
	Author    string
	Assignees []string
	// ReviewUsers are the users whose review is currently requested.
	ReviewUsers []string
	// ReviewTeams are the teams whose review is currently requested, as
	// "org/team".
	ReviewTeams []string
}

// Identity is the authenticated user and the teams they belong to.
type Identity struct {
	Login string
	// Teams are the user's teams, as "org/team".
	Teams []string
}
