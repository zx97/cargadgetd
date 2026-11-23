package livevid

import (
	"image"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// wedgedCodec never returns from Encode, as Codec2 does when it locks up.
type wedgedCodec struct {
	entered chan struct{}
	once    sync.Once
}

func (w *wedgedCodec) Name() string { return "wedged" }
func (w *wedgedCodec) Encode(*image.RGBA) ([]byte, error) {
	w.once.Do(func() { close(w.entered) })
	select {} // block forever, like droid_push
}
func (w *wedgedCodec) Close() error { return nil }

// goodCodec answers at once.
type goodCodec struct{ n int }

func (g *goodCodec) Name() string { return "good" }
func (g *goodCodec) Encode(*image.RGBA) ([]byte, error) {
	g.n++
	return []byte{0, 0, 0, 1, 0x65, byte(g.n)}, nil
}
func (g *goodCodec) Close() error { return nil }

// A wedged encoder must not freeze the session: the frame is dropped, a fresh
// encoder takes over, and the following frame carries a picture again.
func TestEncodeRecoversFromWedgedCodec(t *testing.T) {
	oldStall, oldHook := EncodeStall, openCodecHook
	EncodeStall = 50 * time.Millisecond
	t.Cleanup(func() { EncodeStall, openCodecHook = oldStall, oldHook })

	var opened int32
	wedged := &wedgedCodec{entered: make(chan struct{})}
	openCodecHook = func(w, h, fps, bps int, want string) (codec, error) {
		if atomic.AddInt32(&opened, 1) == 1 {
			return wedged, nil
		}
		return &goodCodec{}, nil
	}

	r, err := New(Options{Width: 64, Height: 64, FPS: 30})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	// First frame: the codec never answers, so Encode must return instead of
	// hanging, and it must have replaced the encoder.
	done := make(chan error, 1)
	go func() {
		_, err := r.Encode(time.Now())
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a stalled encoder must report an error")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Encode hung on a wedged codec")
	}
	<-wedged.entered
	if n := atomic.LoadInt32(&opened); n != 2 {
		t.Fatalf("opened %d encoders, want 2 (wedge + replacement)", n)
	}

	// Next frame: the replacement answers.
	au, err := r.Encode(time.Now())
	if err != nil {
		t.Fatalf("frame after recovery: %v", err)
	}
	if len(au) == 0 {
		t.Fatal("empty AU after recovery")
	}
}

// After MaxEncodeRecovery wedges the session must end, so the service restarts
// instead of dropping frames forever.
func TestEncodeGivesUpAfterTooManyWedges(t *testing.T) {
	oldStall, oldHook := EncodeStall, openCodecHook
	EncodeStall = 20 * time.Millisecond
	t.Cleanup(func() { EncodeStall, openCodecHook = oldStall, oldHook })

	openCodecHook = func(w, h, fps, bps int, want string) (codec, error) {
		return &wedgedCodec{entered: make(chan struct{})}, nil
	}
	r, err := New(Options{Width: 64, Height: 64, FPS: 30})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	var last error
	for i := 0; i <= MaxEncodeRecovery+1; i++ {
		_, last = r.Encode(time.Now())
	}
	if last == nil {
		t.Fatalf("expected a fatal error after %d recoveries", MaxEncodeRecovery)
	}
	if want := "ending the session"; !contains(last.Error(), want) {
		t.Fatalf("error %q does not say the session ends", last)
	}
	// The reason must stick: a later frame reports the same thing, not
	// "closed".
	if _, err := r.Encode(time.Now()); err == nil || !contains(err.Error(), "ending the session") {
		t.Fatalf("latched error = %v", err)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
