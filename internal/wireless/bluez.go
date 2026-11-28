// Package wireless connects the MD to a head unit the way a phone does for
// Wireless Android Auto: Bluetooth RFCOMM for the WPP credential handshake,
// the head unit's Wi-Fi AP, then a TCP connection where the phone is still the
// TLS server.
package wireless

import (
	"context"
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/cargadgetd/cargadgetd/internal/cfg"
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
	Path             dbus.ObjectPath
	Adapter          dbus.ObjectPath
	Address          string
	Name             string
	Paired           bool
	Bonded           bool
	Trusted          bool
	Connected        bool
	ServicesResolved bool
	UUIDs            []string
	// Class is the Bluetooth class of device. It is what separates a head unit
	// from another phone: the paired phones answer the AA RFCOMM probe too, and
	// one of them holding candidate 1 kept the real unit out of its window.
	Class uint32
}

// LooksLikeHeadUnit reports whether the class of device says audio/video —
// which is what a car kit is (0x200404 hands-free, 0x240404 car audio). A PC is
// major 0x01, a phone 0x02: neither is ever the car, and dialling them wastes
// the real unit's window.
func (d *Device) LooksLikeHeadUnit() bool {
	return (d.Class>>8)&0x1f == 0x04
}

// FindDevice returns the paired device matching address (exact) or name
// (case-insensitive substring). With both empty it returns the best candidate
// from FindDevices.
func FindDevice(name, address string) (*Device, error) {
	devs, err := FindDevices(name, address)
	if err != nil {
		return nil, err
	}
	return devs[0], nil
}

// managedObjects reads BlueZ's object tree with a bounded context. BlueZ can stop
// answering on the system bus, and an unbounded Call froze the whole daemon with
// no log line (SIGQUIT dump: the main goroutine sat in Device1.Connect for forty
// minutes without writing anything).
func managedObjects(conn *dbus.Conn) (map[dbus.ObjectPath]map[string]map[string]dbus.Variant, error) {
	var managed map[dbus.ObjectPath]map[string]map[string]dbus.Variant
	cctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err := conn.Object(bluezService, "/").
		CallWithContext(cctx, "org.freedesktop.DBus.ObjectManager.GetManagedObjects", 0).
		Store(&managed)
	if cctx.Err() == context.DeadlineExceeded {
		return nil, fmt.Errorf("bluez: GetManagedObjects did not answer within 10s")
	}
	return managed, err
}

// FindDevices returns the paired devices to try, best first. With a selector it
// returns only the match. Without one every paired device is a candidate, the
// one advertising the Android Auto UUID first: a head unit that is switched off
// must not stop the daemon from finding another one that is on.
func FindDevices(name, address string) ([]*Device, error) {
	conn, err := dbus.SystemBus()
	if err != nil {
		return nil, fmt.Errorf("system bus: %w", err)
	}
	managed, err := managedObjects(conn)
	if err != nil {
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
		d.Bonded = variantBool(props["Bonded"])
		d.Trusted = variantBool(props["Trusted"])
		d.Connected = variantBool(props["Connected"])
		if v, ok := props["Class"]; ok {
			if c, ok := v.Value().(uint32); ok {
				d.Class = c
			}
		}
		d.ServicesResolved = variantBool(props["ServicesResolved"])
		if v, ok := props["Adapter"]; ok {
			d.Adapter, _ = v.Value().(dbus.ObjectPath)
		}
		if v, ok := props["UUIDs"]; ok {
			if uuids, ok := v.Value().([]string); ok {
				d.UUIDs = uuids
			}
		}
		all = append(all, d)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Address < all[j].Address })

	var picked []*Device
	for _, d := range all {
		if !d.Paired {
			continue
		}
		switch {
		case address != "":
			if strings.EqualFold(d.Address, address) {
				picked = append(picked, d)
			}
		case name != "":
			if strings.Contains(strings.ToUpper(d.Name), strings.ToUpper(name)) {
				picked = append(picked, d)
			}
		default:
			picked = append(picked, d)
		}
	}
	if len(picked) == 0 {
		var seen []string
		for _, d := range all {
			seen = append(seen, fmt.Sprintf("%s (%s paired=%v)", d.Name, d.Address, d.Paired))
		}
		return nil, fmt.Errorf("no paired Bluetooth device matches name=%q address=%q; seen: %s",
			name, address, strings.Join(seen, ", "))
	}
	orderCandidates(picked)
	return picked, nil
}

