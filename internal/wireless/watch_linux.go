//go:build linux

package wireless

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/godbus/dbus/v5"
)

// watchState is the small set of values that tell whether the situation
// changed: the Bluetooth link, BlueZ service resolution, the ConnMan state of
// the car's service and the SSID the interface is associated with.
type watchState struct {
	Connected bool
	Services  bool
	WiFiState string
	SSID      string
}

func (s watchState) diff(o watchState, ssid string) string {
	switch {
	case s.Connected != o.Connected:
		return fmt.Sprintf("Bluetooth Connected=%v", s.Connected)
	case s.Services != o.Services:
		return fmt.Sprintf("Bluetooth ServicesResolved=%v", s.Services)
	case s.SSID != o.SSID:
		return fmt.Sprintf("Wi-Fi SSID %q", s.SSID)
	case s.WiFiState != o.WiFiState:
		return fmt.Sprintf("ConnMan state %s→%s", o.WiFiState, s.WiFiState)
	}
	return ""
}

// watchSnapshot reads the watched values. Every part degrades to its zero
// value: a failing read must not stop the watch loop.
func watchSnapshot(devPath dbus.ObjectPath, iface, ssid string) watchState {
	var st watchState
	if conn, err := dbus.SystemBus(); err == nil && devPath != "" {
		var props map[string]dbus.Variant
		if err := conn.Object(bluezService, devPath).
			Call("org.freedesktop.DBus.Properties.GetAll", 0, bluezDeviceIface).
			Store(&props); err == nil {
			st.Connected = variantBool(props["Connected"])
			st.Services = variantBool(props["ServicesResolved"])
		}
	}
	if services, err := connmanWiFiServices(); err == nil {
		for _, s := range services {
			if ssid != "" && strings.EqualFold(s.Name, ssid) {
				st.WiFiState = s.State
			}
		}
	}
	st.SSID = wifiSSID(iface)
	return st
}

// WaitForChange blocks until the head unit or its access point changes state,
// or until max elapses. It is what keeps the connection cycle alive: the daemon
// never gives up; it restarts as soon as something moves (car switched on, AP
// visible, link connected) and otherwise retries after max.
func WaitForChange(ctx context.Context, opt Options, max time.Duration, logf func(string, ...any)) string {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	dev, err := FindDevice(opt.Name, opt.Address)
	if err != nil {
		logf("watch: no paired device to watch (%v), retrying after %s", err, max)
		sleepCtx(ctx, max)
		return ""
	}
	ssid := opt.SSID
	if ssid == "" {
		ssid = savedSSIDFor(dev.Name)
	}
	prev := watchSnapshot(dev.Path, opt.Interface, ssid)
	logf("watch: watching %s and %q (state=%+v) for up to %s", dev.Address, ssid, prev, max)
	start := time.Now()
	for time.Since(start) < max {
		select {
		case <-ctx.Done():
			return ""
		case <-time.After(2 * time.Second):
		}
		cur := watchSnapshot(dev.Path, opt.Interface, ssid)
		if reason := cur.diff(prev, ssid); reason != "" {
			logf("watch: change detected (%s) after %s", reason, sinceText(start))
			return reason
		}
	}
	logf("watch: nothing changed in %s (state=%+v)", max, prev)
	return ""
}

func sleepCtx(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}
