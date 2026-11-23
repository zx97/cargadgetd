package wireless

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/cargadgetd/cargadgetd/internal/cfg"
	"github.com/godbus/dbus/v5"
)

const (
	connmanService = "net.connman"
	wpaService     = "fi.w1.wpa_supplicant1"
)

var wpaPath = dbus.ObjectPath("/fi/w1/wpa_supplicant1")

// connmanNetService is one of ConnMan's network services.
type connmanNetService struct {
	Path  dbus.ObjectPath
	Name  string
	State string
	Type  string
}

// connmanServices lists ConnMan services. Current ConnMan returns
// a(oa{sv}) from Manager.GetServices (path + properties), not a bare ao.
func connmanServices() ([]connmanNetService, error) {
	conn, err := dbus.SystemBus()
	if err != nil {
		return nil, fmt.Errorf("system bus: %w", err)
	}
	var entries []struct {
		Path  dbus.ObjectPath
		Props map[string]dbus.Variant
	}
	if err := conn.Object(connmanService, "/").
		Call("net.connman.Manager.GetServices", 0).Store(&entries); err != nil {
		return nil, fmt.Errorf("connman services: %w", err)
	}
	out := make([]connmanNetService, 0, len(entries))
	for _, e := range entries {
		out = append(out, connmanNetService{
			Path:  e.Path,
			Name:  variantString(e.Props["Name"]),
			State: variantString(e.Props["State"]),
			Type:  variantString(e.Props["Type"]),
		})
	}
	return out, nil
}

// connmanWiFiServices returns the Wi-Fi services ConnMan knows about.
func connmanWiFiServices() ([]connmanNetService, error) {
	all, err := connmanServices()
	if err != nil {
		return nil, err
	}
	var out []connmanNetService
	for _, s := range all {
		if s.Type == "wifi" {
			out = append(out, s)
		}
	}
	return out, nil
}

// savedSSIDFor picks a saved Wi-Fi service whose SSID matches the Bluetooth
// device name (case-insensitive, or one containing the other). It only returns
// networks already configured on the phone, so it never joins an unknown AP.
func savedSSIDFor(name string) string {
	services, err := connmanWiFiServices()
	if err != nil {
		return ""
	}
	n := strings.ToUpper(strings.TrimSpace(name))
	if n == "" {
		return ""
	}
	for _, s := range services {
		if strings.EqualFold(s.Name, name) {
			return s.Name
		}
	}
	for _, s := range services {
		up := strings.ToUpper(s.Name)
		if strings.Contains(up, n) || strings.Contains(n, up) {
			return s.Name
		}
	}
	return ""
}

// connmanConnect asks ConnMan to connect the saved Wi-Fi service whose SSID is
// ssid. ConnMan denies Service.Connect to a plain user session, so the setuid
// gadget helper is tried first (it runs as root). When the helper ran, its
// answer is authoritative: the direct call would only add a misleading
// "Permission denied" to a perfectly good "No carrier" diagnosis.
func connmanConnect(ssid string) error {
	bin := cfg.Get().Gadget.Bin
	if bin != "" {
		out, err := exec.Command(bin, "-wifi-connect", ssid).CombinedOutput()
		msg := strings.TrimSpace(string(out))
		if err == nil {
			return nil
		}
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) || strings.Contains(msg, "needs root") {
			// The helper did not run as root: fall back to the direct call,
			// which fails cleanly and is translated for the page.
			return friendlyWiFiError(ssid, connmanConnectDirect(ssid))
		}
		return friendlyWiFiError(ssid, connmanErrorFromHelper(msg))
	}
	return friendlyWiFiError(ssid, connmanConnectDirect(ssid))
}

