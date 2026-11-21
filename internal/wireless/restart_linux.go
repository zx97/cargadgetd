//go:build linux

package wireless

import (
	"os"
	"syscall"
)

// RestartProcess replaces the current process with a fresh copy of the
// installed binary. os.Args[0] is used so a file replaced by mv is picked up
// (os.Executable would point at the deleted inode).
func RestartProcess() error {
	exe := os.Args[0]
	if exe == "" {
		var err error
		exe, err = os.Executable()
		if err != nil {
			return err
		}
	}
	return syscall.Exec(exe, os.Args, os.Environ())
}
