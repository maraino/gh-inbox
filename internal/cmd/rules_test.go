package cmd

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/maraino/gh-inbox/internal/action"
)

func TestParseRules(t *testing.T) {
	rules, err := parseRules([]byte(`
rules:
  - name: merged
    filters:
      state: [merged, closed]
  - name: stale-ci
    action: read
    filters:
      type: ci
      older-than: 7d
      unread: true
  - name: noisy
    action: unsubscribe
    filters:
      repo: "acme/infra,acme/docs-*"
      reason: subscribed
`))
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 3 {
		t.Fatalf("got %d rules, want 3", len(rules))
	}

	merged, ci, noisy := rules[0], rules[1], rules[2]
	if merged.name != "merged" || merged.kind != action.Done || !slices.Equal(merged.filters.States, []string{"merged", "closed"}) {
		t.Errorf("merged = %+v", merged)
	}
	if ci.kind != action.Read || !slices.Equal(ci.filters.Types, []string{"ci"}) ||
		ci.filters.OlderThan != 7*24*time.Hour || !ci.filters.Unread {
		t.Errorf("stale-ci = %+v", ci)
	}
	if noisy.kind != action.Unsubscribe || !slices.Equal(noisy.filters.Repos, []string{"acme/infra", "acme/docs-*"}) ||
		!slices.Equal(noisy.filters.Reasons, []string{"subscribed"}) {
		t.Errorf("noisy = %+v", noisy)
	}
}

func TestParseRulesErrors(t *testing.T) {
	tests := []struct {
		name string
		yaml string
		want string
	}{
		{"empty", ``, "no rules"},
		{"no rules", `rules: []`, "no rules"},
		{"unknown field", "rules:\n  - name: a\n    filter:\n      type: ci\n", "field filter not found"},
		{"no name", "rules:\n  - filters:\n      type: ci\n", "rule 1 has no name"},
		{"same name", "rules:\n  - name: a\n    filters: {type: ci}\n  - name: a\n    filters: {type: pr}\n", `rule "a" appears twice`},
		{"unknown action", "rules:\n  - name: a\n    action: delete\n    filters: {type: ci}\n", `rule "a"`},
		{"no filters", "rules:\n  - name: a\n", "no filters"},
		{"unknown filter", "rules:\n  - name: a\n    filters: {color: red}\n", `unknown filter "color"`},
		{"no value", "rules:\n  - name: a\n    filters: {type: }\n", "filter type: no value"},
		{"map value", "rules:\n  - name: a\n    filters: {type: {a: b}}\n", "filter type: want a value"},
		{"invalid age", "rules:\n  - name: a\n    filters: {older-than: soon}\n", "filter older-than"},
		{"invalid bool", "rules:\n  - name: a\n    filters: {unread: maybe}\n", "filter unread"},
		{"invalid state", "rules:\n  - name: a\n    filters: {state: draft}\n", `rule "a": --state`},
		{"team as author", "rules:\n  - name: a\n    filters: {author: acme/backend}\n", "is a team"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseRules([]byte(tt.yaml))
			if err == nil {
				t.Fatal("got no error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %q, want it to contain %q", err, tt.want)
			}
		})
	}
}

// testRules are rules for the fake inbox: merged takes notification 1,
// stale-ci notification 3, and prs the pull requests merged didn't take, 2
// and 4.
const testRules = `
rules:
  - name: merged
    filters:
      state: merged
  - name: stale-ci
    action: read
    filters:
      type: ci
      older-than: 7d
  - name: prs
    action: unsubscribe
    filters:
      type: pr
  - name: releases
    filters:
      type: release
`

