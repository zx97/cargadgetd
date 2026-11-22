//go:build linux

package wireless

import (
	"fmt"
	"os"
	"sync"

	"github.com/godbus/dbus/v5"
)

// AAProfileOptions configures the Android Auto RFCOMM server profile a head
// unit emulator registers with BlueZ.
type AAProfileOptions struct {
	// Channel is the RFCOMM channel to publish. A VW MIB3 uses 7.
	Channel uint16
	// Name is the ServiceName in the generated SDP record.
	Name string
	// Version is the BluetoothProfileDescriptorList version (0x0100 for AA).
	Version uint16
}

// RegisterAAProfile registers an Android Auto server profile on the local
// adapter and returns the accepted RFCOMM connections. It is the head-unit
// counterpart of ensureAAProfile (which runs on the phone): cmd/fakecar uses it
// to impersonate a car on a Linux host, so the whole wireless bootstrap can be
// exercised without a vehicle.
func RegisterAAProfile(opt AAProfileOptions, logf func(string, ...any)) (<-chan *os.File, error) {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	if opt.Channel == 0 {
		opt.Channel = 7
	}
	if opt.Name == "" {
		opt.Name = "Android Auto Emulator"
	}
	if opt.Version == 0 {
		opt.Version = 0x0100
	}
	conn, err := dbus.SystemBus()
	if err != nil {
		return nil, fmt.Errorf("system bus: %w", err)
	}
	path := dbus.ObjectPath("/org/cargadgetd/fakecar")
	ch := make(chan *os.File, 8)
	p := &aaProfile{conns: ch, logf: logf}
	if err := conn.Export(p, path, "org.bluez.Profile1"); err != nil {
		return nil, fmt.Errorf("export profile: %w", err)
	}
	opts := map[string]dbus.Variant{
		"Name":                  dbus.MakeVariant(opt.Name),
		"Role":                  dbus.MakeVariant("server"),
		"Service":               dbus.MakeVariant(aaUUIDText),
		"Channel":               dbus.MakeVariant(opt.Channel),
		"Version":               dbus.MakeVariant(opt.Version),
		"AutoConnect":           dbus.MakeVariant(false),
		"RequireAuthentication": dbus.MakeVariant(false),
		"RequireAuthorization":  dbus.MakeVariant(false),
	}
	err = conn.Object(bluezService, "/org/bluez").
		Call("org.bluez.ProfileManager1.RegisterProfile", 0, path, aaUUIDText, opts).Err
	if err != nil {
		return nil, fmt.Errorf("register profile: %s", dbusErrText(err))
	}
	logf("fakecar: AA profile registered (uuid=%s name=%q channel=%d version=0x%04x)",
		aaUUIDText, opt.Name, opt.Channel, opt.Version)
	return ch, nil
}

// EnsureProfileOnce is a small helper: RegisterAAProfile may be called twice
// (an emulator restart) and BlueZ refuses a duplicate export path. Callers use
// this to keep one registration per process.
var (
	profileOnce sync.Once
	profileCh   <-chan *os.File
	profileErr  error
)

// RegisterAAProfileOnce is RegisterAAProfile with a process-wide once, for
// long-running emulators.
func RegisterAAProfileOnce(opt AAProfileOptions, logf func(string, ...any)) (<-chan *os.File, error) {
	profileOnce.Do(func() {
		profileCh, profileErr = RegisterAAProfile(opt, logf)
	})
	return profileCh, profileErr
}