// orderCandidates sorts paired devices the way dialling wants them: the one the
// phone is connected to first, then AA-UUID devices, then by address.
//
// Connected first is not a nicety: it is the only state that means "this head
// unit is here, now". A paired unit that is parked far away must never take
// precedence (measured 2026-11-26: candidate 1 was the MULTIVAN, connected=false,
// while the lab unit was connected=true at candidate 2, and the attempt went to
// the one that could not answer).
func orderCandidates(ds []*Device) {
	// absent is read once: a comparator may not mutate the shared map, and the
	// map is small (a handful of paired devices).
	absent := make(map[string]bool, len(ds))
	for _, d := range ds {
		absent[d.Address] = absentNow(d.Address)
	}
	sort.SliceStable(ds, func(i, j int) bool {
		// Only an audio/video device can be the car, whatever its connection
		// state: the user's PC and his own handset both answered the AA probe
		// and stole candidate 1 (measured 2026-11-26 and 27). The rule has to
		// dominate the "recently silent" flag too: a head unit that is switched
		// off still belongs in front of a PC that happens to be connected, and
		// applying the demotion afterwards put that PC first on the bench
		// (measured 2026-11-28).
		if hi, hj := ds[i].LooksLikeHeadUnit(), ds[j].LooksLikeHeadUnit(); hi != hj {
			return hi
		}
		if absent[ds[i].Address] != absent[ds[j].Address] {
			return !absent[ds[i].Address]
		}
		if ds[i].Connected != ds[j].Connected {
			return ds[i].Connected
		}
		ai := hasUUID(ds[i].UUIDs, aaUUIDText)
		aj := hasUUID(ds[j].UUIDs, aaUUIDText)
		if ai != aj {
			return ai
		}
		return ds[i].Address < ds[j].Address
	})
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
//
// The call is bounded. BlueZ accepts org.bluez.Device1.Connect and then may never
// answer - typically when the peer stops answering the page - and a bare Call
// blocks the caller for ever: a SIGQUIT dump on the bench showed the main
// goroutine stuck here for forty minutes without a single log line. A timeout
// turns that silent freeze into an ordinary failure the retry loop can act on.
func (d *Device) Connect(ctx context.Context) error {
	conn, err := dbus.SystemBus()
	if err != nil {
		return fmt.Errorf("system bus: %w", err)
	}
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	err = conn.Object(bluezService, d.Path).
		CallWithContext(cctx, "org.bluez.Device1.Connect", 0).Err
	if cctx.Err() == context.DeadlineExceeded {
		return fmt.Errorf("bluez: connect %s did not answer within 20s", d.Address)
	}
	return err
}

// AdapterSetup powers the adapter, disables the discoverable/pairable timeouts
// and turns both flags on. It must run as root: Sailfish answers
// org.freedesktop.DBus.Error.AuthFailed to Adapter1 property writes from a
// normal user session, so the daemon reaches this through the setuid helper.
func AdapterSetup(adapter dbus.ObjectPath) error {
	if adapter == "" {
		return fmt.Errorf("adapter path unknown")
	}
	conn, err := dbus.SystemBus()
	if err != nil {
		return fmt.Errorf("system bus: %w", err)
	}
	writes := []struct {
		prop string
		v    any
	}{
		{"Powered", true},
		// A zero timeout means "no timeout": otherwise BlueZ reverts
		// Discoverable after DiscoverableTimeout (900 s here, 180 s on
		// stock BlueZ) and the phone vanishes from the head unit's list.
		{"DiscoverableTimeout", uint32(0)},
		{"PairableTimeout", uint32(0)},
		{"Pairable", true},
		{"Discoverable", true},
	}
	for _, w := range writes {
		err := conn.Object(bluezService, adapter).
			Call("org.freedesktop.DBus.Properties.Set", 0,
				"org.bluez.Adapter1", w.prop, dbus.MakeVariant(w.v)).Err
		if err != nil {
			return fmt.Errorf("%s=%v: %s", w.prop, w.v, dbusErrText(err))
		}
	}
	return nil
}

// dbusErrText names the D-Bus error when its body is empty (Sailfish answers
// AuthFailed with no message), so the log line stays useful.
func dbusErrText(err error) string {
	if err == nil {
		return "<nil>"
	}
	derr, ok := err.(dbus.Error)
	if !ok {
		ptr, isPtr := err.(*dbus.Error)
		if !isPtr {
			return err.Error()
		}
		derr = *ptr
	}
	if len(derr.Body) == 0 || derr.Body[0] == "" {
		return derr.Name
	}
	return fmt.Sprintf("%s: %v", derr.Name, derr.Body[0])
}

// EnsureAdapter powers the Bluetooth adapter and makes it discoverable so the
// head unit can find the phone for the first wireless pairing. The D-Bus write
// needs root, so the setuid gadget helper is used when the direct call is
// refused.
func (d *Device) EnsureAdapter(logf func(string, ...any)) {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	if d.Adapter == "" {
		logf("bluetooth: adapter path unknown")
		return
	}
	if err := AdapterSetup(d.Adapter); err != nil {
		logf("bluetooth: adapter direct: %v", err)
		if bin := cfg.Get().Gadget.Bin; bin != "" {
			out, herr := exec.Command(bin, "-bt-adapter", string(d.Adapter)).CombinedOutput()
			if herr != nil {
				logf("bluetooth: adapter helper %s: %v (%s)", bin, herr, strings.TrimSpace(string(out)))
			} else {
				logf("bluetooth: adapter configured by the root helper")
			}
		} else {
			logf("bluetooth: no gadget binary configured for the root helper")
		}
	} else {
		logf("bluetooth: adapter configured")
	}

	conn, err := dbus.SystemBus()
	if err != nil {
		logf("bluetooth: adapter: %v", err)
		return
	}
	var props map[string]dbus.Variant
	if err := conn.Object(bluezService, d.Adapter).
		Call("org.freedesktop.DBus.Properties.GetAll", 0, "org.bluez.Adapter1").
		Store(&props); err == nil {
		logf("bluetooth: adapter address=%s powered=%v discoverable=%v pairable=%v discovering=%v",
			variantString(props["Address"]),
			variantBool(props["Powered"]),
			variantBool(props["Discoverable"]),
			variantBool(props["Pairable"]),
			variantBool(props["Discovering"]))
	}
}

// WaitConnected polls Device1.Connected until the ACL link is up, logging each
// state change so the log shows how long the link took (or that it never came).
func (d *Device) WaitConnected(timeout time.Duration, logf func(string, ...any)) bool {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	conn, err := dbus.SystemBus()
	if err != nil {
		logf("bluetooth: wait link: system bus: %v", err)
		return false
	}
	start := time.Now()
	deadline := start.Add(timeout)
	prev := "unknown"
	polls := 0
	for time.Now().Before(deadline) {
		var v dbus.Variant
		err := conn.Object(bluezService, d.Path).
			Call("org.freedesktop.DBus.Properties.Get", 0, bluezDeviceIface, "Connected").
			Store(&v)
		var state string
		if err == nil {
			if b, _ := v.Value().(bool); b {
				logf("bluetooth: link up after %s (%d polls)", sinceText(start), polls)
				return true
			}
			state = "down"
		} else {
			state = "error:" + dbusErrText(err)
		}
		if state != prev {
			logf("bluetooth: link %s after %s (%d polls)", state, sinceText(start), polls)
			prev = state
		}
		polls++
		time.Sleep(500 * time.Millisecond)
	}
	logf("bluetooth: link never came up within %s (%d polls, last=%s)", timeout, polls, prev)
	return false
}
