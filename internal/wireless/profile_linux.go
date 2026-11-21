//go:build linux

package wireless

import (
	"fmt"
	"os"
	"sync"

	"github.com/godbus/dbus/v5"
	"golang.org/x/sys/unix"
)

// aaProfile is the org.bluez.Profile1 object. The car discovers the AA UUID
// over SDP and connects its RFCOMM channel to the phone; NewConnection hands
// us the resulting stream.
type aaProfile struct {
	conns chan *os.File
}

func (p *aaProfile) NewConnection(device dbus.ObjectPath, fd dbus.UnixFD, props map[string]dbus.Variant) *dbus.Error {
	dup, err := unix.Dup(int(fd))
	if err != nil {
		return dbus.MakeFailedError(err)
	}
	f := os.NewFile(uintptr(dup), "rfcomm-in")
	if f == nil {
		unix.Close(dup)
		return dbus.MakeFailedError(fmt.Errorf("os.NewFile"))
	}
	select {
	case p.conns <- f:
	default:
		_ = f.Close()
	}
	return nil
}

func (p *aaProfile) RequestDisconnection(device dbus.ObjectPath) *dbus.Error { return nil }
func (p *aaProfile) Release() *dbus.Error                                    { return nil }

var aaServer struct {
	once  sync.Once
	conns chan *os.File
	err   error
}

// ensureAAProfile registers the Android Auto RFCOMM server profile once, so
// the head unit sees the AA UUID in the phone's SDP record and offers the
// Android Auto entry.
func ensureAAProfile(logf func(string, ...any)) (<-chan *os.File, error) {
	aaServer.once.Do(func() {
		aaServer.conns = make(chan *os.File, 1)
		conn, err := dbus.SystemBus()
		if err != nil {
			aaServer.err = err
			return
		}
		path := dbus.ObjectPath("/org/cargadgetd/aa")
		p := &aaProfile{conns: aaServer.conns}
		if err := conn.Export(p, path, "org.bluez.Profile1"); err != nil {
			aaServer.err = fmt.Errorf("export profile: %w", err)
			return
		}
		opts := map[string]dbus.Variant{
			"Role":                  dbus.MakeVariant("server"),
			"Service":               dbus.MakeVariant(aaUUIDText),
			"Channel":               dbus.MakeVariant(uint16(8)),
			"AutoConnect":           dbus.MakeVariant(false),
			"RequireAuthentication": dbus.MakeVariant(false),
			"RequireAuthorization":  dbus.MakeVariant(false),
		}
		call := func() error {
			return conn.Object(bluezService, "/org/bluez").
				Call("org.bluez.ProfileManager1.RegisterProfile", 0, path, aaUUIDText, opts).Err
		}
		err = call()
		if err != nil {
			// Channel 8 may be taken; let BlueZ choose one.
			opts["Channel"] = dbus.MakeVariant(uint16(0))
			err = call()
		}
		if err != nil {
			aaServer.err = fmt.Errorf("register profile: %w", err)
			return
		}
		if logf != nil {
			logf("profile: Android Auto RFCOMM server registered (%s)", aaUUIDText)
		}
	})
	return aaServer.conns, aaServer.err
}
