package wireless

import (
	"fmt"
	"strings"

	"github.com/godbus/dbus/v5"
)

const (
	connmanService = "net.connman"
)

// connmanNetService is one of ConnMan's network services.
type connmanNetService struct {
	Path  dbus.ObjectPath
	Name  string
	State string
}

// connmanWiFiServices returns the Wi-Fi services ConnMan knows about.
func connmanWiFiServices() ([]connmanNetService, error) {
	conn, err := dbus.SystemBus()
	if err != nil {
		return nil, fmt.Errorf("system bus: %w", err)
	}
	var paths []dbus.ObjectPath
	if err := conn.Object(connmanService, "/").
		Call("net.connman.Manager.GetServices", 0).Store(&paths); err != nil {
		return nil, fmt.Errorf("connman services: %w", err)
	}
	var out []connmanNetService
	for _, path := range paths {
		var props map[string]dbus.Variant
		if err := conn.Object(connmanService, path).
			Call("org.freedesktop.DBus.Properties.GetAll", 0, "net.connman.Service").
			Store(&props); err != nil {
			continue
		}
		if variantString(props["Type"]) != "wifi" {
			continue
		}
		out = append(out, connmanNetService{
			Path:  path,
			Name:  variantString(props["Name"]),
			State: variantString(props["State"]),
		})
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
// ssid. The phone normally keeps the car's AP as a saved service, so this is
// only needed when another network is currently preferred.
func connmanConnect(ssid string) error {
	conn, err := dbus.SystemBus()
	if err != nil {
		return fmt.Errorf("system bus: %w", err)
	}
	var services []dbus.ObjectPath
	obj := conn.Object(connmanService, "/")
	if err := obj.Call("net.connman.Manager.GetServices", 0).Store(&services); err != nil {
		return fmt.Errorf("connman services: %w", err)
	}
	for _, path := range services {
		var props map[string]dbus.Variant
		if err := conn.Object(connmanService, path).
			Call("org.freedesktop.DBus.Properties.GetAll", 0, "net.connman.Service").
			Store(&props); err != nil {
			continue
		}
		name := variantString(props["Name"])
		typ := variantString(props["Type"])
		if typ != "wifi" || !strings.EqualFold(name, ssid) {
			continue
		}
		if err := conn.Object(connmanService, path).
			Call("net.connman.Service.Connect", 0).Err; err != nil {
			return fmt.Errorf("connman connect %q: %w", ssid, err)
		}
		return nil
	}
	return fmt.Errorf("connman: no saved wifi service %q", ssid)
}
