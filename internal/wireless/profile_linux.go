//go:build linux

package wireless

import (
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/godbus/dbus/v5"
	"golang.org/x/sys/unix"
)

// aaProfile is the org.bluez.Profile1 object. The car discovers the AA UUID
// over SDP and connects its RFCOMM channel to the phone; NewConnection hands
// us the resulting stream.
type aaProfile struct {
	conns chan *os.File
	logf  func(string, ...any)
}

func (p *aaProfile) log(format string, args ...any) {
	if p.logf != nil {
		p.logf(format, args...)
	}
}

func (p *aaProfile) NewConnection(device dbus.ObjectPath, fd dbus.UnixFD, props map[string]dbus.Variant) *dbus.Error {
	p.log("profile: NewConnection from %s fd=%d props=%v", device, int(fd), props)
	dup, err := unix.Dup(int(fd))
	if err != nil {
		p.log("profile: dup fd failed: %v", err)
		return dbus.MakeFailedError(err)
	}
	f := os.NewFile(uintptr(dup), "rfcomm-in")
	if f == nil {
		p.log("profile: os.NewFile failed for fd %d", dup)
		unix.Close(dup)
		return dbus.MakeFailedError(fmt.Errorf("os.NewFile"))
	}
	select {
	case p.conns <- f:
		p.log("profile: handed dup fd=%d to the handshake", dup)
	default:
		p.log("profile: channel busy, closing fd=%d", dup)
		_ = f.Close()
	}
	return nil
}

func (p *aaProfile) RequestDisconnection(device dbus.ObjectPath) *dbus.Error {
	p.log("profile: RequestDisconnection for %s", device)
	return nil
}

func (p *aaProfile) Release() *dbus.Error {
	p.log("profile: Release (BlueZ unregistered the profile)")
	return nil
}

var aaServer struct {
	mu         sync.Mutex
	conns      chan *os.File
	exported   bool
	registered bool
}

// ensureAAProfile registers the Android Auto RFCOMM server profile, so the head
// unit sees the AA UUID in the phone's SDP record and offers the Android Auto
// entry.
//
// It is idempotent per process *and* per BlueZ lifetime: the registration is
// requested on every Dial attempt and "AlreadyExists" is accepted, because a
// bluetoothd restart on this platform (Sailfish feeds BlueZ through hci_vhci
// and an AppSupport bridge) drops the registration while our process keeps
// running. The old sync.Once silently lost the profile in that case.
func ensureAAProfile(logf func(string, ...any)) (<-chan *os.File, error) {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	aaServer.mu.Lock()
	defer aaServer.mu.Unlock()
	if aaServer.conns == nil {
		aaServer.conns = make(chan *os.File, 1)
	}
	conn, err := dbus.SystemBus()
	if err != nil {
		return nil, err
	}
	path := dbus.ObjectPath("/org/cargadgetd/aa")
	if !aaServer.exported {
		p := &aaProfile{conns: aaServer.conns, logf: logf}
		if err := conn.Export(p, path, "org.bluez.Profile1"); err != nil {
			return nil, fmt.Errorf("export profile: %w", err)
		}
		aaServer.exported = true
	}
	opts := map[string]dbus.Variant{
		// BlueZ synthesises the SDP record from these options. Version adds
		// the BluetoothProfileDescriptorList (attribute 0x0009) that head
		// units read before offering the Android Auto entry; Name becomes the
		// ServiceName (0x0100).
		"Name":                  dbus.MakeVariant("Android Auto"),
		"Role":                  dbus.MakeVariant("server"),
		"Service":               dbus.MakeVariant(aaUUIDText),
		"Channel":               dbus.MakeVariant(uint16(8)),
		"Version":               dbus.MakeVariant(uint16(0x0100)),
		"AutoConnect":           dbus.MakeVariant(false),
		"RequireAuthentication": dbus.MakeVariant(false),
		"RequireAuthorization":  dbus.MakeVariant(false),
	}
	call := func() error {
		return conn.Object(bluezService, "/org/bluez").
			Call("org.bluez.ProfileManager1.RegisterProfile", 0, path, aaUUIDText, opts).Err
	}
	err = call()
	if err != nil && !isAlreadyExists(err) {
		// Channel 8 may be taken; let BlueZ choose one.
		logf("profile: RegisterProfile channel=8 failed: %s; retrying with an automatic channel", dbusErrText(err))
		opts["Channel"] = dbus.MakeVariant(uint16(0))
		err = call()
	}
	if err != nil && !isAlreadyExists(err) {
		return nil, fmt.Errorf("register profile: %s", dbusErrText(err))
	}
	if !aaServer.registered {
		aaServer.registered = true
		logf("profile: Android Auto RFCOMM server registered (%s), options=%v", aaUUIDText, opts)
	}
	return aaServer.conns, nil
}

// isAlreadyExists reports a BlueZ "profile already registered" answer, which is
// success for an idempotent registration.
func isAlreadyExists(err error) bool {
	return err != nil && strings.Contains(dbusErrText(err), "AlreadyExists")
}
