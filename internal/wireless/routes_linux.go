//go:build linux

package wireless

import (
	"fmt"
	"net"
	"os/exec"
	"strings"
	"time"

	"github.com/godbus/dbus/v5"
)

// Route policy while projecting.
//
// Joining the head unit's access point installs its default route, and that AP
// has no internet: the phone then loses connectivity for everything except the
// projection, so online navigation dies exactly when it is needed most. Android
// Auto on a real phone keeps the cellular default and only routes the car's
// subnet over Wi-Fi; this is the same thing by hand.
//
// The order matters: ConnMan usually drops the cellular context when Wi-Fi
// becomes the default service, so removing the Wi-Fi default first would leave
// the phone with no default at all (measured 2026-11-28: "no internet on the
// Jolla" while mobile data was enabled). A replacement default is looked for —
// and asked for — before the access point's is removed.
func ApplyCarRoutes(iface string, logf func(string, ...any)) error {
	if iface == "" {
		return fmt.Errorf("route policy: no interface")
	}
	gw := gatewayOf(iface)
	if gw == "" {
		return nil
	}

	if !hasOtherDefault(iface) {
		logf("route: no default beyond %s, asking ConnMan for the mobile connection", iface)
		connectCellular(logf)
		deadline := time.Now().Add(8 * time.Second)
		for !hasOtherDefault(iface) && time.Now().Before(deadline) {
			time.Sleep(500 * time.Millisecond)
		}
		if !hasOtherDefault(iface) {
			// Keep the projection working: its subnet stays reachable through
			// the access point, and the user is told why the internet is out.
			logf("route: still no default after asking for mobile data — leaving the access point's default in place")
			return nil
		}
	}

	removed := 0
	for _, spec := range [][]string{
		{"route", "del", "default", "via", gw, "dev", iface},
		{"route", "del", "default", "dev", iface},
	} {
		if out, err := runIP(spec...); err != nil {
			if !strings.Contains(out, "No such process") && !strings.Contains(out, "not found") {
				logf("route: ip %s: %v (%s)", strings.Join(spec, " "), err, strings.TrimSpace(out))
			}
			continue
		}
		removed++
	}
	if removed > 0 {
		logf("route: removed %d default route(s) via %s (%s); internet stays on %s",
			removed, gw, iface, otherDefaultDev(iface))
	}
	probeInternet(logf)
	return nil
}

// probeInternet says, in the log, whether the phone can actually reach the
// outside world while projecting. It dials an address, not a name: a DNS
// failure and a routing failure look identical from the car's screen, and the
// journal has to tell them apart.
func probeInternet(logf func(string, ...any)) {
	for _, target := range []string{"1.1.1.1:443", "8.8.8.8:53"} {
		c, err := net.DialTimeout("tcp", target, 2*time.Second)
		if err == nil {
			c.Close()
			logf("route: internet reachable (%s)", target)
			return
		}
		logf("route: internet unreachable (%s): %v", target, err)
	}
}

// RestoreCarRoutes puts back the default route the access point's DHCP asked
// for. Called when the session ends: the phone then behaves as before (if it is
// still on the car's AP) or the home network installs its own default.
func RestoreCarRoutes(iface string, logf func(string, ...any)) error {
	if iface == "" {
		return nil
	}
	gw := gatewayOf(iface)
	if gw == "" {
		return nil
	}
	if out, err := runIP("route", "show", "default", "dev", iface); err == nil && strings.TrimSpace(out) != "" {
		return nil // already back
	}
	if out, err := runIP("route", "replace", "default", "via", gw, "dev", iface); err != nil {
		return fmt.Errorf("restore default via %s: %v (%s)", gw, err, strings.TrimSpace(out))
	}
	logf("route: default restored via %s (%s)", gw, iface)
	return nil
}

// hasOtherDefault reports whether a default route exists on another interface.
func hasOtherDefault(iface string) bool { return otherDefaultDev(iface) != "" }

// otherDefaultDev returns the interface of a default route that is not iface.
func otherDefaultDev(iface string) string {
	out, err := runIP("route", "show", "default")
	if err != nil {
		return ""
	}
	for _, d := range parseDefaultDevs(out) {
		if d != iface {
			return d
		}
	}
	return ""
}

// parseDefaultDevs lists the interface of every default route in `ip route show
// default` output. Split out because it is the one part of the policy that can
// be checked without touching a live routing table.
func parseDefaultDevs(out string) []string {
	var devs []string
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(strings.TrimSpace(line))
		if len(f) < 2 || f[0] != "default" {
			continue
		}
		for i := 0; i+1 < len(f); i++ {
			if f[i] == "dev" {
				devs = append(devs, f[i+1])
				break
			}
		}
	}
	return devs
}

// connectCellular asks ConnMan to bring the mobile connection back: Wi-Fi being
// the default service is what made ConnMan drop it.
func connectCellular(logf func(string, ...any)) {
	conn, err := dbus.SystemBus()
	if err != nil {
		return
	}
	services, err := connmanServices()
	if err != nil {
		return
	}
	for _, s := range services {
		if s.Type != "cellular" {
			continue
		}
		if s.State == "online" || s.State == "ready" {
			logf("route: cellular %q is already %s", s.Name, s.State)
			continue
		}
		if err := conn.Object(connmanService, s.Path).Call("net.connman.Service.Connect", 0).Err; err != nil {
			logf("route: cellular %q connect: %v", s.Name, err)
			continue
		}
		logf("route: asked ConnMan to connect cellular %q (was %s)", s.Name, s.State)
	}
}

// gatewayOf returns the IPv4 gateway the interface got from DHCP, or "".
func gatewayOf(iface string) string {
	out, err := runIP("route", "show", "dev", iface)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(strings.TrimSpace(line))
		if len(f) >= 3 && f[0] == "default" && f[1] == "via" {
			return f[2]
		}
	}
	return ""
}

// runIP runs iproute2 and returns its output.
func runIP(args ...string) (string, error) {
	bin := "/usr/sbin/ip"
	if _, err := exec.LookPath(bin); err != nil {
		bin = "ip"
	}
	out, err := exec.Command(bin, args...).CombinedOutput()
	return string(out), err
}
