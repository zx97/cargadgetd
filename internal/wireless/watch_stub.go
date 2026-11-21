//go:build !linux

package wireless

import (
	"context"
	"time"
)

// WaitForChange is Linux-only; elsewhere it just waits for max.
func WaitForChange(ctx context.Context, opt Options, max time.Duration, logf func(string, ...any)) string {
	sleepCtx(ctx, max)
	return ""
}

func sleepCtx(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}
