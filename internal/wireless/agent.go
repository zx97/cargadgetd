package wireless

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"
)

// Pairing with a fixed PIN
//
// Some head units use a legacy Bluetooth PIN instead of the Secure Simple
// Pairing dialog. They display a four-digit code (0000 on the cheap universal
// units) and expect the phone to send it. Sailfish's own agent only knows the
// six-digit numeric comparison, so the bond ends up half-done: BlueZ records a
// link key, the head unit refuses it, and every later page times out
// ("br-connection-page-timeout") with no visible reason.
//
// This agent answers the PIN request with the code the head unit displays, then
// leaves the bond in place and marks the device trusted.

// pinAgent implements org.bluez.Agent1. Every entry point that could block the
// pairing answers with the configured PIN or a plain confirmation.
type pinAgent struct {
	pin      string
	logf     func(string, ...any)
	mu       sync.Mutex
	requests []string
}

func (a *pinAgent) note(kind, arg string) {
	a.mu.Lock()
	a.requests = append(a.requests, kind)
	a.mu.Unlock()
	if a.logf != nil {
		if arg == "" {
			a.logf("agent: %s", kind)
		} else {
			a.logf("agent: %s(%s) → %s", kind, arg, a.pin)
		}
	}
}

// Requests returns the callbacks the head unit exercised, for the log.
func (a *pinAgent) Requests() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.requests...)
}

// RequestPinCode is the legacy PIN path: the agent supplies the code the head
// unit is showing.
func (a *pinAgent) RequestPinCode(device dbus.ObjectPath) (string, *dbus.Error) {
	a.note("RequestPinCode", string(device))
	return a.pin, nil
}

func (a *pinAgent) DisplayPinCode(device dbus.ObjectPath, pincode string) *dbus.Error {
	// The head unit shows a code; ours is the same one, nothing to do.
	a.note("DisplayPinCode", pincode)
	return nil
}

// RequestPasskey is the SSP path (six digits); the universal units never use
// it, but BlueZ may still ask.
func (a *pinAgent) RequestPasskey(device dbus.ObjectPath) (uint32, *dbus.Error) {
	a.note("RequestPasskey", string(device))
	var v uint32
	for _, r := range a.pin {
		if r < '0' || r > '9' {
			continue
		}
		v = v*10 + uint32(r-'0')
	}
	return v, nil
}

func (a *pinAgent) DisplayPasskey(device dbus.ObjectPath, passkey uint32, entered uint16) *dbus.Error {
	a.note("DisplayPasskey", fmt.Sprintf("%06d", passkey))
	return nil
}

func (a *pinAgent) RequestConfirmation(device dbus.ObjectPath, passkey uint32) *dbus.Error {
	a.note("RequestConfirmation", fmt.Sprintf("%06d", passkey))
	return nil // accept
}

func (a *pinAgent) RequestAuthorization(device dbus.ObjectPath) *dbus.Error {
	a.note("RequestAuthorization", string(device))
	return nil
}

func (a *pinAgent) AuthorizeService(device dbus.ObjectPath, uuid string) *dbus.Error {
	a.note("AuthorizeService", uuid)
	return nil
}

func (a *pinAgent) Cancel() *dbus.Error {
	a.note("Cancel", "")
	return nil
}

func (a *pinAgent) Release() *dbus.Error { return nil }

const (
	agentPath = dbus.ObjectPath("/cargadgetd/agent")
	// PairTimeout bounds Device1.Pair: the head unit answers the page, then
	// either completes or gives up well inside this window.
	PairTimeout = 40 * time.Second
)

