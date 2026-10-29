package cmd

import (
	"slices"
	"strings"
	"testing"
)

func TestCleanDryRun(t *testing.T) {
	c := newFakeInbox()
	got := run(t, c, t.TempDir(), "clean", "--state", "merged")
	if got.err != nil {
		t.Fatal(got.err)
	}
	if len(c.actions) != 0 {
		t.Errorf("a dry run applied actions: %v", c.actions)
	}
	if want := "Would mark 1 notification as done.\n"; !strings.HasPrefix(got.stdout, want) {
		t.Errorf("stdout doesn't start with %q:\n%s", want, got.stdout)
	}
	if !strings.Contains(got.stdout, "--apply") {
		t.Errorf("stdout doesn't mention --apply:\n%s", got.stdout)
	}
}

func TestCleanApply(t *testing.T) {
	tests := []struct {
		name        string
		args        []string
		wantActions []string
		wantStdout  string
	}{
		{
			name:        "done",
			args:        []string{"--state", "merged,open"},
			wantActions: []string{"done 1", "done 2"},
			wantStdout:  "Marked 2 notifications as done.\n",
		},
		{
			name:        "read",
			args:        []string{"--type", "ci", "--action", "read"},
			wantActions: []string{"read 3"},
			wantStdout:  "Marked 1 notification as read.\n",
		},
		{
			name:        "unsubscribe",
			args:        []string{"--involves", "@me", "--action", "unsubscribe"},
			wantActions: []string{"unsubscribe 1", "unsubscribe 2"},
			wantStdout:  "Unsubscribed from 2 notifications.\n",
		},
		{
			name:        "all",
			args:        []string{"--all"},
			wantActions: []string{"done 1", "done 2", "done 3", "done 4"},
			wantStdout:  "Marked 4 notifications as done.\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newFakeInbox()
			got := run(t, c, t.TempDir(), append([]string{"clean", "--apply"}, tt.args...)...)
			if got.err != nil {
				t.Fatal(got.err)
			}
			slices.Sort(c.actions)
			if !slices.Equal(c.actions, tt.wantActions) {
				t.Errorf("actions = %v, want %v", c.actions, tt.wantActions)
			}
			if got.stdout != tt.wantStdout {
				t.Errorf("stdout = %q, want %q", got.stdout, tt.wantStdout)
			}
		})
	}
}

func TestCleanRemembersDone(t *testing.T) {
	cacheDir := t.TempDir()
	c := newFakeInbox()
	if got := run(t, c, cacheDir, "clean", "--state", "merged", "--apply"); got.err != nil {
		t.Fatal(got.err)
	}

	// The API keeps listing the thread, but the record skips it.
	got := run(t, c, cacheDir, "clean", "--state", "merged", "--apply")
	if got.err != nil {
		t.Fatal(got.err)
	}
	if !strings.HasPrefix(got.stderr, "No notifications match.\n") {
		t.Errorf("second run stderr = %q, want no matches", got.stderr)
	}
	if got := run(t, c, cacheDir, "list", "--state", "merged"); got.stdout != "" {
		t.Errorf("list shows a thread marked as done:\n%s", got.stdout)
	}

	// --no-cache ignores the record.
	if got := run(t, c, cacheDir, "clean", "--state", "merged", "--apply", "--no-cache"); got.err != nil {
		t.Fatal(got.err)
	}
	if want := []string{"done 1", "done 1"}; !slices.Equal(c.actions, want) {
		t.Errorf("actions = %v, want %v", c.actions, want)
	}

	// New activity brings the thread back.
	c.notifications[0].UpdatedAt = now
	if got := run(t, c, cacheDir, "list", "--state", "merged"); !strings.Contains(got.stdout, "Merged fix") {
		t.Errorf("list doesn't show a thread with new activity:\n%s", got.stdout)
	}
}

func TestCleanFailures(t *testing.T) {
	cacheDir := t.TempDir()
	c := newFakeInbox()
	c.fail = map[string]bool{"2": true}

	got := run(t, c, cacheDir, "clean", "--state", "merged,open", "--apply")
	if got.err == nil || got.err.Error() != "1 of 2 failed" {
		t.Errorf("error = %v, want 1 of 2 failed", got.err)
	}
	if got.stdout != "Marked 1 notification as done.\n" {
		t.Errorf("stdout = %q", got.stdout)
	}
	if !strings.Contains(got.stderr, "acme/web: Open feature: boom") {
		t.Errorf("stderr doesn't report the failure:\n%s", got.stderr)
	}

	// Only the notification that succeeded is recorded.
	c.fail = nil
	if got := run(t, c, cacheDir, "clean", "--state", "merged,open", "--apply"); got.err != nil {
		t.Fatal(got.err)
	}
	if want := []string{"done 1", "done 2"}; !slices.Equal(c.actions, want) {
		t.Errorf("actions = %v, want %v", c.actions, want)
	}
}

