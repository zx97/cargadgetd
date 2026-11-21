// Package wireless connects the MD to a head unit the way a phone does for
// Wireless Android Auto: Bluetooth RFCOMM for the WPP credential handshake,
// the head unit's Wi-Fi AP, then a TCP connection where the phone is still the
// TLS server.
package wireless

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/godbus/dbus/v5"
)

const (
	bluezService     = "org.bluez"
	bluezDeviceIface = "org.bluez.Device1"
	// aaUUIDText is the Android Auto service class UUID the head unit exposes.
	aaUUIDText = "4de17a00-52cb-11e6-bdf4-0800200c9a66"
)

// Device is a device known to BlueZ.
type Device struct {
	Path    dbus.ObjectPath
	Address string
	Name    string
	Paired  bool
	Trusted bool
	UUIDs   []string
}

// FindDevice returns the paired device matching address (exact) or name
// (case-insensitive substring). With both empty it returns the first paired
// device.
func FindDevice(name, address string) (*Device, error) {
	conn, err := dbus.SystemBus()
	if err != nil {
		return nil, fmt.Errorf("system bus: %w", err)
	}
	var managed map[dbus.ObjectPath]map[string]map[string]dbus.Variant
	obj := conn.Object(bluezService, "/")
	if err := obj.Call("org.freedesktop.DBus.ObjectManager.GetManagedObjects", 0).Store(&managed); err != nil {
		return nil, fmt.Errorf("bluez managed objects: %w", err)
	}
	var all []*Device
	for path, ifaces := range managed {
		props, ok := ifaces[bluezDeviceIface]
		if !ok {
			continue
		}
		d := &Device{Path: path}
		d.Address = variantString(props["Address"])
		d.Name = variantString(props["Name"])
		if d.Name == "" {
			d.Name = variantString(props["Alias"])
		}
		d.Paired = variantBool(props["Paired"])
		d.Trusted = variantBool(props["Trusted"])
		if v, ok := props["UUIDs"]; ok {
			if uuids, ok := v.Value().([]string); ok {
				d.UUIDs = uuids
			}
		}
		all = append(all, d)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Address < all[j].Address })

	for _, d := range all {
		if !d.Paired {
			continue
		}
		switch {
		case address != "":
			if strings.EqualFold(d.Address, address) {
				return d, nil
			}
		case name != "":
			if strings.Contains(strings.ToUpper(d.Name), strings.ToUpper(name)) {
				return d, nil
			}
		}
	}
	// No selector: take the paired device that advertises the AA UUID, so the
	// daemon does not depend on a hard-coded head unit name.
	if address == "" && name == "" {
		for _, d := range all {
			if d.Paired && hasUUID(d.UUIDs, aaUUIDText) {
				return d, nil
			}
		}
		for _, d := range all {
			if d.Paired {
				return d, nil
			}
		}
	}
	var seen []string
	for _, d := range all {
		seen = append(seen, fmt.Sprintf("%s (%s paired=%v)", d.Name, d.Address, d.Paired))
	}
	return nil, fmt.Errorf("no paired Bluetooth device matches name=%q address=%q; seen: %s",
		name, address, strings.Join(seen, ", "))
}

func variantString(v dbus.Variant) string {
	s, _ := v.Value().(string)
	return s
}

func variantBool(v dbus.Variant) bool {
	b, _ := v.Value().(bool)
	return b
}

func hasUUID(uuids []string, want string) bool {
	for _, u := range uuids {
		if strings.EqualFold(u, want) {
			return true
		}
	}
	return false
}

// Connect brings up the Bluetooth link and the head unit's profiles (HFP/A2DP)
// before the RFCOMM connect. The AA handshake needs the car's HFP AG link.
func (d *Device) Connect() error {
	conn, err := dbus.SystemBus()
	if err != nil {
		return fmt.Errorf("system bus: %w", err)
	}
	return conn.Object(bluezService, d.Path).Call("org.bluez.Device1.Connect", 0).Err
}

// WaitConnected polls Device1.Connected until the ACL link is up.
func (d *Device) WaitConnected(timeout time.Duration) bool {
	conn, err := dbus.SystemBus()
	if err != nil {
		return false
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		var v dbus.Variant
		err := conn.Object(bluezService, d.Path).
			Call("org.freedesktop.DBus.Properties.Get", 0, bluezDeviceIface, "Connected").
			Store(&v)
		if err == nil {
			if b, _ := v.Value().(bool); b {
				return true
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	return false
}
