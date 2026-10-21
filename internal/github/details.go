package github

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/cli/go-gh/v2/pkg/api"
	"golang.org/x/sync/errgroup"

	"github.com/maraino/gh-inbox/internal/inbox"
)

const (
	// detailsBatchSize is how many subjects one GraphQL query looks up. Larger
	// queries risk timing out.
	detailsBatchSize = 25
	// detailsConcurrency is how many queries run at the same time.
	detailsConcurrency = 4
)

// prFragment and issueFragment select the fields of inbox.Subject. A query
// must only include the fragments it uses. %s in prFragment is where
// teamFields go.
const (
	prFragment = `
fragment pr on PullRequest {
  number state url
  author { login }
  assignees(first: 100) { nodes { login } }
  reviewRequests(first: 100) {
    nodes {
      requestedReviewer {
        __typename
        ... on User { login }
        ... on Bot { login }
        ... on Mannequin { login }%s
      }
    }
  }
}`
	issueFragment = `
fragment issue on Issue {
  number state url
  author { login }
  assignees(first: 100) { nodes { login } }
}`
	// teamFields select the teams whose review is requested. Reading them
	// needs the read:org scope.
	teamFields = `
        ... on Team { slug organization { login } }`
)

// DetailsOptions choose what SubjectDetails looks up.
type DetailsOptions struct {
	// ReviewTeams looks up the teams whose review is requested, which needs
	// the read:org scope. Without it, inbox.Subject.ReviewTeams is empty.
	ReviewTeams bool
}

// errMissingReadOrg explains how to grant the scope team lookups need.
var errMissingReadOrg = errors.New(`reading teams needs the read:org scope: run "gh auth refresh -s read:org"`)

// subjectRef identifies a pull request or issue.
type subjectRef struct {
	url   string // the API URL, which keys the results
	owner string
	repo  string
	num   int
	isPR  bool
}

// parseSubjectRef parses the API URL of a pull request or issue, such as
// https://api.github.com/repos/OWNER/REPO/pulls/123. Enterprise hosts put
// the same path under /api/v3.
func parseSubjectRef(apiURL string) (subjectRef, bool) {
	u, err := url.Parse(apiURL)
	if err != nil {
		return subjectRef{}, false
	}
	_, rest, ok := strings.Cut(u.Path, "/repos/")
	if !ok {
		return subjectRef{}, false
	}
	parts := strings.Split(rest, "/")
	if len(parts) != 4 || (parts[2] != "pulls" && parts[2] != "issues") {
		return subjectRef{}, false
	}
	num, err := strconv.Atoi(parts[3])
	if err != nil || num <= 0 {
		return subjectRef{}, false
	}
	return subjectRef{url: apiURL, owner: parts[0], repo: parts[1], num: num, isPR: parts[2] == "pulls"}, true
}

// subjectWebURL returns the web URL of the pull request or issue with the
// given API URL, or "" when it isn't one. The API of github.com and GHE.com
// is on an "api." subdomain; Enterprise Server's is under /api/v3 on the
// same host.
func subjectWebURL(apiURL string) string {
	ref, ok := parseSubjectRef(apiURL)
	if !ok {
		return ""
	}
	u, err := url.Parse(apiURL)
	if err != nil {
		return ""
	}
	kind := "issues"
	if ref.isPR {
		kind = "pull"
	}
	web := url.URL{
		Scheme: u.Scheme,
		Host:   strings.TrimPrefix(u.Host, "api."),
		Path:   fmt.Sprintf("/%s/%s/%s/%d", ref.owner, ref.repo, kind, ref.num),
	}
	return web.String()
}

