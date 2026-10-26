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
	xterm "golang.org/x/term"

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
	// rulesPath returns where the rules file is.
	rulesPath func() (string, error)
	// readKey reads a key press from the terminal. It's nil when stdin or
	// stdout isn't a terminal.
	readKey func() (byte, error)
	// openBrowser opens a URL in the web browser.
	openBrowser func(url string) error
}

func defaultEnv() *env {
	t := term.FromEnv()
	width, _, err := t.Size()
	if err != nil {
		width = 80
	}
	var readKey func() (byte, error)
	if term.IsTerminal(os.Stdin) && t.IsTerminalOutput() {
		readKey = readTerminalKey
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
		rulesPath:    defaultRulesPath,
		readKey:      readKey,
		// Finding the browser reads the gh configuration, so only do it
		// when one is opened.
		openBrowser: func(url string) error { return browser.New("", t.Out(), t.ErrOut()).Browse(url) },
	}
}

// readTerminalKey reads a key press from stdin, a terminal, without waiting
// for Enter.
func readTerminalKey() (byte, error) {
	fd := int(os.Stdin.Fd())
	state, err := xterm.MakeRaw(fd)
	if err != nil {
		return 0, err
	}
	defer func() { _ = xterm.Restore(fd, state) }() // Nothing to do if it fails.
	var b [1]byte
	if _, err := os.Stdin.Read(b[:]); err != nil {
		return 0, err
	}
	return b[0], nil
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
them. "gh inbox triage" goes through them one at a time.`,
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
	flags.BoolVar(&opts.noCache, "no-cache", false, "Ignore the local record of notifications already marked as done")
	flags.BoolVar(&opts.verbose, "verbose", false, "Log API requests and retries to stderr")

	cmd.AddCommand(newListCmd(opts, e), newCleanCmd(opts, e), newTriageCmd(opts, e))
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
	return e.newClient(opts)
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
