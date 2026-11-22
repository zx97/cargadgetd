package session_test

import (
	"errors"
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

// pairing runs an MD against the fake head unit until service discovery has
// completed. The returned channels carry each side's Run result.
type pairing struct {
	md, hu      *session.MD
	peer        *hupeer.Peer
	mdErr, huEr chan error
}

func newPairing(t *testing.T) *pairing {
	t.Helper()
	dir := certDir(t)
	a, b := net.Pipe()
	t.Cleanup(func() { a.Close(); b.Close() })

	mdSSL, err := aassl.NewServer(filepath.Join(dir, "android_auto.crt"), filepath.Join(dir, "android_auto.key"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { mdSSL.Close() })
	huSSL, err := aassl.NewClient(filepath.Join(dir, "headunit.crt"), filepath.Join(dir, "headunit.key"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { huSSL.Close() })

	p := &pairing{
		md:    session.NewMD(a, mdSSL, log.New(io.Discard, "", 0)),
		peer:  hupeer.New(b, huSSL, log.New(io.Discard, "", 0)),
		mdErr: make(chan error, 1),
		huEr:  make(chan error, 1),
	}
	go func() { p.mdErr <- p.md.Run() }()
	go func() { p.huEr <- p.peer.Run() }()
	select {
	case <-p.md.WaitReady():
	case err := <-p.mdErr:
		t.Fatalf("md failed before ready: %v", err)
	case err := <-p.huEr:
		t.Fatalf("hu failed before ready: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("timeout waiting for SDR")
	}
	return p
}

// TestByeByeIsAcknowledged covers a car that leaves Android Auto: it sends a
// ByeBye and waits for the answer before closing. Ignoring it (the old
// behaviour) left the car waiting and the phone in a session nobody ended.
func TestByeByeIsAcknowledged(t *testing.T) {
	p := newPairing(t)

	if err := p.peer.SendByeBye(1); err != nil { // USER_SELECTION
		t.Fatal(err)
	}

	select {
	case err := <-p.mdErr:
		if !errors.Is(err, session.ErrHeadUnitByeBye) {
			t.Fatalf("md Run error = %v, want ErrHeadUnitByeBye", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the session did not end after bye-bye")
	}

	deadline := time.After(2 * time.Second)
	for p.peer.ByeByeResponses() == 0 {
		select {
		case <-deadline:
			t.Fatal("the phone never acknowledged the bye-bye")
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// TestSilentHeadUnitEndsSession covers a car that reboots mid-projection: the
// socket stays half-open, so only a silence check can tell the session is dead.
// Without it the daemon pushed frames for minutes and never retried.
func TestSilentHeadUnitEndsSession(t *testing.T) {
	old := session.SilentHU
	session.SilentHU = 300 * time.Millisecond
	t.Cleanup(func() { session.SilentHU = old })

	p := newPairing(t)

	// The fixture only talks during the handshake: stop it from pinging by
	// leaving it idle, and wait for the watchdog.
	select {
	case err := <-p.mdErr:
		if err == nil {
			t.Fatal("md Run returned nil, want a silence error")
		}
		if !errors.Is(err, session.ErrHeadUnitSilent) && err.Error() != "head unit silent" {
			t.Fatalf("md Run error = %v, want head unit silent", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a silent head unit did not end the session")
	}
}