// PairDevice pairs with a head unit, answering the PIN it displays. The
// selector is a Bluetooth address or a device name (a head unit with a random
// address changes it at every boot). A bond the head unit no longer accepts is
// dropped and redone once: that stale state is what makes every later page time
// out.
func PairDevice(selector, pin string, logf func(string, ...any)) error {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	if selector == "" {
		return fmt.Errorf("pairing needs a Bluetooth address or name")
	}
	if pin == "" {
		pin = "0000"
	}
	conn, err := dbus.SystemBus()
	if err != nil {
		return fmt.Errorf("system bus: %w", err)
	}

	agent := &pinAgent{pin: pin, logf: logf}
	if err := conn.Export(agent, agentPath, "org.bluez.Agent1"); err != nil {
		return fmt.Errorf("export agent: %w", err)
	}
	defer conn.Export(nil, agentPath, "org.bluez.Agent1")
	mgr := conn.Object(bluezService, "/org/bluez")
	if err := mgr.Call("org.bluez.AgentManager1.RegisterAgent", 0, agentPath, "KeyboardDisplay").Err; err != nil {
		return fmt.Errorf("register agent: %w", err)
	}
	defer mgr.Call("org.bluez.AgentManager1.UnregisterAgent", 0, agentPath)
	if err := mgr.Call("org.bluez.AgentManager1.RequestDefaultAgent", 0, agentPath).Err; err != nil {
		// Not fatal: another default agent may refuse to yield, but ours is
		// still registered and receives the PIN request when it is needed.
		logf("agent: default agent not taken over: %v", err)
	}

	// BlueZ only pairs with an object it has seen. A device that is bonded (or
	// whose address is known) can be created directly — Adapter1.CreateDevice
	// works without an inquiry, which matters for head units that are
	// connectable but not discoverable.
	if _, err := devicePath(conn, selector); err != nil {
		created := false
		if isAddress(selector) {
			if aerr := createDevice(conn, selector, logf); aerr == nil {
				created = true
			}
		}
		if !created {
			if derr := discover(conn, selector, 25*time.Second, logf); derr != nil {
				return derr
			}
		}
	}
	if path, err := devicePath(conn, selector); err == nil {
		logf("pairing: target %s is %s", selector, path)
	}

	logf("pairing: %s with PIN %s", selector, pin)
	paired, err := pairOnce(conn, selector, logf)
	if err != nil || !paired {
		if err != nil {
			logf("pairing: first attempt: %v", err)
		}
		// The bond may be the half-done one the head unit refuses. Start over.
		if path, perr := devicePath(conn, selector); perr == nil {
			adapter := parentPath(path)
			if rerr := conn.Object(bluezService, adapter).Call("org.bluez.Adapter1.RemoveDevice", 0, path).Err; rerr == nil {
				logf("pairing: stale bond removed, pairing again")
				if derr := discover(conn, selector, 25*time.Second, logf); derr != nil {
					return derr
				}
				paired, err = pairOnce(conn, selector, logf)
			}
		}
	}
	if !paired {
		if err != nil {
			return fmt.Errorf("pair: %w", err)
		}
		return fmt.Errorf("pair: device is not bonded after the attempt")
	}

	path, _ := devicePath(conn, selector)
	if path != "" && !trusted(conn, path) {
		if e := conn.Object(bluezService, path).SetProperty("org.bluez.Device1.Trusted", dbus.MakeVariant(true)); e != nil {
			logf("pairing: could not mark trusted: %v", e)
		} else {
			logf("pairing: marked trusted")
		}
	}
	logf("pairing: bonded, agent calls: %s", strings.Join(agent.Requests(), ", "))
	return nil
}

// pairOnce calls Device1.Pair and reports the resulting bond state.
func pairOnce(conn *dbus.Conn, selector string, logf func(string, ...any)) (bool, error) {
	path, err := devicePath(conn, selector)
	if err != nil {
		return false, err
	}
	done := make(chan error, 1)
	go func() {
		done <- conn.Object(bluezService, path).Call("org.bluez.Device1.Pair", 0).Err
	}()
	var perr error
	select {
	case perr = <-done:
	case <-time.After(PairTimeout):
		perr = fmt.Errorf("no answer within %s", PairTimeout)
	}
	p, _ := deviceFlags(conn, path)
	if perr != nil {
		return p, perr
	}
	return p, nil
}

// discover runs a Bluetooth inquiry until address shows up in BlueZ, then stops
// it. Without this the object of a device that is not in the cache cannot be
// paired at all.
func discover(conn *dbus.Conn, selector string, wait time.Duration, logf func(string, ...any)) error {
	adapter, err := adapterPath(conn)
	if err != nil {
		return err
	}
	if err := conn.Object(bluezService, adapter).Call("org.bluez.Adapter1.StartDiscovery", 0).Err; err != nil {
		logf("pairing: discovery not started: %v", err)
	} else {
		logf("pairing: looking for %s", selector)
		defer conn.Object(bluezService, adapter).Call("org.bluez.Adapter1.StopDiscovery", 0)
	}
	deadline := time.Now().Add(wait)
	for time.Now().Before(deadline) {
		if _, err := devicePath(conn, selector); err == nil {
			logf("pairing: %s in range", selector)
			return nil
		}
		time.Sleep(time.Second)
	}
	return fmt.Errorf("%s did not show up within %s (head unit Bluetooth off or not discoverable?)", selector, wait)
}

