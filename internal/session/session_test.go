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
	"github.com/cargadgetd/cargadgetd/internal/testpem"
)

func certDir(t *testing.T) string {
	t.Helper()
	return testpem.Dir(t)
}

func TestPhonePingsLikeAWirelessLink(t *testing.T) {
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

	md := session.NewMD(a, mdSSL, log.New(io.Discard, "", 0))
	hu := hupeer.New(b, huSSL, log.New(io.Discard, "", 0))
	errMD := make(chan error, 1)
	go func() { errMD <- md.Run() }()
	go func() { _ = hu.Run() }()

	select {
	case <-md.WaitReady():
	case err := <-errMD:
		t.Fatalf("md failed before ready: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("timeout waiting for SDR")
	}

	// USB stays silent and only answers; a wireless link pings every second.
	time.Sleep(1200 * time.Millisecond)
	if n := hu.PhonePings(); n != 0 {
		t.Fatalf("pinged %d times before EnablePings", n)
	}
	md.EnablePings(50 * time.Millisecond)

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && hu.PhonePings() < 5 {
		time.Sleep(20 * time.Millisecond)
	}
	if n := hu.PhonePings(); n < 5 {
		t.Fatalf("only %d pings in 3s, want at least 5", n)
	}
	// The head unit answered them: an unhandled PingResponse used to be a
	// strict-mode session failure, so the session must still be alive.
	select {
	case err := <-errMD:
		t.Fatalf("session ended on answered pings: %v", err)
	default:
	}
}

func TestNoPingsBeforeTheSessionIsUp(t *testing.T) {
	// A ping is an encrypted frame. Sending one before the TLS handshake is
	// complete leaves the handshake unfinished: the bench unit answered the
	// version request, sent its ServerHello, and then waited 25 s for a
	// ClientHello that had already been written as ciphertext.
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

	md := session.NewMD(a, mdSSL, log.New(io.Discard, "", 0))
	md.EnablePings(20 * time.Millisecond) // as soon as the daemon would
	hu := hupeer.New(b, huSSL, log.New(io.Discard, "", 0))
	errMD := make(chan error, 1)
	go func() { errMD <- md.Run() }()
	go func() { _ = hu.Run() }()

	select {
	case <-md.WaitReady():
	case err := <-errMD:
		t.Fatalf("handshake failed while pings were configured: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("timeout waiting for SDR: the handshake never completed")
	}

	// From here the session is up, so pings must flow.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && hu.PhonePings() < 3 {
		time.Sleep(10 * time.Millisecond)
	}
	if n := hu.PhonePings(); n < 3 {
		t.Fatalf("only %d pings once the session was up", n)
	}
}

func TestMDHUhandshakeAVTouch(t *testing.T) {

	dir := certDir(t)
	mdCert := filepath.Join(dir, "android_auto.crt")
	mdKey := filepath.Join(dir, "android_auto.key")
	huCert := filepath.Join(dir, "headunit.crt")
	huKey := filepath.Join(dir, "headunit.key")

	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()

	mdSSL, err := aassl.NewServer(mdCert, mdKey)
	if err != nil {
		t.Fatal(err)
	}
	defer mdSSL.Close()
	huSSL, err := aassl.NewClient(huCert, huKey)
	if err != nil {
		t.Fatal(err)
	}
	defer huSSL.Close()

	md := session.NewMD(a, mdSSL, log.New(io.Discard, "", 0))
	hu := hupeer.New(b, huSSL, log.New(io.Discard, "", 0))

	errMD := make(chan error, 1)
	errHU := make(chan error, 1)
	go func() { errMD <- md.Run() }()
	go func() { errHU <- hu.Run() }()

	select {
	case <-md.WaitReady():
	case err := <-errMD:
		t.Fatalf("md failed before ready: %v", err)
	case err := <-errHU:
		t.Fatalf("hu failed before ready: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("timeout waiting for SDR")
	}

	if err := md.OpenVideo(); err != nil {
		t.Fatal(err)
	}
	if err := md.OpenAudio(); err != nil {
		t.Fatal(err)
	}
	if err := md.OpenInput(); err != nil {
		t.Fatal(err)
	}
	if err := md.OpenSensor(); err != nil {
		t.Fatal(err)
	}

	// drain a few events / allow HU to respond
	deadline := time.After(3 * time.Second)
	gotVideoSetup := false
	gotAudioSetup := false
loop:
	for {
		select {
		case ev := <-md.Events:
			switch ev.Type {
			case "video_setup_ok", "video_focus":
				gotVideoSetup = true
			case "audio_setup_ok":
				gotAudioSetup = true
			case "touch":
				// ok
			}
			if gotVideoSetup && gotAudioSetup {
				break loop
			}
		case <-deadline:
			break loop
		}
	}

	nal := make([]byte, 64)
	nal[0], nal[1], nal[2], nal[3], nal[4] = 0, 0, 0, 1, 0x67
	for i := 0; i < 3; i++ {
		if err := md.SendH264(uint64(i+1)*33333, nal); err != nil {
			t.Fatal(err)
		}
		if err := md.SendPCM(uint64(i+1)*10000, make([]byte, 480)); err != nil {
			t.Fatal(err)
		}
	}
	if err := hu.SendTouch(100, 200); err != nil {
		t.Fatal(err)
	}

	time.Sleep(200 * time.Millisecond)

	videoFrames, audioFrames := hu.Frames()
	if videoFrames < 1 {
		t.Fatalf("expected video frames, got %d", videoFrames)
	}
	if audioFrames < 1 {
		t.Fatalf("expected audio frames, got %d", audioFrames)
	}
	if hu.Touches() < 1 {
		t.Fatalf("expected touch sent")
	}

	// Expect MD to observe touch
	touchDeadline := time.After(2 * time.Second)
	for {
		select {
		case ev := <-md.Events:
			if ev.Type == "touch" {
				touch, ok := session.ParseTouch(ev.Payload)
				if !ok || touch.X != 100 || touch.Y != 200 {
					t.Fatalf("touch %+v ok=%v", touch, ok)
				}
				return
			}
		case <-touchDeadline:
			t.Fatal("md did not receive touch event")
		}
	}
}

func TestUSBVideoNAckHoldThenRecover(t *testing.T) {
	dir := certDir(t)
	mdCert := filepath.Join(dir, "android_auto.crt")
	mdKey := filepath.Join(dir, "android_auto.key")
	huCert := filepath.Join(dir, "headunit.crt")
	huKey := filepath.Join(dir, "headunit.key")

	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()

	mdSSL, err := aassl.NewServer(mdCert, mdKey)
	if err != nil {
		t.Fatal(err)
	}
	defer mdSSL.Close()
	huSSL, err := aassl.NewClient(huCert, huKey)
	if err != nil {
		t.Fatal(err)
	}
	defer huSSL.Close()

	md := session.NewMD(a, mdSSL, log.New(io.Discard, "", 0))
	md.SetUSBBulk(true)
	hu := hupeer.New(b, huSSL, log.New(io.Discard, "", 0))

	errMD := make(chan error, 1)
	errHU := make(chan error, 1)
	go func() { errMD <- md.Run() }()
	go func() { errHU <- hu.Run() }()

	select {
	case <-md.WaitReady():
	case err := <-errMD:
		t.Fatalf("md failed before ready: %v", err)
	case err := <-errHU:
		t.Fatalf("hu failed before ready: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("timeout waiting for SDR")
	}

	if err := md.OpenVideo(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-md.WaitVideoStarted():
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for video StartInd")
	}

	nal := make([]byte, 64)
	nal[0], nal[1], nal[2], nal[3], nal[4] = 0, 0, 0, 1, 0x67
	hu.SkipVideoAck.Store(true)
	// A head unit may batch its acks, so a few AUs may wait at once; past that
	// the sender must stop instead of flooding the pipe.
	for i := 0; i < 8; i++ {
		err := md.SendH264(33333, nal)
		if errors.Is(err, session.ErrNoVideoCredit) {
			break
		}
		if err != nil {
			t.Fatalf("AU %d: %v", i, err)
		}
	}
	if n := md.VideoInFlight(); n < 2 {
		t.Fatalf("in_flight=%d, want several AUs allowed to wait", n)
	}
	if err := md.ReserveVideoCredit(0); !errors.Is(err, session.ErrNoVideoCredit) {
		t.Fatalf("want ErrNoVideoCredit while ACKs are held, got %v", err)
	}

	// One ack releases one AU; the head unit acks every frame it accepted.
	for i, n := 0, md.VideoInFlight(); i < n; i++ {
		if err := hu.SendMediaAck(md.VideoCh); err != nil {
			t.Fatalf("late MediaAck %d: %v", i, err)
		}
	}
	deadline := time.Now().Add(2 * time.Second)
	for md.VideoInFlight() > 0 {
		if time.Now().After(deadline) {
			t.Fatal("late MediaAck did not restore video credit")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := md.ReserveVideoCredit(0); err != nil {
		t.Fatalf("credit after late ACK: %v", err)
	}
	md.ReleaseVideoCredit()
}

func TestGiveUpVideoAckAfterSilence(t *testing.T) {
	dir := certDir(t)
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
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
	md := session.NewMD(a, mdSSL, log.New(io.Discard, "", 0))
	md.SetUSBBulk(true)
	hu := hupeer.New(b, huSSL, log.New(io.Discard, "", 0))
	go func() { _ = md.Run() }()
	go func() { _ = hu.Run() }()
	select {
	case <-md.WaitReady():
	case <-time.After(10 * time.Second):
		t.Fatal("timeout waiting for SDR")
	}
	if err := md.OpenVideo(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-md.WaitVideoStarted():
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for video StartInd")
	}

	lost := []byte{0, 0, 0, 1, 0x65, 0x11}
	next := []byte{0, 0, 0, 1, 0x65, 0x22}
	hu.SkipVideoAck.Store(true)
	sent := time.Now()
	if err := md.SendH264(33333, lost); err != nil {
		t.Fatalf("first AU: %v", err)
	}
	if md.GiveUpVideoAck(session.VideoAckGiveUp) {
		t.Fatal("gave up before the ack could be missing")
	}
	if md.VideoInFlight() != 1 {
		t.Fatalf("in_flight=%d want 1", md.VideoInFlight())
	}
	time.Sleep(session.VideoAckGiveUp + 20*time.Millisecond)
	if !md.GiveUpVideoAck(session.VideoAckGiveUp) {
		t.Fatalf("ack still held after %s", time.Since(sent).Round(time.Millisecond))
	}
	gaveUp := time.Since(sent)
	if gaveUp > session.VideoAckGiveUp+300*time.Millisecond {
		t.Fatalf("give-up took %s, want about %s", gaveUp.Round(time.Millisecond), session.VideoAckGiveUp)
	}
	if md.VideoInFlight() != 0 {
		t.Fatalf("in_flight=%d after give-up", md.VideoInFlight())
	}
	t.Logf("missing ack: gave up after %s (limit %s), frame not retransmitted", gaveUp.Round(time.Millisecond), session.VideoAckGiveUp)
	// The abandoned frame's ack must not drive the counter negative.
	if err := hu.SendMediaAck(md.VideoCh); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	if md.VideoInFlight() != 0 {
		t.Fatalf("late ack changed in_flight to %d", md.VideoInFlight())
	}
	hu.SkipVideoAck.Store(false)
	if err := md.SendH264(66666, next); err != nil {
		t.Fatalf("next AU: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for md.VideoInFlight() > 0 {
		if time.Now().After(deadline) {
			t.Fatal("MediaAck after give-up did not free credit")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got, _ := hu.Frames(); got != 2 {
		t.Fatalf("head unit got %d video frames, want 2 (lost + next, no replay)", got)
	}
}
