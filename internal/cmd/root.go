// Package cmd implements the gh inbox command line.
package cmd

import (
	"context"
	"fmt"
	"io"
	"os"
	"runtime/debug"
	"strings"
	"time"

	"github.com/cli/go-gh/v2/pkg/auth"
	"github.com/cli/go-gh/v2/pkg/browser"
	"github.com/cli/go-gh/v2/pkg/term"
	"github.com/spf13/cobra"

	"github.com/maraino/gh-inbox/internal/action"
	"github.com/maraino/gh-inbox/internal/cache"
	"github.com/maraino/gh-inbox/internal/github"
	"github.com/maraino/gh-inbox/internal/inbox"
)

// Execute runs the gh inbox command line with the process arguments.
func Execute(ctx context.Context) error {
	return newRootCmd(defaultEnv()).ExecuteContext(ctx)
}

// client is the part of the GitHub client the commands use.
type client interface {
	Notifications(ctx context.Context) ([]inbox.Notification, error)
	SubjectDetails(ctx context.Context, notifications []inbox.Notification, opts github.DetailsOptions) (map[string]*inbox.Subject, error)
	Viewer(ctx context.Context) (inbox.Identity, error)
	Unwatch(ctx context.Context, repo string) error
	action.Client
}

// env is what the commands use from outside the process, so that tests can
// replace it.
type env struct {
	out    io.Writer
	errOut io.Writer
	// isTTY reports whether out is a terminal, and width is its width.
	isTTY bool
	width int
	// showProgress reports whether errOut is a terminal that can show
	// progress.
	showProgress bool
	now          func() time.Time
	newClient    func(github.Options) (client, error)
	cachePath    func(host string) (string, error)
	// cacheDir returns where API responses for a host are cached.
	cacheDir func(host string) (string, error)
	// rulesPath returns where the rules file is.
	rulesPath func() (string, error)
	// openKeyboard starts reading key presses from the terminal; Ctrl-C
	// calls interrupt. It's nil when stdin or stdout isn't a terminal.
	openKeyboard func(interrupt func()) (keyboard, error)
	// openBrowser opens a URL in the web browser.
	openBrowser func(url string) error
}

func defaultEnv() *env {
	t := term.FromEnv()
	width, _, err := t.Size()
	if err != nil {
		width = 80
	}
	var openKeyboard func(func()) (keyboard, error)
	if term.IsTerminal(os.Stdin) && t.IsTerminalOutput() {
		openKeyboard = openTerminalKeyboard
	}
	return &env{
		out:          t.Out(),
		errOut:       t.ErrOut(),
		isTTY:        t.IsTerminalOutput(),
		width:        width,
		showProgress: term.IsTerminal(os.Stderr),
		now:          time.Now,
		newClient:    func(opts github.Options) (client, error) { return github.New(opts) },
		cachePath:    cache.DefaultPath,
		cacheDir:     defaultCacheDir,
		rulesPath:    defaultRulesPath,
		openKeyboard: openKeyboard,
		// Finding the browser reads the gh configuration, so only do it
		// when one is opened.
		openBrowser: func(url string) error { return browser.New("", t.Out(), t.ErrOut()).Browse(url) },
	}
}

// rootOptions are the flags shared by every command.
type rootOptions struct {
	hostname string
	noCache  bool
	verbose  bool
}

func newRootCmd(e *env) *cobra.Command {
	opts := &rootOptions{}
	cmd := &cobra.Command{
		Use:   "inbox",
		Short: "Clean up your GitHub notifications inbox",
		Long: `Clean up your GitHub notifications inbox.

Use "gh inbox list" to see which notifications match a set of filters, and
"gh inbox clean" to mark them as done, mark them as read, or unsubscribe from
them. "gh inbox triage" goes through them one at a time. "gh inbox unwatch"
stops watching the repositories that fill the inbox.`,
		Annotations: map[string]string{
			cobra.CommandDisplayNameAnnotation: "gh inbox",
		},
		Version:       version(),
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	cmd.SetOut(e.out)
	cmd.SetErr(e.errOut)

	flags := cmd.PersistentFlags()
	flags.StringVar(&opts.hostname, "hostname", "", "GitHub `host` to use (default: the gh default host)")
	flags.BoolVar(&opts.noCache, "no-cache", false,
		"Ignore the local record of notifications already marked as done, and cached API responses")
	flags.BoolVar(&opts.verbose, "verbose", false, "Log API requests and retries to stderr")

	cmd.AddCommand(newListCmd(opts, e), newCleanCmd(opts, e), newTriageCmd(opts, e), newUnwatchCmd(opts, e))
	return cmd
}

// version returns the module version the binary was built from: a tag such
// as "v1.2.0", or a pseudo-version for a build between tags.
func version() string {
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" {
		return info.Main.Version
	}
	return "(devel)"
}

// host returns the GitHub host to use.
func (o *rootOptions) host() string {
	if o.hostname != "" {
		return o.hostname
	}
	host, _ := auth.DefaultHost()
	return host
}

func (o *rootOptions) client(e *env) (client, error) {
	opts := github.Options{Host: o.host()}
	if o.verbose {
		opts.Log = e.errOut
	}
	if e.cacheDir != nil {
		// Without a cache directory, nothing is cached.
		opts.CacheDir, _ = e.cacheDir(opts.Host)
		// --no-cache doesn't read the cache, but still renews it, so that
		// the changes made now don't leave an out-of-date list behind.
		opts.RefreshCache = o.noCache
	}
	return e.newClient(opts)
}

// defaultCacheDir returns where API responses for a host are cached:
// $XDG_CACHE_HOME/gh-inbox/HOST, or ~/.cache/gh-inbox/HOST.
func defaultCacheDir(host string) (string, error) {
	return cache.XDGPath("XDG_CACHE_HOME", ".cache", "gh-inbox", host)
}

// repoPages are the repository pages that list subjects of each type
// without a web URL of their own.
var repoPages = map[inbox.SubjectType]string{
	inbox.TypeCheckSuite: "/actions",
	inbox.TypeRelease:    "/releases",
	inbox.TypeDiscussion: "/discussions",
	inbox.TypeAdvisory:   "/security/advisories",
	inbox.TypeDependabot: "/security/dependabot",
}

// webURL returns the web page of a notification: its pull request, issue or
// commit, or the repository page that lists subjects of its type.
func (o *rootOptions) webURL(n inbox.Notification) string {
	if n.URL != "" {
		return n.URL
	}
	repo := "https://" + o.host() + "/" + n.Repo
	if n.Type == inbox.TypeCommit {
		if _, sha, ok := strings.Cut(n.SubjectURL, "/commits/"); ok && sha != "" && !strings.Contains(sha, "/") {
			return repo + "/commit/" + sha
		}
	}
	return repo + repoPages[n.Type]
}

// openCache opens the record of threads marked as done. It returns nil with
// --no-cache.
func (o *rootOptions) openCache(e *env) (*cache.Store, error) {
	if o.noCache {
		return nil, nil
	}
	path, err := e.cachePath(o.host())
	if err != nil {
		return nil, fmt.Errorf("locating the record of notifications marked as done: %w", err)
	}
	return cache.Open(path)
}
