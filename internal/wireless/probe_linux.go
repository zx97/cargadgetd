//go:build linux

package wireless

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"
)

// candidateAAChannels is the order in which we probe RFCOMM channels when SDP
// fails. A VW MIB3 publishes Android Auto (UUID 4de17a00…) as a vendor "SPP"
// record on channel 7 and its generic SPP service on 8 — dialling 8 reaches a
// service that never speaks WPP, which looked like "the car ignores us".
// The rest are common AA channels on other head units.
var candidateAAChannels = []uint8{7, 8, 9}

// fullScan is the rest of the RFCOMM range, probed only when the common
// channels are refused. A universal head unit with a working phone picked its
// own channel; without SDP (which some units refuse on this stack) the only way
// to find it is to ask every channel. Refusals come back in tens of
// milliseconds, so the whole range costs seconds.
func fullScan() []uint8 {
	var out []uint8
	for ch := 1; ch <= 30; ch++ {
		if ch == 7 || ch == 8 || ch == 9 {
			continue
		}
		out = append(out, uint8(ch))
	}
	return out
}

// probeAAChannel finds the head unit's Android Auto RFCOMM channel by talking
// to it: connect, send a WifiInfoRequest, and keep the channel that answers.
// This is protocol-level discovery and does not depend on SDP, which the unit
// often refuses while its baseband link is not fully up.
func probeAAChannel(ctx context.Context, addr string, logf func(string, ...any)) (uint8, error) {
	down := 0
	hangs := 0
	channels := append(append([]uint8{}, candidateAAChannels...), fullScan()...)
	for i, ch := range channels {
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		// The extended range is a last resort: do not spend a second between
		// attempts there, refusals are immediate.
		pause := time.Second
		if i >= len(candidateAAChannels) {
			pause = 200 * time.Millisecond
		}
		t := time.Now()
		// The SABM for a channel with no listener can hang past the controller's
		// own timeout on this stack, which stalled the whole scan at channel 9.
		// Bound each attempt and go on to the next channel.
		type dialResult struct {
			rc  net.Conn
			err error
		}
		done := make(chan dialResult, 1)
		go func(ch uint8) {
			rc, err := dialRFCOMM(addr, ch, logf)
			done <- dialResult{rc, err}
		}(ch)
		var rc net.Conn
		var err error
		select {
		case r := <-done:
			rc, err = r.rc, r.err
		case <-time.After(1500 * time.Millisecond):
			// A refused channel answers in ~40ms; a hanging one means no service
			// at all (head unit off, or out of range). 4s per channel made a
			// scan without a head unit take two minutes, which is far too slow
			// to ever catch a one-minute window.
			hangs++
			logf("probe: channel %d: no answer within 1.5s, skipping", ch)
			if hangs >= 5 {
				return 0, fmt.Errorf("probe: %d channels never answered, the controller is not responding", hangs)
			}
			continue
		}
		if err != nil {
			logf("probe: channel %d: connect failed after %s: %v", ch, sinceText(t), err)
			// A page timeout means the baseband link is not up: probing the
			// remaining channels would spend a minute per channel for nothing.
			if msg := err.Error(); strings.Contains(msg, "host is down") || strings.Contains(msg, "timed out") {
				down++
				if down >= 2 {
					return 0, fmt.Errorf("probe: RFCOMM link is down, cannot probe channels (%v)", err)
				}
			} else {
				down = 0
			}
			continue
		}
		down = 0
		logf("probe: channel %d: connected, asking for WPP", ch)
		// Deadlines are not honoured here: the RFCOMM fd is a plain file to the
		// Go runtime, so a read on a channel that accepted and then stays
		// silent blocks the whole scan. Bound it with a goroutine instead.
		type wppResult struct {
			werr     error
			answered bool
		}
		wres := make(chan wppResult, 1)
		go func(rc net.Conn) {
			_ = rc.SetReadDeadline(time.Now().Add(2 * time.Second))
			// Same opening as the handshake: the unit answered the info request in
			// the sessions that worked, and the version request wakes units that
			// wait for the phone to speak first.
			werr := writeWPP(rc, wppVersionRequest, buildVersionRequest(1, 0))
			if werr == nil {
				werr = writeWPP(rc, wppInfoRequest, nil)
			}
			answered := false
			if werr == nil {
				buf := make([]byte, 4)
				if n, rerr := rc.Read(buf); rerr == nil && n == 4 {
					answered = true
				}
			}
			wres <- wppResult{werr, answered}
		}(rc)
		var werr error
		answered := false
		// A channel that accepted is a real service, and the head unit may take
		// seconds before it sends anything. Measured on Android Auto against the
		// bench unit: the version request arrives 5.4 s after the RFCOMM connect,
		// and Google's own budget is 12 s. A 3 s wait closed the socket just
		// before the unit spoke: "channel 1: opened but no WPP answer within 3s",
		// then the scan moved on to channels that were refused.
		select {
		case r := <-wres:
			werr, answered = r.werr, r.answered
		case <-time.After(12 * time.Second):
			logf("probe: channel %d: opened but no WPP answer within 12s", ch)
			werr = fmt.Errorf("no WPP answer")
		}
		rc.Close()
		time.Sleep(pause)
		if werr != nil {
			logf("probe: channel %d: no WPP answer after %s: %v", ch, sinceText(t), werr)
			continue
		}
		if answered {
			logf("probe: channel %d answered the WPP info request after %s", ch, sinceText(t))
			return ch, nil
		}
		logf("probe: channel %d opened but did not answer WPP (another service)", ch)
	}
	return 0, fmt.Errorf("probe: no RFCOMM channel answered WPP (tried %v and 1-30)", candidateAAChannels)
}
