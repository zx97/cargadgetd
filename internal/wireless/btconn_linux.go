//go:build linux

package wireless

import (
	"errors"

	"golang.org/x/sys/unix"
)

// dialBluetooth creates a Bluetooth socket, connects it and retries when
// connect(2) is interrupted by a signal (EINTR). The Go runtime delivers
// asynchronous preemption signals that can interrupt a blocking Bluetooth
// connect; that is not a car or link error, and treating it as one made the
// daemon give up on attempts the head unit had actually accepted. A fresh
// socket is used for the retry: the interrupted attempt may still be pending in
// the kernel, and reusing the fd would return EALREADY.
// maxEINTR covers Go's own SIGURG preemption, which interrupts a blocking
// Bluetooth connect several times per second on a busy process. It is not a
// failure and must never cost a channel probe.
const maxEINTR = 20

func dialBluetooth(typ, proto int, sa unix.Sockaddr, attempts int, logf func(string, ...any)) (int, error) {
	if attempts < maxEINTR {
		attempts = maxEINTR
	}
	var last error
	for i := 1; i <= attempts; i++ {
		fd, err := unix.Socket(unix.AF_BLUETOOTH, typ|unix.SOCK_CLOEXEC, proto)
		if err != nil {
			return -1, err
		}
		if err := unix.Connect(fd, sa); err != nil {
			unix.Close(fd)
			last = err
			if errors.Is(err, unix.EINTR) && i < attempts {
				if logf != nil {
					logf("bt: connect interrupted by a signal (EINTR), retry %d/%d", i, attempts)
				}
				continue
			}
			return -1, err
		}
		return fd, nil
	}
	return -1, last
}
