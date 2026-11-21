//go:build !linux

package wireless

import (
	"context"
	"fmt"
)

// probeAAChannel is Linux-only.
func probeAAChannel(ctx context.Context, addr string, logf func(string, ...any)) (uint8, error) {
	return 0, fmt.Errorf("probe: Bluetooth is only implemented on Linux")
}
