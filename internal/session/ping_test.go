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
	"github.com/cargadgetd/cargadgetd/internal/testpem"
)

// A wireless link pings about once a second; USB only answers.
func TestPhonePingsOnceTheSessionIsUp(t *testing.T) {
	dir := testpem.Dir(t)
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

	md := session.NewMD(a, mdSSL, log.New(io.Discard, "", 0))
	md.EnablePings(20 * time.Millisecond) // before Run: must not break the handshake
	hu := hupeer.New(b, huSSL, log.New(io.Discard, "", 0))
	errMD := make(chan error, 1)
	go func() { errMD <- md.Run() }()
	go func() { _ = hu.Run() }()

	select {
	case <-md.WaitReady():
	case err := <-errMD:
		t.Fatalf("handshake failed with pings configured: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("timeout waiting for SDR")
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && hu.PhonePings() < 5 {
		time.Sleep(20 * time.Millisecond)
	}
	if n := hu.PhonePings(); n < 5 {
		t.Fatalf("only %d pings in 3s after the session was up", n)
	}
	select {
	case err := <-errMD:
		t.Fatalf("session ended on answered pings: %v", err)
	default:
	}
}