func writeRules(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "rules.yml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCleanRulesDryRun(t *testing.T) {
	c := newFakeInbox()
	got := run(t, c, t.TempDir(), "clean", "--rules", "--rules-file", writeRules(t, testRules))
	if got.err != nil {
		t.Fatal(got.err)
	}
	if len(c.actions) != 0 {
		t.Errorf("a dry run applied actions: %v", c.actions)
	}
	for _, want := range []string{
		"Rule \"merged\": Would mark 1 notification as done.\n",
		"Rule \"stale-ci\": Would mark 1 notification as read.\n",
		"Rule \"prs\": Would unsubscribe from 2 notifications.\n",
		"Rule \"releases\": No notifications match.\n",
		"Run again with --apply to do it.\n",
	} {
		if !strings.Contains(got.stdout, want) {
			t.Errorf("stdout doesn't contain %q:\n%s", want, got.stdout)
		}
	}
	// Notification 4 can't be read, which merged needs, but prs takes it.
	if got.stderr != "" {
		t.Errorf("stderr = %q, want nothing", got.stderr)
	}
	// Without prs, it's reported.
	got = run(t, c, t.TempDir(), "clean", "--rule", "merged", "--rules-file", writeRules(t, testRules))
	if want := "Skipped 1 notification whose pull request or issue couldn't be read:\n  acme/gone: Lost PR\n"; !strings.Contains(got.stderr, want) {
		t.Errorf("stderr = %q, want it to contain %q", got.stderr, want)
	}
}

func TestCleanRulesApply(t *testing.T) {
	tests := []struct {
		name        string
		args        []string
		wantActions []string
		wantStdout  string
	}{
		{
			name:        "every rule",
			args:        []string{"--rules"},
			wantActions: []string{"done 1", "read 3", "unsubscribe 2", "unsubscribe 4"},
			wantStdout: "Rule \"merged\": Marked 1 notification as done.\n" +
				"Rule \"stale-ci\": Marked 1 notification as read.\n" +
				"Rule \"prs\": Unsubscribed from 2 notifications.\n" +
				"Rule \"releases\": No notifications match.\n",
		},
		{
			name:        "some rules, in the order of the file",
			args:        []string{"--rule", "prs,merged"},
			wantActions: []string{"done 1", "unsubscribe 2", "unsubscribe 4"},
			wantStdout: "Rule \"merged\": Marked 1 notification as done.\n" +
				"Rule \"prs\": Unsubscribed from 2 notifications.\n",
		},
		{
			name:        "a later rule takes what an earlier one isn't given",
			args:        []string{"--rule", "prs"},
			wantActions: []string{"unsubscribe 1", "unsubscribe 2", "unsubscribe 4"},
			wantStdout:  "Rule \"prs\": Unsubscribed from 3 notifications.\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newFakeInbox()
			args := append([]string{"clean", "--apply", "--rules-file", writeRules(t, testRules)}, tt.args...)
			got := run(t, c, t.TempDir(), args...)
			if got.err != nil {
				t.Fatal(got.err)
			}
			slices.Sort(c.actions)
			if !slices.Equal(c.actions, tt.wantActions) {
				t.Errorf("actions = %v, want %v", c.actions, tt.wantActions)
			}
			if got.stdout != tt.wantStdout {
				t.Errorf("stdout =\n%s\nwant\n%s", got.stdout, tt.wantStdout)
			}
		})
	}
}

func TestCleanRulesRemembersDone(t *testing.T) {
	cacheDir := t.TempDir()
	rules := writeRules(t, testRules)
	c := newFakeInbox()
	if got := run(t, c, cacheDir, "clean", "--apply", "--rule", "merged", "--rules-file", rules); got.err != nil {
		t.Fatal(got.err)
	}
	c.actions = nil
	got := run(t, c, cacheDir, "clean", "--apply", "--rule", "prs", "--rules-file", rules)
	if got.err != nil {
		t.Fatal(got.err)
	}
	slices.Sort(c.actions)
	if want := []string{"unsubscribe 2", "unsubscribe 4"}; !slices.Equal(c.actions, want) {
		t.Errorf("actions = %v, want %v", c.actions, want)
	}
}

func TestCleanRulesDefaultPath(t *testing.T) {
	path := writeRules(t, testRules)
	c := newFakeInbox()
	got := runEnv(t, c, t.TempDir(), func(e *env) {
		e.rulesPath = func() (string, error) { return path, nil }
	}, "clean", "--rule", "stale-ci")
	if got.err != nil {
		t.Fatal(got.err)
	}
	if want := "Rule \"stale-ci\": Would mark 1 notification as read.\n"; !strings.HasPrefix(got.stdout, want) {
		t.Errorf("stdout doesn't start with %q:\n%s", want, got.stdout)
	}
}

