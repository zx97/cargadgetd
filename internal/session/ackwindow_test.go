package session

import (
	"io"
	"log"
	"testing"
	"time"
)

// A head unit that acks every frame must keep the narrow window: widening it
// there is how a DHU gets flooded (BAD_DECRYPT on the old USB path).
func TestVideoWindowStaysNarrowForPerFrameAcks(t *testing.T) {
	md := NewMD(nil, nil, log.New(io.Discard, "", 0))
	md.SetUSBBulk(true)
	md.videoStreaming.Store(true)
	base := time.Now()
	for i := 0; i < 12; i++ {
		md.noteAckTime(base.Add(time.Duration(i) * 33 * time.Millisecond)) // ~30/s
	}
	if got := md.videoWindow(); got != maxVideoInFlight {
		t.Fatalf("window = %d, want %d for a per-frame acker", got, maxVideoInFlight)
	}
}

// The lab Melia unit acks one frame in twenty-five. The window must widen, or
// the picture runs at 1.9 fps instead of the 21.8 fps the same unit shows over
// Wi-Fi.
func TestVideoWindowWidensForBatchAcks(t *testing.T) {
	md := NewMD(nil, nil, log.New(io.Discard, "", 0))
	md.SetUSBBulk(true)
	md.videoStreaming.Store(true)
	base := time.Now()
	for i := 0; i < 8; i++ {
		md.noteAckTime(base.Add(time.Duration(i) * 800 * time.Millisecond)) // ~1.2/s
	}
	if got := md.videoWindow(); got != batchAckWindow {
		t.Fatalf("window = %d, want %d for a batch acker", got, batchAckWindow)
	}
	// Several AUs may now wait at once instead of one per ack.
	for i := 0; i < 10; i++ {
		if err := md.ReserveVideoCredit(0); err != nil {
			t.Fatalf("reserve %d: %v", i, err)
		}
	}
	if n := md.VideoInFlight(); n < 10 {
		t.Fatalf("in_flight = %d, want the wide window", n)
	}
}

func TestVideoWindowUnknownBeforeSamples(t *testing.T) {
	md := NewMD(nil, nil, log.New(io.Discard, "", 0))
	md.SetUSBBulk(true)
	md.videoStreaming.Store(true)
	if got := md.videoWindow(); got != maxVideoInFlight {
		t.Fatalf("window = %d before any ack, want %d", got, maxVideoInFlight)
	}
}
