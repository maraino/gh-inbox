package github

import (
	"context"
	"time"

	"github.com/maraino/gh-inbox/internal/inbox"
)

// notificationsPath lists every notification in the inbox, read or unread.
// 50 is the largest page size the endpoint allows.
const notificationsPath = "notifications?all=true&per_page=50"

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

// Notifications lists every notification in the inbox, read or unread.
//
// The API also lists threads that were marked as done and haven't had
// activity since; it has no way to tell them apart from read ones.
func (c *Client) Notifications(ctx context.Context) ([]inbox.Notification, error) {
	var all []inbox.Notification
	for path := notificationsPath; path != ""; {
		var page []apiNotification
		next, err := c.get(ctx, path, &page)
		if err != nil {
			return nil, err
		}
		for _, n := range page {
			all = append(all, inbox.Notification{
				ID:         n.ID,
				Repo:       n.Repository.FullName,
				Type:       inbox.SubjectType(n.Subject.Type),
				Title:      n.Subject.Title,
				Reason:     n.Reason,
				Unread:     n.Unread,
				UpdatedAt:  n.UpdatedAt,
				SubjectURL: n.Subject.URL,
			})
		}
		path = next
	}
	return all, nil
}