func TestCleanRulesErrors(t *testing.T) {
	rules := writeRules(t, testRules)
	missing := filepath.Join(t.TempDir(), "rules.yml")
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"rule and rules", []string{"--rule", "merged", "--rules"}, "can't be used together"},
		{"with filters", []string{"--rules", "--state", "open"}, "can't be combined"},
		{"with action", []string{"--rules", "--action", "read"}, "can't be combined"},
		{"with all", []string{"--rules", "--all"}, "can't be combined"},
		{"unknown rule", []string{"--rule", "merged,nope,nada,nope"}, `no rule named "nope", "nada"`},
		{"no file", []string{"--rules", "--rules-file", missing}, "no rules file at " + missing},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newFakeInbox()
			args := append([]string{"clean", "--apply", "--rules-file", rules}, tt.args...)
			got := run(t, c, t.TempDir(), args...)
			if got.err == nil {
				t.Fatal("got no error")
			}
			if !strings.Contains(got.err.Error(), tt.want) {
				t.Errorf("error = %q, want it to contain %q", got.err, tt.want)
			}
			if len(c.actions) != 0 {
				t.Errorf("applied actions: %v", c.actions)
			}
		})
	}
}

func TestCleanRulesMe(t *testing.T) {
	rules := writeRules(t, `
rules:
  - name: reviews
    action: read
    filters:
      review-requested: "@me"
`)
	c := newFakeInbox()
	got := run(t, c, t.TempDir(), "clean", "--apply", "--rules", "--rules-file", rules)
	if got.err != nil {
		t.Fatal(got.err)
	}
	if want := []string{"read 2"}; !slices.Equal(c.actions, want) {
		t.Errorf("actions = %v, want %v", c.actions, want)
	}
}

func TestExampleRules(t *testing.T) {
	data, err := os.ReadFile("../../examples/rules.yml")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parseRules(data); err != nil {
		t.Error(err)
	}
}

func TestListRules(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		wantStdout string
		wantStderr string
	}{
		{
			name:       "one rule",
			args:       []string{"--rule", "merged"},
			wantStdout: "acme/api\t#1\tpr\tmerged\treview_requested\t2026-10-20T11:00:00Z\tMerged fix\n",
			wantStderr: "\nSkipped 1 notification whose pull request or issue couldn't be read:\n  acme/gone: Lost PR\n",
		},
		{
			name:       "a rule without details",
			args:       []string{"--rule", "stale-ci"},
			wantStdout: "acme/api\t-\tci\t-\tci_activity\t2026-10-10T12:00:00Z\tCI failed\n",
		},
		{
			// prs needs no details, but they're shown for what it matched.
			name: "every rule, in the order of the inbox",
			args: []string{"--rules"},
			wantStdout: "acme/api\t#1\tpr\tmerged\treview_requested\t2026-10-20T11:00:00Z\tMerged fix\n" +
				"acme/web\t#2\tpr\topen\treview_requested\t2026-10-20T10:00:00Z\tOpen feature\n" +
				"acme/api\t-\tci\t-\tci_activity\t2026-10-10T12:00:00Z\tCI failed\n" +
				"acme/gone\t#4\tpr\t-\treview_requested\t2026-10-20T09:00:00Z\tLost PR\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args := append([]string{"list", "--rules-file", writeRules(t, testRules)}, tt.args...)
			got := run(t, newFakeInbox(), t.TempDir(), args...)
			if got.err != nil {
				t.Fatal(got.err)
			}
			if got.stdout != tt.wantStdout {
				t.Errorf("stdout =\n%s\nwant\n%s", got.stdout, tt.wantStdout)
			}
			if got.stderr != tt.wantStderr {
				t.Errorf("stderr = %q, want %q", got.stderr, tt.wantStderr)
			}
		})
	}
}

func TestListRulesErrors(t *testing.T) {
	rules := writeRules(t, testRules)
	for _, tt := range []struct {
		args []string
		want string
	}{
		{[]string{"--rule", "merged", "--state", "open"}, "can't be combined with filters"},
		{[]string{"--rule", "merged", "--rules"}, "can't be used together"},
		{[]string{"--rule", "nope"}, `no rule named "nope"`},
	} {
		got := run(t, newFakeInbox(), t.TempDir(), append([]string{"list", "--rules-file", rules}, tt.args...)...)
		if got.err == nil || !strings.Contains(got.err.Error(), tt.want) {
			t.Errorf("%v: error = %v, want it to contain %q", tt.args, got.err, tt.want)
		}
	}
}