// createDevice asks BlueZ for a device object by address, without an inquiry.
// BlueZ warns it is deprecated for unknown devices but it still creates the
// object, which is enough to page and pair a head unit that hides its
// discoverable flag.
func createDevice(conn *dbus.Conn, address string, logf func(string, ...any)) error {
	adapter, err := adapterPath(conn)
	if err != nil {
		return err
	}
	var path dbus.ObjectPath
	if err := conn.Object(bluezService, adapter).Call("org.bluez.Adapter1.CreateDevice", 0, address).Store(&path); err != nil {
		logf("pairing: create %s: %v", address, err)
		return err
	}
	logf("pairing: device object created for %s (%s)", address, path)
	return nil
}

// adapterPath returns the first BlueZ adapter path.
func adapterPath(conn *dbus.Conn) (dbus.ObjectPath, error) {
	managed, err := managedObjects(conn)
	if err != nil {
		return "", fmt.Errorf("bluez managed objects: %w", err)
	}
	for path, ifaces := range managed {
		if _, ok := ifaces["org.bluez.Adapter1"]; ok {
			return path, nil
		}
	}
	return "", fmt.Errorf("no BlueZ adapter")
}

// trusted reads the Trusted property.
func trusted(conn *dbus.Conn, path dbus.ObjectPath) bool {
	_, t := deviceFlags(conn, path)
	return t
}

// UnpairDevice forgets a bond, so the next pairing starts from scratch.
func UnpairDevice(address string, logf func(string, ...any)) error {
	conn, err := dbus.SystemBus()
	if err != nil {
		return fmt.Errorf("system bus: %w", err)
	}
	path, err := devicePath(conn, address)
	if err != nil {
		return err
	}
	if err := conn.Object(bluezService, parentPath(path)).Call("org.bluez.Adapter1.RemoveDevice", 0, path).Err; err != nil {
		return fmt.Errorf("remove device: %w", err)
	}
	if logf != nil {
		logf("bond for %s removed", address)
	}
	return nil
}

// devicePath finds the BlueZ object path of a device by address, or by name
// when the selector is not a MAC. Head units with a random address change it
// at every boot, so the name is often the only stable handle.
func devicePath(conn *dbus.Conn, selector string) (dbus.ObjectPath, error) {
	managed, err := managedObjects(conn)
	if err != nil {
		return "", fmt.Errorf("bluez managed objects: %w", err)
	}
	wantAddr := isAddress(selector)
	for path, ifaces := range managed {
		props, ok := ifaces[bluezDeviceIface]
		if !ok {
			continue
		}
		if wantAddr {
			if strings.EqualFold(variantString(props["Address"]), selector) {
				return path, nil
			}
			continue
		}
		name := variantString(props["Name"])
		if name == "" {
			name = variantString(props["Alias"])
		}
		if strings.Contains(strings.ToUpper(name), strings.ToUpper(selector)) {
			return path, nil
		}
	}
	if wantAddr {
		return "", fmt.Errorf("no BlueZ device with address %s", selector)
	}
	return "", fmt.Errorf("no BlueZ device whose name contains %q", selector)
}

// isAddress reports whether s looks like a Bluetooth address (aa:bb:cc:dd:ee:ff).
func isAddress(s string) bool {
	return strings.Count(s, ":") == 5 && len(s) == 17
}

// parentPath returns the adapter path of a device path
// (/org/bluez/hci1/dev_xx… → /org/bluez/hci1).
func parentPath(p dbus.ObjectPath) dbus.ObjectPath {
	s := string(p)
	i := strings.LastIndex(s, "/")
	if i <= 0 {
		return p
	}
	return dbus.ObjectPath(s[:i])
}

// deviceFlags reads the Paired and Trusted properties.
func deviceFlags(conn *dbus.Conn, path dbus.ObjectPath) (paired, trusted bool) {
	var v dbus.Variant
	obj := conn.Object(bluezService, path)
	if err := obj.Call("org.freedesktop.DBus.Properties.Get", 0, bluezDeviceIface, "Paired").Store(&v); err == nil {
		paired, _ = v.Value().(bool)
	}
	if err := obj.Call("org.freedesktop.DBus.Properties.Get", 0, bluezDeviceIface, "Trusted").Store(&v); err == nil {
		trusted, _ = v.Value().(bool)
	}
	return paired, trusted
}
