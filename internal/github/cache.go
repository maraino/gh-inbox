package github

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/maraino/gh-inbox/internal/cache"
	"github.com/maraino/gh-inbox/internal/inbox"
)

// The cache keeps API responses between runs, so that running several
// commands in a row doesn't list the inbox and look up every pull request
// again. It's only an optimization: a cache that can't be read or written
// is ignored.
const (
	// cacheVersion is the version of the cache files' format.
	cacheVersion = 1
	// listMaxAge is how long the list of notifications is reused while the
	// API says there are no new ones. The API doesn't report threads read
	// or marked as done elsewhere, such as on github.com, as new, so this
	// bounds how long they can look unread.
	listMaxAge = 10 * time.Minute
	// detailsMaxAge is how long the details of a pull request or issue are
	// reused while its notification has no new activity. Some changes,
	// such as labels, don't count as activity.
	detailsMaxAge = time.Hour

	listCacheFile    = "notifications.json"
	detailsCacheFile = "details.json"
)

// listCache is the last list of notifications, and when the API said it
// last changed.
type listCache struct {
	Version       int                  `json:"version"`
	FetchedAt     time.Time            `json:"fetched_at"`
	LastModified  string               `json:"last_modified"`
	Notifications []inbox.Notification `json:"notifications"`
}

// detailsCache maps the API URLs of pull requests and issues to their
// details.
type detailsCache struct {
	Version  int                      `json:"version"`
	Subjects map[string]cachedSubject `json:"subjects"`
}

type cachedSubject struct {
	FetchedAt time.Time `json:"fetched_at"`
	// UpdatedAt is the latest activity of the notification when the
	// details were looked up.
	UpdatedAt time.Time `json:"updated_at"`
	// ReviewTeams reports whether the teams whose review is requested were
	// looked up.
	ReviewTeams bool `json:"review_teams"`
	// Subject is nil for a pull request or issue that couldn't be read.
	Subject *inbox.Subject `json:"subject"`
}

// readList returns the cached list of notifications, or nil when there's
// none that can be reused.
func (c *Client) readList() *listCache {
	var lc listCache
	if !c.readCache(listCacheFile, &lc) || lc.Version != cacheVersion || lc.LastModified == "" ||
		c.now().Sub(lc.FetchedAt) > listMaxAge {
		return nil
	}
	return &lc
}

func (c *Client) writeList(lastModified string, notifications []inbox.Notification) {
	if lastModified == "" {
		return
	}
	c.writeCache(listCacheFile, listCache{
		Version:       cacheVersion,
		FetchedAt:     c.now(),
		LastModified:  lastModified,
		Notifications: notifications,
	})
}

// forgetList removes the cached list of notifications, after a change to a
// thread that the API wouldn't report as new.
func (c *Client) forgetList() {
	if c.cacheDir != "" {
		_ = os.Remove(filepath.Join(c.cacheDir, listCacheFile)) // Nothing to do if it fails.
	}
}

// readDetails returns the cached details that can still be used.
func (c *Client) readDetails() map[string]cachedSubject {
	var dc detailsCache
	if !c.readCache(detailsCacheFile, &dc) || dc.Version != cacheVersion || dc.Subjects == nil {
		return map[string]cachedSubject{}
	}
	for url, s := range dc.Subjects {
		if c.now().Sub(s.FetchedAt) > detailsMaxAge {
			delete(dc.Subjects, url)
		}
	}
	return dc.Subjects
}

func (c *Client) writeDetails(subjects map[string]cachedSubject) {
	c.writeCache(detailsCacheFile, detailsCache{Version: cacheVersion, Subjects: subjects})
}

// readCache decodes a cache file into v. It reports whether it could.
func (c *Client) readCache(name string, v any) bool {
	if c.cacheDir == "" || c.refresh {
		return false
	}
	data, err := os.ReadFile(filepath.Join(c.cacheDir, name))
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			c.logf("* Ignoring the cache: %v", err)
		}
		return false
	}
	if err := json.Unmarshal(data, v); err != nil {
		c.logf("* Ignoring the cache %s: %v", name, err)
		return false
	}
	return true
}

// writeCache replaces a cache file with v atomically.
func (c *Client) writeCache(name string, v any) {
	if c.cacheDir == "" {
		return
	}
	data, err := json.Marshal(v)
	if err == nil {
		err = cache.WriteFileAtomic(filepath.Join(c.cacheDir, name), data)
	}
	if err != nil {
		c.logf("* Not caching %s: %v", name, err)
	}
}
