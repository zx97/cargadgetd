package main

import (
	"log"
	"time"

	"github.com/cargadgetd/cargadgetd/internal/livevid"
)

// warmEncoder creates the hardware encoder, encodes two frames and closes it,
// once, at daemon start-up.
//
// The vendor encoder can take the whole process down inside Codec2 while it is
// being created — the VW run died there with no line of ours in the log, in the
// middle of a projection (2026-11-26 09:19). Doing the same create/encode/close
// before any car is connected gives that failure a place to happen where it
// costs nothing: the supervisor restarts the daemon, and the session that
// follows starts with the encoder already proven and the HAL warm.
//
// Failure here is not fatal: a phone without a hardware encoder falls back to
// the software one inside livevid, exactly as before.
func warmEncoder(w, h, fps, kbps int, encoder string) {
	if w <= 0 || h <= 0 {
		return
	}
	if encoder == "" {
		encoder = "droidmedia"
	}
	start := time.Now()
	r, err := livevid.New(livevid.Options{
		Width:   w,
		Height:  h,
		FPS:     fps,
		Bitrate: kbps,
		Encoder: encoder,
	})
	if err != nil {
		log.Printf("video warm-up: %v (the session will try again)", err)
		return
	}
	for i := 0; i < 2; i++ {
		if _, err := r.Encode(time.Now()); err != nil {
			log.Printf("video warm-up: encode %d: %v", i+1, err)
			break
		}
	}
	if err := r.Close(); err != nil {
		log.Printf("video warm-up: close: %v", err)
	}
	log.Printf("video warm-up: encoder ready in %s (%dx%d @%dfps)",
		time.Since(start).Round(time.Millisecond), w, h, fps)
}
