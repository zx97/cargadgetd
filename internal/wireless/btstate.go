package wireless

import (
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/cargadgetd/cargadgetd/internal/cfg"
	"github.com/godbus/dbus/v5"
)

const bluezAdapterIface = "org.bluez.Adapter1"

// BTSnapshot is the Bluetooth state as BlueZ reports it right now.
//
// The status page used to show only what the last attempt had discovered, so a
// phone whose adapter had been switched off still displayed "Bluetooth link ✓"
// until something tried again. This is read fresh on every page request and
// carries the adapter's own Powered flag, which is the fact that was missing:
// with the adapter off, nothing else on the page means anything.
type BTSnapshot struct {
	Adapter      string // /org/bluez/hci1, or "" when BlueZ has none
	Present      bool
	Powered      bool
	Discoverable bool
	Devices      []*Device
	Err          string
}

// ReadBT reads the adapter and its devices from BlueZ. It never fails: a bus
// that does not answer comes back as Err with Present=false, which the page
// shows as "Bluetooth unavailable" instead of a green light.
func ReadBT() BTSnapshot {
	conn, err := dbus.SystemBus()
	if err != nil {
		return BTSnapshot{Err: err.Error()}
	}
	managed, err := managedObjects(conn)
	if err != nil {
		return BTSnapshot{Err: err.Error()}
	}
	snap := BTSnapshot{}
	for path, ifaces := range managed {
		props, ok := ifaces[bluezAdapterIface]
		if !ok {
			continue
		}
		// Prefer hci1: this phone exposes a second, silent controller as hci0.
		if snap.Adapter == "" || strings.Contains(string(path), "hci1") {
			snap.Adapter = string(path)
			snap.Present = true
			snap.Powered = variantBool(props["Powered"])
			snap.Discoverable = variantBool(props["Discoverable"])
		}
	}
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
		d.Connected = variantBool(props["Connected"])
		if v, ok := props["Class"]; ok {
			if c, ok := v.Value().(uint32); ok {
				d.Class = c
			}
		}
		if v, ok := props["Adapter"]; ok {
			d.Adapter, _ = v.Value().(dbus.ObjectPath)
		}
		// Adapter property is absent on some BlueZ builds: keep the device when
		// we cannot tell which adapter it belongs to.
		if snap.Adapter != "" && d.Adapter != "" && string(d.Adapter) != snap.Adapter {
			continue
		}
		snap.Devices = append(snap.Devices, d)
	}
	orderCandidates(snap.Devices)
	return snap
}

// PowerBT turns the adapter on through the setuid helper (polkit refuses a
// plain Properties.Set from this session: "AuthFailed"), then re-reads the
// state. It is what the "debug Bluetooth" button runs before dumping the tree.
func PowerBT(logf func(string, ...any)) (BTSnapshot, error) {
	snap := ReadBT()
	if !snap.Present {
		return snap, fmt.Errorf("no Bluetooth adapter reported by BlueZ")
	}
	if snap.Powered {
		return snap, nil
	}
	bin := cfg.Get().Gadget.Bin
	if bin == "" {
		return snap, fmt.Errorf("no helper configured (gadget.bin)")
	}
	out, err := exec.Command(bin, "-bt-adapter", snap.Adapter).CombinedOutput()
	msg := strings.TrimSpace(string(out))
	if err != nil {
		return ReadBT(), fmt.Errorf("power adapter: %v (%s)", err, lastNonEmpty(msg))
	}
	if logf != nil {
		logf("bluetooth: adapter powered through the helper (%s)", lastNonEmpty(msg))
	}
	return ReadBT(), nil
}

// DeviceLabel is the one-line description the page shows for a device.
func DeviceLabel(lang string, d *Device) string {
	name := d.Name
	if name == "" {
		name = d.Address
	}
	kind := ""
	switch {
	case d.LooksLikeHeadUnit():
		kind = " — " + T(lang, "car (audio/video)")
	case d.IsPhoneLike():
		kind = " — " + T(lang, "phone")
	}
	state := T(lang, "not connected")
	if d.Connected {
		state = T(lang, "connected")
	}
	return fmt.Sprintf("%s (%s)%s, %s", name, d.Address, kind, state)
}

// IsPhoneLike reports a phone class (major 0x02): kept next to
// LooksLikeHeadUnit so the page can label it.
func (d *Device) IsPhoneLike() bool { return (d.Class>>8)&0x1f == 0x02 }

// btCache keeps one reading for a couple of seconds: the page polls, and every
// poll doing a D-Bus round-trip would hammer BlueZ while a car is connecting.
var btCache struct {
	sync.Mutex
	snap BTSnapshot
	at   time.Time
}

// ReadBTCached returns the adapter state, re-reading it when the last reading is
// older than maxAge. A zero or negative maxAge always re-reads.
func ReadBTCached(maxAge time.Duration) BTSnapshot {
	btCache.Lock()
	defer btCache.Unlock()
	if maxAge > 0 && time.Since(btCache.at) < maxAge {
		return btCache.snap
	}
	btCache.snap = ReadBT()
	btCache.at = time.Now()
	return btCache.snap
}
