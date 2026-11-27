//go:build linux

package wireless

import (
	"fmt"
	"os/exec"
	"strings"
)

// Route policy while projecting.
//
// Joining the head unit's access point installs its default route, and that AP
// has no internet: the phone then loses connectivity for everything except the
// projection, so online navigation dies exactly when it is needed most. Android
// Auto on a real phone keeps the cellular default and only routes the car's
// subnet over Wi-Fi; this is the same thing by hand.
//
// ApplyCarRoutes must not cut the projection: the car's own subnet stays on the
// interface (the DHCP link route), only the default route is removed.
func ApplyCarRoutes(iface string, logf func(string, ...any)) error {
	if iface == "" {
		return fmt.Errorf("route policy: no interface")
	}
	gw := gatewayOf(iface)
	if gw == "" {
		// No gateway on this interface: nothing to remove, and the subnet route
		// is already there.
		return nil
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
	logf("route: removed %d default route(s) via %s (%s), the projection keeps its subnet", removed, gw, iface)
	return nil
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

func runIP(args ...string) (string, error) {
	bin := "/usr/sbin/ip"
	if _, err := exec.LookPath(bin); err != nil {
		bin = "ip"
	}
	out, err := exec.Command(bin, args...).CombinedOutput()
	return string(out), err
}
