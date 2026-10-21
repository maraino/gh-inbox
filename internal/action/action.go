// Package action applies an action, such as marking as done, to many
// notifications at once.
package action

import (
	"context"
	"fmt"
	"sync"

	"golang.org/x/sync/errgroup"

	"github.com/maraino/gh-inbox/internal/inbox"
)

// Kind is an action that can be applied to a notification.
type Kind string

// The actions.
const (
	Done        Kind = "done"
	Read        Kind = "read"
	Unsubscribe Kind = "unsubscribe"
)

// ParseKind returns the action with the given name.
func ParseKind(name string) (Kind, error) {
	switch k := Kind(name); k {
	case Done, Read, Unsubscribe:
		return k, nil
	default:
		return "", fmt.Errorf("unknown action %q: valid actions are done, read, unsubscribe", name)
	}
}

// Client applies the actions to threads.
type Client interface {
	MarkDone(ctx context.Context, threadID string) error
	MarkRead(ctx context.Context, threadID string) error
	Unsubscribe(ctx context.Context, threadID string) error
}

// Failure is a notification the action couldn't be applied to.
type Failure struct {
	Notification inbox.Notification
	Err          error
}

// Runner applies an action to notifications concurrently.
type Runner struct {
	Client Client
	Kind   Kind
	// Concurrency is how many requests are made at the same time. Values
	// below 1 mean 1.
	Concurrency int
	// Progress, when set, is called after each notification with how many
	// are finished. Calls don't overlap.
	Progress func(finished, total int)
}

// Run applies the action to every notification. It doesn't stop at the
// first failure: it returns the notifications the action was applied to and
// those it failed for, both in their original order. Once ctx is done, the
// remaining notifications fail with the context's error.
func (r *Runner) Run(ctx context.Context, notifications []inbox.Notification) (succeeded []inbox.Notification, failed []Failure) {
	apply := r.apply()
	errs := make([]error, len(notifications))

	var (
		mu       sync.Mutex
		finished int
	)
	var g errgroup.Group
	g.SetLimit(max(r.Concurrency, 1))
	for i, n := range notifications {
		g.Go(func() error {
			if errs[i] = ctx.Err(); errs[i] == nil {
				errs[i] = apply(ctx, n.ID)
			}
			if r.Progress != nil {
				mu.Lock()
				finished++
				r.Progress(finished, len(notifications))
				mu.Unlock()
			}
			return nil
		})
	}
	_ = g.Wait() // The goroutines report their errors in errs.

	for i, n := range notifications {
		if errs[i] != nil {
			failed = append(failed, Failure{Notification: n, Err: errs[i]})
		} else {
			succeeded = append(succeeded, n)
		}
	}
	return succeeded, failed
}

func (r *Runner) apply() func(context.Context, string) error {
	switch r.Kind {
	case Done:
		return r.Client.MarkDone
	case Read:
		return r.Client.MarkRead
	case Unsubscribe:
		return r.Client.Unsubscribe
	default:
		return func(context.Context, string) error {
			return fmt.Errorf("unknown action %q", r.Kind)
		}
	}
}
