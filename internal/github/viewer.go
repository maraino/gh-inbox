package github

import (
	"context"
	"fmt"

	"github.com/maraino/gh-inbox/internal/inbox"
)

// Viewer returns the authenticated user and the teams they belong to.
// Listing teams needs the read:org scope.
func (c *Client) Viewer(ctx context.Context) (inbox.Identity, error) {
	var user struct {
		Login string `json:"login"`
	}
	if _, err := c.get(ctx, "user", &user); err != nil {
		return inbox.Identity{}, fmt.Errorf("looking up the authenticated user: %w", err)
	}

	id := inbox.Identity{Login: user.Login}
	for path := "user/teams?per_page=100"; path != ""; {
		var page []struct {
			Slug         string `json:"slug"`
			Organization struct {
				Login string `json:"login"`
			} `json:"organization"`
		}
		links, err := c.get(ctx, path, &page)
		if err != nil {
			return inbox.Identity{}, fmt.Errorf("listing your teams: %w", err)
		}
		for _, t := range page {
			id.Teams = append(id.Teams, t.Organization.Login+"/"+t.Slug)
		}
		path = links["next"]
	}
	return id, nil
}
