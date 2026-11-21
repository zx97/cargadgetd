package wireless

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"github.com/cargadgetd/cargadgetd/internal/cfg"
	"github.com/godbus/dbus/v5"
)

const connmanService = "net.connman"

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
		return fmt.Errorf("réseau « %s » hors de portée : allume le contact de la voiture", ssid)
	case strings.Contains(s, "Permission denied"), strings.Contains(s, "AuthFailed"):
		return fmt.Errorf("ConnMan refuse l'accès : helper setuid absent (réinstalle le paquet)")
	}
	return err
}

// ConnectWiFi is the direct ConnMan connect used by the setuid helper (root).
func ConnectWiFi(ssid string) error {
	return connmanConnectDirect(ssid)
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
