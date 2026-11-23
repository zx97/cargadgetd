// encprobe encodes a moving pattern for a while and reports every stall.
//
// It reproduces the video-encoder freeze without a head unit, a cable or a
// car: the wedge we chase happens inside Codec2/droidmedia, so it can be
// studied from an ssh session. Run it on the phone:
//
//	encprobe -seconds 600 -size 1280x720 -kbps 1500
//
// Lines worth watching: "stalled", "released", "rebuilt".
package main

import (
	"flag"
	"fmt"
	"image"
	"image/color"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/cargadgetd/cargadgetd/internal/livevid"
)

func main() {
	seconds := flag.Int("seconds", 600, "how long to encode")
	fps := flag.Int("fps", 30, "frames per second")
	size := flag.String("size", "1280x720", "encoder size, WxH")
	kbps := flag.Int("kbps", 1500, "CBR target")
	encoder := flag.String("encoder", "droidmedia", "droidmedia or software")
	flag.Parse()

	var w, h int
	if _, err := fmt.Sscanf(*size, "%dx%d", &w, &h); err != nil || w <= 0 || h <= 0 {
		log.Fatalf("bad -size %q", *size)
	}

	// A pattern that changes every frame, like a scrolled map: a still picture
	// lets the encoder coast on P-frames and would not exercise the pipeline
	// the way the shell does.
	paint := func(dst *image.RGBA, now time.Time) {
		cw, ch := dst.Rect.Dx(), dst.Rect.Dy()
		shift := int(now.UnixNano()/1e6) % cw
		for y := 0; y < ch; y++ {
			row := dst.Pix[y*dst.Stride : y*dst.Stride+cw*4]
			for x := 0; x < cw; x++ {
				v := uint8((x + y + shift) & 0xff)
				row[x*4+0] = v
				row[x*4+1] = v / 2
				row[x*4+2] = 255 - v
				row[x*4+3] = 255
			}
		}
		// A few solid blocks so the encoder has flat areas too.
		for i := 0; i < 6; i++ {
			x0 := (shift*3 + i*97) % (cw - 80)
			y0 := (i * ch / 7) % (ch - 40)
			for y := y0; y < y0+40 && y < ch; y++ {
				for x := x0; x < x0+80 && x < cw; x++ {
					dst.SetRGBA(x, y, color.RGBA{R: 0x20, G: 0xe0, B: 0x60, A: 255})
				}
			}
		}
	}

	live, err := livevid.New(livevid.Options{
		Width: w, Height: h, FPS: *fps,
		TargetBps: *kbps * 1000,
		Paint:     paint,
		Encoder:   *encoder,
	})
	if err != nil {
		log.Fatalf("open encoder: %v", err)
	}
	defer live.Close()
	log.Printf("encprobe: %dx%d @%dfps %dkbps encoder=%s for %s", w, h, *fps, *kbps, live.EncoderName(), time.Duration(*seconds)*time.Second)

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)

	period := time.Second / time.Duration(*fps)
	deadline := time.Now().Add(time.Duration(*seconds) * time.Second)
	var frames, bytes, stalls int
	var maxAU int
	start := time.Now()
	lastReport := start

	ticker := time.NewTicker(period)
	defer ticker.Stop()
	for time.Now().Before(deadline) {
		select {
		case <-stop:
			log.Printf("encprobe: stopped by signal")
			return
		case <-ticker.C:
		}
		t0 := time.Now()
		au, err := live.Encode(t0)
		if err != nil {
			stalls++
			log.Printf("encprobe: frame error after %s: %v (total %d)", time.Since(t0).Round(time.Millisecond), err, stalls)
			continue
		}
		frames++
		bytes += len(au)
		if len(au) > maxAU {
			maxAU = len(au)
		}
		if time.Since(lastReport) >= 10*time.Second {
			el := time.Since(start).Seconds()
			log.Printf("encprobe: %d frames in %.0fs (%.1f fps), %.2f Mbps, max AU %dB, errors %d",
				frames, el, float64(frames)/el, float64(bytes)*8/el/1e6, maxAU, stalls)
			lastReport = time.Now()
		}
	}
	log.Printf("encprobe: done: %d frames, %d errors, max AU %dB — the encoder %s",
		frames, stalls, maxAU, map[bool]string{true: "survived", false: "did NOT survive"}[stalls == 0])
}