// SubjectDetails looks up the pull requests and issues the notifications are
// about. It returns their details keyed by subject URL. Subjects that no
// longer exist or that can't be read are missing from the result; any other
// failure is an error.
func (c *Client) SubjectDetails(ctx context.Context, notifications []inbox.Notification, opts DetailsOptions) (map[string]*inbox.Subject, error) {
	var refs []subjectRef
	seen := make(map[string]bool)
	for _, n := range notifications {
		if !n.Type.HasDetails() || seen[n.SubjectURL] {
			continue
		}
		if ref, ok := parseSubjectRef(n.SubjectURL); ok {
			refs = append(refs, ref)
			seen[ref.url] = true
		}
	}

	var mu sync.Mutex
	details := make(map[string]*inbox.Subject, len(refs))
	g, ctx := errgroup.WithContext(ctx)
	g.SetLimit(detailsConcurrency)
	for batch := range slices.Chunk(refs, detailsBatchSize) {
		g.Go(func() error {
			found, err := c.lookUpBatch(ctx, batch, opts)
			if err != nil {
				return err
			}
			mu.Lock()
			defer mu.Unlock()
			for k, v := range found {
				details[k] = v
			}
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, fmt.Errorf("looking up pull requests and issues: %w", err)
	}
	return details, nil
}

// apiSubject is the GraphQL shape of a pull request or issue.
type apiSubject struct {
	Number int    `json:"number"`
	State  string `json:"state"`
	URL    string `json:"url"`
	Author *struct {
		Login string `json:"login"`
	} `json:"author"`
	Assignees struct {
		Nodes []struct {
			Login string `json:"login"`
		} `json:"nodes"`
	} `json:"assignees"`
	ReviewRequests struct {
		Nodes []struct {
			RequestedReviewer *struct {
				Typename     string `json:"__typename"`
				Login        string `json:"login"`
				Slug         string `json:"slug"`
				Organization struct {
					Login string `json:"login"`
				} `json:"organization"`
			} `json:"requestedReviewer"`
		} `json:"nodes"`
	} `json:"reviewRequests"`
}

// lookUpBatch looks up the refs with a single query, with one aliased
// repository field per ref.
func (c *Client) lookUpBatch(ctx context.Context, refs []subjectRef, opts DetailsOptions) (map[string]*inbox.Subject, error) {
	var params, fields strings.Builder
	var hasPRs, hasIssues bool
	vars := make(map[string]any, 3*len(refs))
	for i, ref := range refs {
		field, fragment := "issue", "issue"
		if ref.isPR {
			field, fragment = "pullRequest", "pr"
		}
		hasPRs = hasPRs || ref.isPR
		hasIssues = hasIssues || !ref.isPR
		fmt.Fprintf(&params, "$o%d: String!, $r%d: String!, $n%d: Int!, ", i, i, i)
		fmt.Fprintf(&fields, "s%d: repository(owner: $o%d, name: $r%d) { %s(number: $n%d) { ...%s } }\n",
			i, i, i, field, i, fragment)
		vars[fmt.Sprintf("o%d", i)] = ref.owner
		vars[fmt.Sprintf("r%d", i)] = ref.repo
		vars[fmt.Sprintf("n%d", i)] = ref.num
	}
	query := fmt.Sprintf("query(%s) {\n%s}\n", strings.TrimSuffix(params.String(), ", "), fields.String())
	if hasPRs {
		teams := ""
		if opts.ReviewTeams {
			teams = teamFields
		}
		query += fmt.Sprintf(prFragment, teams)
	}
	if hasIssues {
		query += issueFragment
	}

	var resp map[string]*struct {
		PullRequest *apiSubject `json:"pullRequest"`
		Issue       *apiSubject `json:"issue"`
	}
	err := c.retry(ctx, func() error {
		resp = nil
		return c.gql.DoWithContext(ctx, query, vars, &resp)
	})
	// Subjects that can't be read come back as null with an error each, next
	// to the data for the others.
	switch {
	case err == nil || onlyUnreadable(err):
	case hasErrorType(err, "INSUFFICIENT_SCOPES"):
		return nil, errMissingReadOrg
	default:
		return nil, err
	}

	found := make(map[string]*inbox.Subject, len(refs))
	for i, ref := range refs {
		repo := resp[fmt.Sprintf("s%d", i)]
		if repo == nil {
			continue
		}
		subject := repo.Issue
		if ref.isPR {
			subject = repo.PullRequest
		}
		if subject != nil {
			found[ref.url] = subject.toSubject()
		}
	}
	return found, nil
}

// hasErrorType reports whether err is a GraphQL error with an error of the
// given type.
func hasErrorType(err error, typ string) bool {
	var gqlErr *api.GraphQLError
	return errors.As(err, &gqlErr) &&
		slices.ContainsFunc(gqlErr.Errors, func(item api.GraphQLErrorItem) bool { return item.Type == typ })
}

// onlyUnreadable reports whether err is a GraphQL error made only of
// subjects that don't exist or aren't accessible.
func onlyUnreadable(err error) bool {
	var gqlErr *api.GraphQLError
	if !errors.As(err, &gqlErr) {
		return false
	}
	for _, item := range gqlErr.Errors {
		if item.Type != "NOT_FOUND" && item.Type != "FORBIDDEN" {
			return false
		}
	}
	return true
}

func (s *apiSubject) toSubject() *inbox.Subject {
	subject := &inbox.Subject{
		Number: s.Number,
		State:  inbox.State(strings.ToLower(s.State)),
		URL:    s.URL,
	}
	if s.Author != nil {
		subject.Author = s.Author.Login
	}
	for _, a := range s.Assignees.Nodes {
		subject.Assignees = append(subject.Assignees, a.Login)
	}
	for _, r := range s.ReviewRequests.Nodes {
		switch reviewer := r.RequestedReviewer; {
		case reviewer == nil:
			// The reviewer is no longer visible, such as a deleted team.
		case reviewer.Typename == "Team":
			subject.ReviewTeams = append(subject.ReviewTeams, reviewer.Organization.Login+"/"+reviewer.Slug)
		default:
			subject.ReviewUsers = append(subject.ReviewUsers, reviewer.Login)
		}
	}
	return subject
}
