//go:build linux

package wireless

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// candidateAAChannels is the order in which we probe RFCOMM channels when SDP
// fails. A VW MIB3 publishes Android Auto (UUID 4de17a00…) as a vendor "SPP"
// record on channel 7 and its generic SPP service on 8 — dialling 8 reaches a
// service that never speaks WPP, which looked like "the car ignores us".
// The rest are common AA channels on other head units.
var candidateAAChannels = []uint8{7, 9, 6, 5, 10, 11, 4, 3, 12, 13, 2, 14, 15}

// probeAAChannel finds the head unit's Android Auto RFCOMM channel by talking
// to it: connect, send a WifiInfoRequest, and keep the channel that answers.
// This is protocol-level discovery and does not depend on SDP, which the unit
// often refuses while its baseband link is not fully up.
func probeAAChannel(ctx context.Context, addr string, logf func(string, ...any)) (uint8, error) {
	down := 0
	for _, ch := range candidateAAChannels {
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		t := time.Now()
		rc, err := dialRFCOMM(addr, ch, logf)
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
		_ = rc.SetReadDeadline(time.Now().Add(2 * time.Second))
		werr := writeWPP(rc, wppInfoRequest, nil)
		answered := false
		if werr == nil {
			buf := make([]byte, 4)
			if n, rerr := rc.Read(buf); rerr == nil && n == 4 {
				answered = true
			}
		}
		rc.Close()
		if werr != nil {
			logf("probe: channel %d: write failed after %s: %v", ch, sinceText(t), werr)
			continue
		}
		if answered {
			logf("probe: channel %d answered the WPP info request after %s", ch, sinceText(t))
			return ch, nil
		}
		logf("probe: channel %d opened but did not answer WPP (another service)", ch)
	}
	return 0, fmt.Errorf("probe: no channel among %v answered WPP", candidateAAChannels)
}
