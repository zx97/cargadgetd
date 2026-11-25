// Command wifilab joins a head unit's access point and samples the wireless
// link before, during and after the join.
//
// It exists to measure one thing: ConnMan deauthenticated the station right
// after JoinWithAgent returned, because the registered agent that supplied the
// passphrase of an unsaved service is what keeps the secret alive. -release
// reproduces the old behaviour (unregister at the end of the join) so the two
// runs can be compared on the bench without a head unit.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/cargadgetd/cargadgetd/internal/wireless"
)

// link returns (ssid, driverState) as the kernel reports them.
func link(iface string) (string, string) {
	out, err := exec.Command("/usr/sbin/iw", "dev", iface, "link").Output()
	if err != nil {
		return "", "no-iw"
	}
	ssid := ""
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "SSID:") {
			ssid = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "SSID:"))
		}
	}
	if ssid == "" {
		return "", "disconnected"
	}
	return ssid, "associated"
}

func main() {
	ssid := flag.String("ssid", "", "access point to join")
	psk := flag.String("psk", "", "passphrase")
	iface := flag.String("iface", "wlan0", "wireless interface")
	secs := flag.Int("secs", 90, "how long to watch after the join returns")
	release := flag.Bool("release", false, "unregister the ConnMan agent right after the join (old behaviour)")
	sampleMs := flag.Int("sample-ms", 200, "link sampling period")
	flag.Parse()
	if *ssid == "" {
		log.Fatal("wifilab: -ssid is required")
	}

	start := time.Now()
	stamp := func() string { return fmt.Sprintf("+%6.2fs", time.Since(start).Seconds()) }

	// Sample from the very beginning: the interesting part is the transition.
	prev := "?"
	stop := make(chan struct{})
	go func() {
		t := time.NewTicker(time.Duration(*sampleMs) * time.Millisecond)
		defer t.Stop()
		for {
			name, state := link(*iface)
			cur := state + " " + name
			if cur != prev {
				log.Printf("%s link: %s", stamp(), cur)
				prev = cur
			}
			select {
			case <-stop:
				return
			case <-t.C:
			}
		}
	}()

	if err := wireless.JoinWithAgent(*ssid, *psk, log.Printf); err != nil {
		log.Printf("%s join failed: %v", stamp(), err)
	} else {
		log.Printf("%s join returned", stamp())
	}
	if *release {
		wireless.ReleaseAgent()
		log.Printf("%s agent released (old behaviour)", stamp())
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	select {
	case <-time.After(time.Duration(*secs) * time.Second):
	case <-ctx.Done():
	}
	close(stop)

	if *release {
		// Nothing to restore: the agent is already gone.
		return
	}
	wireless.RestoreHomeWiFi(log.Printf)
	log.Printf("%s done", stamp())
}
