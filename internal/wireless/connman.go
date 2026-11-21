package wireless

import (
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
// gadget helper is tried first (it runs as root); the direct call is the
// fallback.
func connmanConnect(ssid string) error {
	bin := cfg.Get().Gadget.Bin
	if bin != "" {
		out, err := exec.Command(bin, "-wifi-connect", ssid).CombinedOutput()
		if err == nil {
			return nil
		}
		helperErr := fmt.Errorf("wifi helper: %v (%s)", err, strings.TrimSpace(string(out)))
		if derr := connmanConnectDirect(ssid); derr != nil {
			return fmt.Errorf("%v; direct: %v", helperErr, derr)
		}
		return nil
	}
	return connmanConnectDirect(ssid)
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
