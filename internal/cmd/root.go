// Package cmd implements the gh inbox command line.
package cmd

import (
	"context"
	"fmt"
	"io"
	"os"
	"runtime/debug"
	"time"

	"github.com/cli/go-gh/v2/pkg/auth"
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
}

func defaultEnv() *env {
	t := term.FromEnv()
	width, _, err := t.Size()
	if err != nil {
		width = 80
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
them.`,
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

	cmd.AddCommand(newListCmd(opts, e), newCleanCmd(opts, e))
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
