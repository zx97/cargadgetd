//go:build !linux

package wireless

import "fmt"

func sdpRFCOMMChannel(string, func(string, ...any)) (uint8, error) {
	return 0, fmt.Errorf("sdp: only implemented on Linux")
}