// connmanJoin connects ssid, provisioning it when psk is known. The passphrase
// travels in the environment of the setuid helper: its command line is public,
// its environment is root-only.
func connmanJoin(ssid, psk string) error {
	if psk == "" {
		return connmanConnect(ssid)
	}
	bin := cfg.Get().Gadget.Bin
	if bin == "" {
		return connmanConnect(ssid)
	}
	cmd := exec.Command(bin, "-wifi-join", ssid)
	cmd.Env = append(os.Environ(), "CARGADGETD_WIFI_PSK="+psk)
	out, err := cmd.CombinedOutput()
	msg := strings.TrimSpace(string(out))
	if err == nil {
		return nil
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || strings.Contains(msg, "needs root") {
		return connmanConnect(ssid)
	}
	return friendlyWiFiError(ssid, connmanErrorFromHelper(msg))
}

// connmanErrorFromHelper drops the log timestamp/prefix added by the helper.
func connmanErrorFromHelper(out string) error {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	last := strings.TrimSpace(lines[len(lines)-1])
	if i := strings.Index(last, "connman connect "); i >= 0 {
		last = last[i:]
	}
	return errors.New(last)
}

// friendlyWiFiError turns ConnMan's wording into something a driver can act on.
func friendlyWiFiError(ssid string, err error) error {
	if err == nil {
		return nil
	}
	s := err.Error()
	switch {
	case strings.Contains(s, "Already connected"), strings.Contains(s, "In progress"):
		return nil
	case strings.Contains(s, "No carrier"), strings.Contains(s, "no carrier"):
		return fmt.Errorf("network « %s » out of range: turn the car ignition on", ssid)
	case strings.Contains(s, "Permission denied"), strings.Contains(s, "AuthFailed"):
		return fmt.Errorf("ConnMan denied access: setuid helper missing (reinstall the package)")
	}
	return err
}

// ConnectWiFi is the direct ConnMan connect used by the setuid helper (root).
func ConnectWiFi(ssid string) error {
	return connmanConnectDirect(ssid)
}

// JoinWiFi connects the head unit's access point, adding it to wpa_supplicant
// with the passphrase the WPP handshake delivered.
//
// ConnMan file provisioning does not exist on Sailfish: /var/lib/connman is
// absent (connmand runs without its wifi plugin) and an unsaved AP is refused
// with "no saved wifi service". wpa_supplicant is what really associates, it
// runs on the system bus, and the setuid helper reaches it as root.
func JoinWiFi(ssid, passphrase string) error {
	if strings.TrimSpace(ssid) == "" {
		return fmt.Errorf("connman: empty ssid")
	}
	// ConnMan does the association, DHCP, route and its own UI bookkeeping.
	if err := JoinWithAgent(ssid, passphrase, nil); err == nil {
		return nil
	} else if passphrase == "" {
		// An open network has no passphrase prompt to answer; the saved-service
		// path is enough there.
		return connmanConnectDirect(ssid)
	} else {
		// Fallback: associate through wpa_supplicant, which works but leaves
		// ConnMan unaware (no DHCP, stale UI), so nothing else should rely on it.
		if werr := wpaJoin(ssid, passphrase); werr != nil {
			return fmt.Errorf("connman: %v (wpa fallback: %v)", err, werr)
		}
		return nil
	}
}

// wpaJoin adds the network to the running wpa_supplicant and selects it, which
// starts the association immediately.
func wpaJoin(ssid, passphrase string) error {
	conn, err := dbus.SystemBus()
	if err != nil {
		return fmt.Errorf("wpa: system bus: %w", err)
	}
	root := conn.Object(wpaService, wpaPath)

	var ifacePath dbus.ObjectPath
	if err := root.Call("fi.w1.wpa_supplicant1.GetInterface", 0, "wlan0").Store(&ifacePath); err != nil {
		var v dbus.Variant
		if perr := root.Call("org.freedesktop.DBus.Properties.Get", 0,
			"fi.w1.wpa_supplicant1", "Interfaces").Store(&v); perr != nil {
			return fmt.Errorf("wpa: no interface (GetInterface: %v)", err)
		}
		ifaces, _ := v.Value().([]dbus.ObjectPath)
		if len(ifaces) == 0 {
			return fmt.Errorf("wpa: no interface at all")
		}
		ifacePath = ifaces[0]
	}

	// This head unit regenerates its SSID every session (carplay_wifi_34A5,
	// B2AB, DC02, 5059, 0D60 …). Left alone, wpa_supplicant keeps one network
	// per SSID and the phone's Wi-Fi list fills with dead carplay entries.
	// Drop the stale ones before adding today's.
	iface0 := conn.Object(wpaService, ifacePath)
	var vnets dbus.Variant
	if err := iface0.Call("org.freedesktop.DBus.Properties.Get", 0,
		"fi.w1.wpa_supplicant1.Interface", "Networks").Store(&vnets); err == nil {
		nets, _ := vnets.Value().([]dbus.ObjectPath)
		for _, n := range nets {
			var props map[string]dbus.Variant
			if err := conn.Object(wpaService, n).Call("org.freedesktop.DBus.Properties.GetAll", 0,
				"fi.w1.wpa_supplicant1.Network").Store(&props); err != nil {
				continue
			}
			b, _ := props["ssid"].Value().([]byte)
			name := string(b)
			if name == ssid {
				// Already configured: just select it.
				if err := iface0.Call("fi.w1.wpa_supplicant1.Interface.SelectNetwork", 0, n).Err; err == nil {
					return nil
				}
				break
			}
			if strings.HasPrefix(name, "carplay_wifi") {
				if err := iface0.Call("fi.w1.wpa_supplicant1.Interface.RemoveNetwork", 0, n).Err; err == nil {
					continue
				}
			}
		}
	}

	args := map[string]dbus.Variant{
		"ssid": dbus.MakeVariant([]byte(ssid)),
	}
	if passphrase == "" {
		args["key_mgmt"] = dbus.MakeVariant("NONE")
	} else {
		args["key_mgmt"] = dbus.MakeVariant("WPA-PSK")
		args["psk"] = dbus.MakeVariant(passphrase)
	}
	iface := conn.Object(wpaService, ifacePath)
	var netPath dbus.ObjectPath
	if err := iface.Call("fi.w1.wpa_supplicant1.Interface.AddNetwork", 0, args).Store(&netPath); err != nil {
		return fmt.Errorf("wpa: add network %q: %w", ssid, err)
	}
	if err := iface.Call("fi.w1.wpa_supplicant1.Interface.SelectNetwork", 0, netPath).Err; err != nil {
		return fmt.Errorf("wpa: select %q: %w", ssid, err)
	}
	return nil
}

// connmanScan asks ConnMan for a Wi-Fi scan, so a head unit's fresh AP shows up
// in its service list.
func connmanScan() {
	conn, err := dbus.SystemBus()
	if err != nil {
		return
	}
	var techs []struct {
		Path  dbus.ObjectPath
		Props map[string]dbus.Variant
	}
	if err := conn.Object(connmanService, "/").
		Call("net.connman.Manager.GetTechnologies", 0).Store(&techs); err != nil {
		return
	}
	for _, t := range techs {
		if variantString(t.Props["Type"]) == "wifi" {
			_ = conn.Object(connmanService, t.Path).Call("net.connman.Technology.Scan", 0).Err
		}
	}
}

func connmanConnectDirect(ssid string) error {
	services, err := connmanWiFiServices()
	if err != nil {
		return err
	}
	for _, s := range services {
		if !strings.EqualFold(s.Name, ssid) {
			continue
		}
		conn, err := dbus.SystemBus()
		if err != nil {
			return fmt.Errorf("system bus: %w", err)
		}
		if err := conn.Object(connmanService, s.Path).
			Call("net.connman.Service.Connect", 0).Err; err != nil {
			return fmt.Errorf("connman connect %q: %w", ssid, err)
		}
		return nil
	}
	return fmt.Errorf("connman: no saved wifi service %q", ssid)
}
