package wireless

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/godbus/dbus/v5"
)

// LocalNames returns the names the head unit uses for this phone:
//
//   - the Bluetooth adapter alias ("Manolla"), which is what the car lists for
//     Wireless Android Auto;
//   - the USB gadget product string ("Jolla Phone 2026"), which is what the car
//     shows when the phone is plugged in.
//
// The two differ because they come from two different subsystems (BlueZ's
// adapter alias and the phone's configfs gadget strings). Both name the same
// phone, and the app says so instead of leaving the user to guess.
func LocalNames() (bt, address, usb string) {
	if conn, err := dbus.SystemBus(); err == nil {
		var managed map[dbus.ObjectPath]map[string]map[string]dbus.Variant
		if err := conn.Object(bluezService, "/").
			Call("org.freedesktop.DBus.ObjectManager.GetManagedObjects", 0).
			Store(&managed); err == nil {
			for _, ifaces := range managed {
				props, ok := ifaces["org.bluez.Adapter1"]
				if !ok {
					continue
				}
				address = variantString(props["Address"])
				bt = variantString(props["Alias"])
				if bt == "" {
					bt = variantString(props["Name"])
				}
				if bt != "" || address != "" {
					break
				}
			}
		}
	}
	return bt, address, usbProduct()
}

// usbProduct returns the name the head unit shows on the USB cable. Live
// configfs strings win; at rest the gadget has no strings, so fall back to the
// host name usb-moded uses to build them (/etc/machine-info).
func usbProduct() string {
	paths, _ := filepath.Glob("/sys/kernel/config/usb_gadget/*/strings/0x409/product")
	sort.Strings(paths)
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		if s := strings.TrimSpace(string(b)); s != "" {
			return s
		}
	}
	if b, err := os.ReadFile("/etc/machine-info"); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			if v, ok := strings.CutPrefix(strings.TrimSpace(line), "PRETTY_HOSTNAME="); ok {
				return strings.TrimSpace(strings.Trim(v, `"`))
			}
		}
	}
	if h, err := os.Hostname(); err == nil {
		return h
	}
	return ""
}
