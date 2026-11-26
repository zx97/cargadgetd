package session

import (
	"io"
	"log"
	"testing"
)

func TestVideoFocusProjectedNoInputKeepsStreaming(t *testing.T) {
	m := NewMD(nil, nil, log.New(io.Discard, "", 0))

	if err := m.onVideoFocus(ChanVideo, []byte{0x08, 0x01, 0x10, 0x01}); err != nil {
		t.Fatal(err)
	}
	if !m.VideoStreaming() {
		t.Fatal("focus=1 should start video")
	}
	select {
	case <-m.WaitVideoStarted():
	default:
		t.Fatal("focus=1 should release the video loop")
	}
	start := recvStartInd(t, m)

	// DHU sends this while the projection stays up (input focus elsewhere).
	// Pausing here reopened Codec2 on every return to focus=1.
	if err := m.onVideoFocus(ChanVideo, []byte{0x08, 0x04, 0x10, 0x01}); err != nil {
		t.Fatal(err)
	}
	if !m.VideoStreaming() {
		t.Fatal("focus=4 should keep video streaming")
	}
	if msg, ok := tryRecv(m); ok {
		t.Fatalf("focus=4 while streaming enqueued %x", msg.payload)
	}

	if err := m.onVideoFocus(ChanVideo, []byte{0x08, 0x01, 0x10, 0x01}); err != nil {
		t.Fatal(err)
	}
	if !m.VideoStreaming() {
		t.Fatal("focus=1 while streaming should stay up")
	}
	if msg, ok := tryRecv(m); ok {
		t.Fatalf("repeat focus=1 enqueued %x", msg.payload)
	}

	if err := m.onVideoFocus(ChanVideo, []byte{0x08, 0x02}); err != nil {
		t.Fatal(err)
	}
	if m.VideoStreaming() {
		t.Fatal("focus=2 (native) should pause video")
	}

	if err := m.onVideoFocus(ChanVideo, []byte{0x08, 0x01, 0x10, 0x01}); err != nil {
		t.Fatal(err)
	}
	if !m.VideoStreaming() {
		t.Fatal("focus=1 after native loss should resume")
	}
	again := recvStartInd(t, m)
	if string(again) != string(start) {
		t.Fatalf("resume StartInd %x, first was %x", again, start)
	}
}

func TestVideoFocus4StartsWhenIdle(t *testing.T) {
	m := NewMD(nil, nil, log.New(io.Discard, "", 0))
	if err := m.onVideoFocus(ChanVideo, []byte{0x08, 0x04, 0x10, 0x01}); err != nil {
		t.Fatal(err)
	}
	if !m.VideoStreaming() {
		t.Fatal("focus=4 with nothing playing should start video")
	}
	recvStartInd(t, m)
}

func recvStartInd(t *testing.T, m *MD) []byte {
	t.Helper()
	msg, ok := tryRecv(m)
	if !ok {
		t.Fatal("missing StartInd")
	}
	if len(msg.payload) < 2 || msg.payload[0] != 0x80 || msg.payload[1] != 0x01 {
		t.Fatalf("payload %x, want MediaStartInd", msg.payload)
	}
	// The focus request follows the StartInd: "ask to be shown", which is what
	// makes a unit stop showing its own wallpaper. Consume it here so the rest
	// of the tests still see "nothing else was enqueued".
	ask, ok := tryRecv(m)
	if !ok {
		t.Fatal("missing VideoFocusRequest after StartInd")
	}
	if len(ask.payload) < 2 || ask.payload[0] != 0x80 || ask.payload[1] != 0x07 {
		t.Fatalf("payload %x, want VideoFocusRequest after StartInd", ask.payload)
	}
	if len(ask.payload) < 4 || ask.payload[2] != 0x08 || ask.payload[3] != 0x01 {
		t.Fatalf("focus request %x, want mode projected (08 01)", ask.payload)
	}
	return msg.payload
}

func tryRecv(m *MD) (outMsg, bool) {
	select {
	case msg := <-m.sendQ:
		return msg, true
	default:
		return outMsg{}, false
	}
}
