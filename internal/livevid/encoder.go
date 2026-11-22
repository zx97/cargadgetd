package livevid

import (
	"image"
	"strings"
)

// codec turns one RGBA frame into one Annex-B H.264 access unit.
type codec interface {
	Name() string
	Encode(*image.RGBA) ([]byte, error)
	Close() error
}

func openCodec(w, h, fps, bps int, want string) (codec, error) {
	switch strings.ToLower(strings.TrimSpace(want)) {
	case "software":
		return newX264(w, h, fps)
	default:
		return newDroid(w, h, fps, bps)
	}
}

const (
	minBps     = 250_000
	crispFloor = 1_500_000
	// frameBudget is the CBR target per frame. ~6KB (the old 1.5 Mbps cap
	// at 30fps) smeared map lines and type. ~20KB (4.8 Mbps at 30fps) keeps
	// that detail. A 720p hardware IDR at 1.5 Mbps was ~31KB, about 5× the
	// old frame, so this lands a typical IDR near 100KB. A larger picture is
	// still sent: USB fragments it, and reopening the encoder to shrink it
	// killed Codec2. Software x264 stays CQP and ignores this.
	frameBudget = 20_000
)

func hwBitrate(w, h, fps, hintKBPS int) int {
	_ = w
	_ = h
	if fps < 1 {
		fps = 30
	}
	bps := frameBudget * 8 * fps
	if bps < crispFloor {
		bps = crispFloor
	}
	if bps > 6_000_000 {
		bps = 6_000_000
	}
	// Legacy hint only raises. A stale 400 kbps call site must not undo the floor.
	if hint := hintKBPS * 1000; hintKBPS > 1000 && hint > bps {
		bps = hint
	}
	return bps
}

// clampBps keeps an explicit target inside what the hardware encoder and the
// link can actually do.
func clampBps(bps int) int {
	if bps < minBps {
		return minBps
	}
	if bps > 6_000_000 {
		return 6_000_000
	}
	return bps
}

// stepDown drops one quarter, but not below floor (and never below minBps).
func stepDown(bps, floor int) int {
	if bps <= 0 {
		return bps
	}
	if floor < minBps {
		floor = minBps
	}
	next := bps * 3 / 4
	if next < floor {
		next = floor
	}
	if next >= bps {
		return bps
	}
	return next
}

// stepUp raises one third, but not above ceiling.
func stepUp(bps, ceiling int) int {
	if bps <= 0 || ceiling <= bps {
		return bps
	}
	next := bps * 4 / 3
	if next > ceiling || next <= bps {
		next = ceiling
	}
	if next <= bps {
		return bps
	}
	return next
}
