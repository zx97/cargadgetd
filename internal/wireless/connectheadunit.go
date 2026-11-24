package wireless

import (
	"context"
	"time"
)

// ConnectHeadUnit asks BlueZ to connect the paired head unit. Stock Android
// Auto does this from its car-Bluetooth service as soon as it sees the car's
// address in the service discovery: the head unit then has an active
// Bluetooth link to the same phone while the AA session runs, which is what
// lets it associate the USB device with the Bluetooth bond (one entry in its
// device list instead of two).
//
// address may be empty: the paired device advertising the Android Auto UUID is
// used, the same rule Dial applies.
func ConnectHeadUnit(address string, attempts int, logf func(string, ...any)) {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	if attempts < 1 {
		attempts = 1
	}
	dev, err := FindDevice("", address)
	if err != nil {
		logf("bt-link: %v", err)
		return
	}
	logf("bt-link: head unit %s %s connected=%v trusted=%v", dev.Name, dev.Address, dev.Connected, dev.Trusted)
	if dev.Connected {
		logf("bt-link: already connected")
		return
	}
	for i := 1; i <= attempts; i++ {
		t := time.Now()
		err := dev.Connect(context.Background())
		if err != nil {
			logf("bt-link: Device1.Connect attempt %d/%d failed after %s: %v", i, attempts, sinceText(t), err)
			time.Sleep(3 * time.Second)
			continue
		}
		logf("bt-link: Device1.Connect attempt %d/%d returned OK in %s", i, attempts, sinceText(t))
		if dev.WaitConnected(15*time.Second, logf) {
			logf("bt-link: link up")
			return
		}
		logf("bt-link: link still down after attempt %d", i)
	}
	logf("bt-link: giving up after %d attempts (the car did not answer the page)", attempts)
}
