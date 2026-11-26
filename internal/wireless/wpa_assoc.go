//go:build linux

package wireless

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// AssociateWiFi joins a head unit's access point without ConnMan's
// Service.Connect.
//
// Why this exists: ConnMan's Connect returns about 34 s after it was issued, and
// completing it re-applies the service — which deauthenticates the station. On
// the bench that landed 600 ms after `join accepted` and, in the session before,
// 24 s into the projection, where the head unit answers a lost association by
// ending Android Auto:
//
//	02:07:49.837 link: wlan0 ssid="carplay_wifi_4ADC" ip="192.168.43.2"
//	02:08:14.224 wifi: join #1 "carplay_wifi_4ADC" accepted by ConnMan in 34.531s
//	02:08:14.829 link: LOST wlan0 ssid="" ip="192.168.43.2"
//
// The association itself is fast and does not need ConnMan: wpa_supplicant adds
// the network and associates in a few seconds. Only the lease is missing, and
// busybox's udhcpc takes it. Nothing is left in flight, so nothing can be
// re-applied while the head unit is projecting.
//
// Root only: it writes the interface address. Reachable through
// `cargadgetctl -wifi-assoc`.
func AssociateWiFi(ssid, passphrase, iface string, logf func(string, ...any)) error {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	if iface == "" {
		iface = "wlan0"
	}
	if strings.TrimSpace(ssid) == "" {
		return fmt.Errorf("wpa: empty ssid")
	}
	// Keep the saved home network from pulling the interface back mid-session.
	suspendOtherAutoConnect(ssid)

	if err := wpaCliJoin(ssid, passphrase, iface); err != nil {
		return err
	}
	deadline := time.Now().Add(20 * time.Second)
	for !strings.EqualFold(wifiSSID(iface), ssid) {
		if time.Now().After(deadline) {
			return fmt.Errorf("wpa: %s did not associate with %q in 20s", iface, ssid)
		}
		time.Sleep(250 * time.Millisecond)
	}
	logf("wpa: associated %q, taking the lease", ssid)
	return dhcpLease(iface, logf)
}

// wpaCliJoin adds and selects an access point through wpa_cli.
//
// Not the D-Bus API: this wpa_supplicant build answers AddNetwork with
// "invalid message format" whatever the dictionary holds (measured 2026-11-25
// on the Jolla). wpa_cli talks to the same control socket over a text protocol
// and works, and the helper that runs it is root anyway.
func wpaCliJoin(ssid, passphrase, iface string) error {
	if ssid == "" || strings.ContainsAny(ssid, "\"\n") {
		return fmt.Errorf("wpa_cli: unusable ssid %q", ssid)
	}
	if strings.ContainsAny(passphrase, "\"\n") {
		return fmt.Errorf("wpa_cli: passphrase contains a quote or newline")
	}
	cli := func(args ...string) (string, error) {
		full := append([]string{"-i", iface, "-p", "/var/run/wpa_supplicant"}, args...)
		out, err := exec.Command("/usr/sbin/wpa_cli", full...).CombinedOutput()
		s := strings.TrimSpace(string(out))
		if err != nil {
			return s, fmt.Errorf("wpa_cli %s: %v (%s)", strings.Join(args, " "), err, s)
		}
		if strings.Contains(s, "FAIL") {
			return s, fmt.Errorf("wpa_cli %s: %s", strings.Join(args, " "), lastNonEmpty(s))
		}
		return s, nil
	}

	// Drop the dead carplay_* blocks this unit leaves behind: it regenerates its
	// SSID every power cycle, so the list grows for ever otherwise.
	if list, err := cli("list_networks"); err == nil {
		for _, line := range strings.Split(list, "\n") {
			f := strings.Split(line, "\t")
			if len(f) < 2 || f[0] == "network id" {
				continue
			}
			quoted := fmt.Sprintf("%q", ssid)
			if f[1] == quoted {
				if _, err := cli("remove_network", f[0]); err == nil {
					continue
				}
			}
			if strings.HasPrefix(f[1], "carplay_wifi") {
				_, _ = cli("remove_network", f[0])
			}
		}
	}

	out, err := cli("add_network")
	if err != nil {
		return err
	}
	idx := lastNonEmpty(out)
	if idx == "" || idx == "FAIL" {
		return fmt.Errorf("wpa_cli add_network: %q", out)
	}
	steps := [][]string{
		{"set_network", idx, "ssid", fmt.Sprintf("%q", ssid)},
		{"set_network", idx, "key_mgmt", "WPA-PSK"},
		{"set_network", idx, "psk", fmt.Sprintf("%q", passphrase)},
		// select_network forces the association; this wpa_supplicant has no
		// "autostart" network parameter (it answers FAIL), and the block is
		// removed again by ForgetHeadUnitAPs when the session ends, so nothing
		// rejoins this AP on its own.
		{"enable_network", idx},
		{"select_network", idx},
	}
	for _, st := range steps {
		if _, err := cli(st...); err != nil {
			return err
		}
	}
	return nil
}

// lastNonEmpty returns the last non-blank line of s, trimmed.
func lastNonEmpty(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if l := strings.TrimSpace(lines[i]); l != "" {
			return l
		}
	}
	return ""
}

// udhcpcScript applies (and removes) a busybox udhcpc lease. Kept inline so the
// helper carries no external file; iproute2 accepts a dotted netmask as well as
// a prefix length, so busybox's $mask works either way.
const udhcpcScript = `#!/bin/sh
case "$1" in
deconfig)
	ip addr flush dev "$interface" 2>/dev/null
	;;
bound|renew)
	ip addr flush dev "$interface" 2>/dev/null
	ip addr add "$ip/$mask" dev "$interface" 2>/dev/null
	[ -n "$router" ] && ip route replace default via "$router" dev "$interface"
	;;
esac
`

// dhcpLease runs busybox udhcpc once and fails if no IPv4 address was applied.
func dhcpLease(iface string, logf func(string, ...any)) error {
	const script = "/run/cargadgetd-udhcpc.sh"
	if err := os.WriteFile(script, []byte(udhcpcScript), 0o755); err != nil {
		return fmt.Errorf("udhcpc: script: %w", err)
	}
	out, err := exec.Command("/bin/busybox", "udhcpc",
		"-i", iface, "-n", "-q", "-t", "6", "-T", "2", "-s", script).CombinedOutput()
	msg := strings.TrimSpace(string(out))
	if ip := wifiIPv4(iface); ip != "" {
		logf("udhcpc: %s has %s", iface, ip)
		return nil
	}
	if err != nil {
		return fmt.Errorf("udhcpc: no lease on %s (%v: %s)", iface, err, msg)
	}
	return fmt.Errorf("udhcpc: no lease on %s", iface)
}
