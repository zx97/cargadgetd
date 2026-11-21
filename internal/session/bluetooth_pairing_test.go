package session_test

import (
	"io"
	"log"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/cargadgetd/cargadgetd/internal/session"
	"github.com/cargadgetd/cargadgetd/internal/session/hupeer"
	aassl "github.com/cargadgetd/cargadgetd/internal/ssl"
)

// TestBluetoothPairingAnnouncesAddress drives a full handshake and checks that
// the MD opens the head unit's Bluetooth channel and sends its Bluetooth
// address there — the bridge between the USB session and the Bluetooth bond.
func TestBluetoothPairingAnnouncesAddress(t *testing.T) {
	dir := certDir(t)
	mdSSL, err := aassl.NewServer(filepath.Join(dir, "android_auto.crt"), filepath.Join(dir, "android_auto.key"))
	if err != nil {
		t.Fatal(err)
	}
	defer mdSSL.Close()
	huSSL, err := aassl.NewClient(filepath.Join(dir, "headunit.crt"), filepath.Join(dir, "headunit.key"))
	if err != nil {
		t.Fatal(err)
	}
	defer huSSL.Close()

	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()

	const phone = "50:56:A8:06:03:39"
	md := session.NewMD(a, mdSSL, log.New(io.Discard, "", 0))
	md.SetLocalBluetooth(phone, "Manolla")
	hu := hupeer.New(b, huSSL, log.New(io.Discard, "", 0))

	errMD := make(chan error, 1)
	go func() { errMD <- md.Run() }()
	go func() { _ = hu.Run() }()

	select {
	case <-md.WaitReady():
	case err := <-errMD:
		t.Fatalf("md failed before ready: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("timeout waiting for the service discovery response")
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if n, addr, methods := hu.BluetoothPairing(); n > 0 {
			if addr != phone {
				t.Fatalf("announced address = %q, want %q", addr, phone)
			}
			if len(methods) == 0 {
				t.Fatal("no pairing method advertised in the request")
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the MD never sent BluetoothPairingRequest on the Bluetooth channel")
}
