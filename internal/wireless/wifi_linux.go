//go:build linux

package wireless

import (
	"context"
	"fmt"
	"net"
	"os/exec"
	"strings"
	"time"
)

// wifiSSID returns the SSID wlan0 is associated with, or "".
func wifiSSID(iface string) string {
	out, err := exec.Command("/usr/sbin/iw", "dev", iface, "link").Output()
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "SSID:") {
			return strings.TrimSpace(strings.TrimPrefix(line, "SSID:"))
		}
	}
	return ""
}

// wifiIPv4 returns the first IPv4 address on iface, or "".
func wifiIPv4(iface string) string {
	ifi, err := net.InterfaceByName(iface)
	if err != nil {
		return ""
	}
	addrs, err := ifi.Addrs()
	if err != nil {
		return ""
	}
	for _, a := range addrs {
		if ipnet, ok := a.(*net.IPNet); ok && ipnet.IP.To4() != nil {
			return ipnet.IP.String()
		}
	}
	return ""
}

// logWiFi logs the current association and IPv4 address.
func logWiFi(iface string, logf func(string, ...any)) {
	if logf == nil {
		return
	}
	logf("wifi: %s current ssid=%q ip=%q", iface, wifiSSID(iface), wifiIPv4(iface))
}

// waitWifi blocks until iface is associated with want and has an IPv4 address.
// psk comes from the WPP handshake: with it the AP is provisioned on the spot,
// which is what a head unit whose SSID changes between sessions needs.
func waitWifi(ctx context.Context, iface, want, psk, wantIPPrefix string, timeout time.Duration, logf func(string, ...any), st *Status) error {
	start := time.Now()
	nextJoin := start
	prev := ""
	joins := 0
	for {
		ssid := wifiSSID(iface)
		ip := wifiIPv4(iface)
		match := want == "" || strings.EqualFold(ssid, want)
		// The address must come from the head unit's own network: after a join
		// the interface keeps its previous lease for a moment, and taking that
		// as success made the TCP dial leave through the home router.
		ipOK := ip != "" && (wantIPPrefix == "" || strings.HasPrefix(ip, wantIPPrefix))
		if cur := fmt.Sprintf("ssid=%q ip=%q", ssid, ip); cur != prev {
			logf("wifi: %s now %s at +%s (want %q)", iface, cur, sinceText(start), want)
			prev = cur
		}
		st.Setf("Car Wi-Fi", match && ipOK, "current=%q want=%q ip=%q", ssid, want, ip)
		if match && ipOK {
			logf("wifi: %s ssid=%q ip=%s after %s", iface, ssid, ip, sinceText(start))
			st.Step("Wi-Fi joined: %q (%s)", ssid, ip)
			return nil
		}
		// A saved network does not switch while another one is preferred.
		// Ask ConnMan to connect the car's AP, then keep polling.
		if want != "" && time.Now().After(nextJoin) {
			nextJoin = time.Now().Add(10 * time.Second)
			joins++
			t := time.Now()
			if err := connmanJoin(want, psk); err != nil {
				logf("wifi: join #%d %q failed after %s: %v", joins, want, sinceText(t), err)
				st.Set("Wi-Fi join", false, err.Error())
			} else {
				logf("wifi: join #%d %q accepted by ConnMan in %s", joins, want, sinceText(t))
				st.Set("Wi-Fi join", true, "requested from ConnMan")
			}
		}
		if time.Since(start) > timeout {
			return fmt.Errorf("wifi: %s not on %q with IPv4 after %s (ssid=%q ip=%q, %d join calls)",
				iface, want, timeout, ssid, ip, joins)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}
