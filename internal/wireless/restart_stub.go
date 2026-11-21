//go:build !linux

package wireless

import "fmt"

func RestartProcess() error {
	return fmt.Errorf("wireless: process restart is only implemented on Linux")
}
