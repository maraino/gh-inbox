package render

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/maraino/gh-inbox/internal/inbox"
)

var now = time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)

var sample = []inbox.Notification{
	{
		ID: "1", Repo: "acme/api", Type: inbox.TypePullRequest, Title: "Fix it", Reason: "review_requested",
		Unread: true, UpdatedAt: now.Add(-3 * time.Hour),
		SubjectURL: "https://api.github.com/repos/acme/api/pulls/7", Number: 7,
		URL:     "https://github.com/acme/api/pull/7",
		Subject: &inbox.Subject{Number: 7, State: inbox.StateMerged, URL: "https://github.com/acme/api/pull/7", Author: "alice"},
	},
	{
		ID: "2", Repo: "acme/api", Type: inbox.TypeCheckSuite, Title: "CI failed", Reason: "ci_activity",
		UpdatedAt: now.Add(-2 * 24 * time.Hour),
	},
	{
		ID: "3", Repo: "acme/web", Type: inbox.TypePullRequest, Title: "Add it", Reason: "review_requested",
		UpdatedAt: now.Add(-5 * time.Minute), Number: 9,
		Subject: &inbox.Subject{Number: 9, State: inbox.StateOpen},
	},
}

func TestTable(t *testing.T) {
	var buf bytes.Buffer
	if err := Table(&buf, sample, false, 0, now); err != nil {
		t.Fatal(err)
	}
	want := "acme/api\t#7\tpr\tmerged\treview_requested\t2026-10-20T09:00:00Z\tFix it\n" +
		"acme/api\t-\tci\t-\tci_activity\t2026-10-18T12:00:00Z\tCI failed\n" +
		"acme/web\t#9\tpr\topen\treview_requested\t2026-10-20T11:55:00Z\tAdd it\n"
	if got := buf.String(); got != want {
		t.Errorf("Table() =\n%s\nwant\n%s", got, want)
	}
}

func TestTableTTY(t *testing.T) {
	var buf bytes.Buffer
	if err := Table(&buf, sample, true, 120, now); err != nil {
		t.Fatal(err)
	}
	got := buf.String()
	for _, want := range []string{"REPO", "NUMBER", "#7", "TITLE", "3h ago", "2d ago", "5m ago"} {
		if !strings.Contains(got, want) {
			t.Errorf("Table() on a terminal doesn't contain %q:\n%s", want, got)
		}
	}
}

func TestSummary(t *testing.T) {
	var buf bytes.Buffer
	if err := Summary(&buf, sample); err != nil {
		t.Fatal(err)
	}
	want := `3 notifications

By repository
  2  acme/api
  1  acme/web

By type
  2  pr
  1  ci

By reason
  2  review_requested
  1  ci_activity

By state
  1  (none)
  1  merged
  1  open
`
	if got := buf.String(); got != want {
		t.Errorf("Summary() =\n%s\nwant\n%s", got, want)
	}
}

func TestSummaryWithoutDetails(t *testing.T) {
	ns := []inbox.Notification{{Repo: "acme/api", Type: inbox.TypePullRequest, Reason: "subscribed"}}
	var buf bytes.Buffer
	if err := Summary(&buf, ns); err != nil {
		t.Fatal(err)
	}
	if got := buf.String(); strings.Contains(got, "By state") {
		t.Errorf("Summary() shows states it doesn't know:\n%s", got)
	}
}

func TestSummaryLimitsRepos(t *testing.T) {
	var ns []inbox.Notification
	for i := range maxRepos + 2 {
		ns = append(ns, inbox.Notification{Repo: "acme/repo" + string(rune('a'+i))})
	}
	var buf bytes.Buffer
	if err := Summary(&buf, ns); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "and 2 more") {
		t.Errorf("Summary() doesn't say how many repositories it left out:\n%s", buf.String())
	}
}

func TestJSON(t *testing.T) {
	var buf bytes.Buffer
	if err := JSON(&buf, sample[:2]); err != nil {
		t.Fatal(err)
	}
	var got []map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("JSON() wrote %d notifications, want 2", len(got))
	}
	if got[0]["url"] != "https://github.com/acme/api/pull/7" || got[0]["number"] != 7.0 {
		t.Errorf("first notification's url, number = %v, %v", got[0]["url"], got[0]["number"])
	}
	if _, ok := got[0]["subject_url"]; ok {
		t.Error("a notification has the API URL")
	}
	if _, ok := got[1]["url"]; ok {
		t.Error("a CI notification has a URL")
	}
	subject, ok := got[0]["subject"].(map[string]any)
	if !ok || subject["state"] != "merged" || subject["author"] != "alice" {
		t.Errorf("first notification's subject = %v", got[0]["subject"])
	}
	if assignees, ok := subject["assignees"].([]any); !ok || len(assignees) != 0 {
		t.Errorf("assignees = %v, want []", subject["assignees"])
	}
	if _, ok := got[1]["subject"]; ok {
		t.Error("a notification without details has a subject")
	}
}
