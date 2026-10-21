// Package cmd implements the gh inbox command line.
package cmd

import (
	"context"

	"github.com/spf13/cobra"
)

// Execute runs the gh inbox command line with the process arguments.
func Execute(ctx context.Context) error {
	return newRootCmd().ExecuteContext(ctx)
}

// rootOptions are the flags shared by every command.
type rootOptions struct {
	hostname string
	noCache  bool
	verbose  bool
}

func newRootCmd() *cobra.Command {
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
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	flags := cmd.PersistentFlags()
	flags.StringVar(&opts.hostname, "hostname", "", "GitHub `host` to use (default: the gh default host)")
	flags.BoolVar(&opts.noCache, "no-cache", false, "Ignore the local record of notifications already marked as done")
	flags.BoolVar(&opts.verbose, "verbose", false, "Log API requests and retries to stderr")

	return cmd
}
