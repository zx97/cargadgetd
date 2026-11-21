//go:build !linux

package wireless

import (
	"fmt"
	"os"
)

func ensureAAProfile(func(string, ...any)) (<-chan *os.File, error) {
	return nil, fmt.Errorf("wireless: Bluetooth profile is only implemented on Linux")
}
