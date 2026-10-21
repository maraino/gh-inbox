// Command gh-inbox is a GitHub CLI extension that cleans up the
// notifications inbox.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"

	"github.com/maraino/gh-inbox/internal/cmd"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	err := cmd.Execute(ctx)
	stop()
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