func TestCleanErrors(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "no filters", args: []string{"clean", "--apply"}, want: "no filters given"},
		{name: "unknown action", args: []string{"clean", "--all", "--action", "delete"}, want: "unknown action"},
		{name: "no concurrency", args: []string{"clean", "--all", "--concurrency", "0"}, want: "--concurrency"},
		{name: "sort without list", args: []string{"clean", "--all", "--sort", "repo"}, want: "add --list"},
		{name: "unknown sort key", args: []string{"clean", "--all", "--list", "--sort", "title"}, want: "invalid --sort key"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newFakeInbox()
			got := run(t, c, t.TempDir(), tt.args...)
			if got.err == nil || !strings.Contains(got.err.Error(), tt.want) {
				t.Errorf("error = %v, want one containing %q", got.err, tt.want)
			}
			if len(c.actions) != 0 {
				t.Errorf("applied actions: %v", c.actions)
			}
		})
	}
}

func TestCleanList(t *testing.T) {
	merged := "acme/api\t#1\tpr\tmerged\treview_requested\t"
	open := "acme/web\t#2\tpr\topen\treview_requested\t"
	tests := []struct {
		name        string
		args        []string
		fail        map[string]bool
		wantPrefix  string
		wantRows    []string
		notWantRows []string
	}{
		{
			name:       "dry run",
			args:       []string{"--state", "merged,open"},
			wantPrefix: "Would mark 2 notifications as done.\n\n",
			wantRows:   []string{merged, open, "\nRun again with --apply to do it.\n"},
		},
		{
			name:        "apply",
			args:        []string{"--state", "merged,open", "--apply"},
			fail:        map[string]bool{"2": true},
			wantPrefix:  "Marked 1 notification as done:\n\n",
			wantRows:    []string{merged},
			notWantRows: []string{open},
		},
		{
			name:       "rules dry run",
			args:       []string{"--rules", "--rules-file", writeRules(t, testRules)},
			wantPrefix: "Rule \"merged\": Would mark 1 notification as done.\n\n",
			wantRows:   []string{merged, "Rule \"prs\": Would unsubscribe from 2 notifications.\n\n" + open},
		},
		{
			name:       "rules apply",
			args:       []string{"--rules", "--rules-file", writeRules(t, testRules), "--apply"},
			wantPrefix: "Rule \"merged\": Marked 1 notification as done:\n\n" + merged,
			wantRows:   []string{"\n\nRule \"prs\": Unsubscribed from 2 notifications:\n\n" + open},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newFakeInbox()
			c.fail = tt.fail
			got := run(t, c, t.TempDir(), append([]string{"clean", "--list"}, tt.args...)...)
			if got.err != nil && tt.fail == nil {
				t.Fatal(got.err)
			}
			if !strings.HasPrefix(got.stdout, tt.wantPrefix) {
				t.Errorf("stdout doesn't start with %q:\n%s", tt.wantPrefix, got.stdout)
			}
			for _, want := range tt.wantRows {
				if !strings.Contains(got.stdout, want) {
					t.Errorf("stdout doesn't contain %q:\n%s", want, got.stdout)
				}
			}
			for _, row := range tt.notWantRows {
				if strings.Contains(got.stdout, row) {
					t.Errorf("stdout contains %q:\n%s", row, got.stdout)
				}
			}
			if strings.Contains(got.stdout, "By repository") {
				t.Errorf("stdout has a summary:\n%s", got.stdout)
			}
		})
	}
}

func TestCleanListSort(t *testing.T) {
	// The filters don't need the details, but the table shows the states.
	got := run(t, newFakeInbox(), t.TempDir(), "clean", "--type", "pr", "--list", "--sort", "repo")
	if got.err != nil {
		t.Fatal(got.err)
	}
	want := "Would mark 3 notifications as done.\n\n" + rows(1, 4, 2)
	if !strings.HasPrefix(got.stdout, want) {
		t.Errorf("stdout doesn't start with %q:\n%s", want, got.stdout)
	}

	got = run(t, newFakeInbox(), t.TempDir(), "clean", "--type", "pr", "--list", "--sort", "state", "--apply")
	if got.err != nil {
		t.Fatal(got.err)
	}
	if want := "Marked 3 notifications as done:\n\n" + rows(2, 1, 4); got.stdout != want {
		t.Errorf("stdout =\n%s\nwant\n%s", got.stdout, want)
	}
}
