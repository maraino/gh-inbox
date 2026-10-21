package github

import (
	"context"
	"net/url"
	"strconv"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/maraino/gh-inbox/internal/inbox"
)

const (
	// notificationsPath lists every notification in the inbox, read or
	// unread. 50 is the largest page size the endpoint allows.
	notificationsPath = "notifications?all=true&per_page=50"
	// listConcurrency is how many pages are fetched at the same time.
	listConcurrency = 4
)

type apiNotification struct {
	ID        string    `json:"id"`
	Unread    bool      `json:"unread"`
	Reason    string    `json:"reason"`
	UpdatedAt time.Time `json:"updated_at"`
	Subject   struct {
		Title string `json:"title"`
		URL   string `json:"url"`
		Type  string `json:"type"`
	} `json:"subject"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
}

// Notifications lists every notification in the inbox, read or unread, most
// recently updated first.
//
// The API also lists threads that were marked as done and haven't had
// activity since; it has no way to tell them apart from read ones.
func (c *Client) Notifications(ctx context.Context) ([]inbox.Notification, error) {
	var first []apiNotification
	links, err := c.get(ctx, notificationsPath, &first)
	if err != nil {
		return nil, err
	}
	pages := [][]apiNotification{first}

	// The first page says how many there are, so the rest can be fetched
	// concurrently. Otherwise, follow the links one page at a time.
	if urls, ok := pageURLs(links); ok {
		rest, err := c.getPages(ctx, urls)
		if err != nil {
			return nil, err
		}
		pages = append(pages, rest...)
	} else {
		for next := links["next"]; next != ""; next = links["next"] {
			var page []apiNotification
			if links, err = c.get(ctx, next, &page); err != nil {
				return nil, err
			}
			pages = append(pages, page)
		}
	}

	// Notifications that move while the pages are fetched can appear twice.
	var all []inbox.Notification
	seen := make(map[string]bool)
	for _, page := range pages {
		for _, n := range page {
			if seen[n.ID] {
				continue
			}
			seen[n.ID] = true
			ref, _ := parseSubjectRef(n.Subject.URL)
			all = append(all, inbox.Notification{
				ID:         n.ID,
				Repo:       n.Repository.FullName,
				Type:       inbox.SubjectType(n.Subject.Type),
				Title:      n.Subject.Title,
				Reason:     n.Reason,
				Unread:     n.Unread,
				UpdatedAt:  n.UpdatedAt,
				SubjectURL: n.Subject.URL,
				Number:     ref.num,
				URL:        subjectWebURL(n.Subject.URL),
			})
		}
	}
	return all, nil
}

// pageURLs returns the URLs of every page after the first, from the links
// of the first page. It returns false when the links don't say how many
// pages there are.
func pageURLs(links map[string]string) ([]string, bool) {
	next, last := links["next"], links["last"]
	if next == "" {
		return nil, true
	}
	nextURL, err := url.Parse(next)
	if err != nil {
		return nil, false
	}
	lastURL, err := url.Parse(last)
	if err != nil {
		return nil, false
	}
	lastPage, err := strconv.Atoi(lastURL.Query().Get("page"))
	if err != nil || nextURL.Query().Get("page") != "2" {
		return nil, false
	}

	urls := make([]string, 0, lastPage-1)
	for page := 2; page <= lastPage; page++ {
		q := nextURL.Query()
		q.Set("page", strconv.Itoa(page))
		u := *nextURL
		u.RawQuery = q.Encode()
		urls = append(urls, u.String())
	}
	return urls, true
}

// getPages fetches the pages concurrently and returns them in order.
func (c *Client) getPages(ctx context.Context, urls []string) ([][]apiNotification, error) {
	pages := make([][]apiNotification, len(urls))
	g, ctx := errgroup.WithContext(ctx)
	g.SetLimit(listConcurrency)
	for i, u := range urls {
		g.Go(func() error {
			_, err := c.get(ctx, u, &pages[i])
			return err
		})
	}
	return pages, g.Wait()
}
