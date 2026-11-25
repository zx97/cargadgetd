// Package livevid encodes shell-painted RGBA frames to Annex-B H.264.
// Hardware encode (Sailfish droidmedia). video.encoder=software selects x264.
package livevid

import (
	"fmt"
	"image"
	"image/color"
	"log"
	"sync"
	"time"

	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/math/fixed"
)

const (
	DefaultWidth  = 1280
	DefaultHeight = 720
	// comfortAU is a frame small enough that the pipe can take a higher rate.
	comfortAU = 48 * 1024
	// bitrateCooldown spaces CBR changes so a choke does not walk the
	// rate down in one burst, and a climb does not reopen Codec2 in a loop.
	bitrateCooldown = 8 * time.Second
)

// climbAfter is how many small frames must pass before CBR steps back up.
var climbAfter = 120

// openCodecHook, when set, replaces openCodec. Tests use it.
var openCodecHook func(w, h, fps, bps int, want string) (codec, error)

// Renderer encodes live RGBA frames to Annex-B access units.
type Renderer struct {
	mu         sync.Mutex
	enc        codec
	want       string
	rgba       *image.RGBA
	w, h       int
	fps        int
	bitrate    int
	forceIDR   bool
	paint      func(*image.RGBA, time.Time)
	bps        int // live CBR target
	ceiling    int // highest bitrate this session may return to; starts at the open rate
	lastChange time.Time
	climbedAt  time.Time
	chokeStart time.Time
	chokeN     int
	healthy    int

	// abandoned holds encoders that stopped answering. They are never Closed:
	// the call inside the codec may still be running, and droid_close on a
	// wedged Codec2 blocks as well.
	abandoned  map[codec]bool
	recoveries int
	// fatalErr latches the reason the encoder gave up, so every later frame
	// reports it instead of a generic "closed".
	fatalErr error
}

// Options configure the live encoder.
type Options struct {
	Width  int // default 1280
	Height int // default 720
	FPS    int // default 10
	// Bitrate is a kbps hint for hardware CBR. At or below 1000 kbps it
	// leaves the USB frame budget (~20KB/frame). A higher hint only raises.
	Bitrate int
	// TargetBps, when > 0, replaces both the frame-budget target and the
	// ceiling: the operator asked for this exact CBR rate (video.bitrate_kbps).
	// A car whose decoder reboots under a heavy stream is the reason it exists.
	TargetBps int
	Paint     func(*image.RGBA, time.Time)
	// Encoder is "droidmedia" (Sailfish HAL) or "software" (x264).
	Encoder string
}

// New creates a baseline / zerolatency encoder at opts.Width×opts.Height.
func New(opts Options) (*Renderer, error) {
	if opts.FPS <= 0 {
		opts.FPS = 10
	}
	if opts.Bitrate <= 0 {
		opts.Bitrate = 400
	}
	if opts.Width <= 0 {
		opts.Width = DefaultWidth
	}
	if opts.Height <= 0 {
		opts.Height = DefaultHeight
	}
	bps := hwBitrate(opts.Width, opts.Height, opts.FPS, opts.Bitrate)
	if opts.TargetBps > 0 {
		bps = clampBps(opts.TargetBps)
	}
	r := &Renderer{
		rgba:    image.NewRGBA(image.Rect(0, 0, opts.Width, opts.Height)),
		w:       opts.Width,
		h:       opts.Height,
		fps:     opts.FPS,
		bitrate: opts.Bitrate,
		bps:     bps,
		paint:   opts.Paint,
		want:    opts.Encoder,
	}
	r.ceiling = r.bps
	r.abandoned = map[codec]bool{}
	if err := r.openEncoder(); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *Renderer) openEncoder() error {
	if r.enc != nil {
		_ = r.enc.Close()
		r.enc = nil
	}
	var enc codec
	var err error
	if openCodecHook != nil {
		enc, err = openCodecHook(r.w, r.h, r.fps, r.bps, r.want)
	} else {
		enc, err = openCodec(r.w, r.h, r.fps, r.bps, r.want)
	}
	if err != nil {
		return err
	}
	r.enc = enc
	return nil
}

// Close frees the encoder. An abandoned (wedged) encoder is skipped: calling
// Close on it would block the same way the frame did.
func (r *Renderer) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.enc == nil {
		return nil
	}
	err := r.enc.Close()
	r.enc = nil
	return err
}

// ForceIDR makes the next Encode start a fresh IDR (reopens the encoder).
func (r *Renderer) ForceIDR() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.forceIDR = true
}

// RequestIDR asks for a keyframe on the next Encode without reopening the
// encoder when the codec allows it (hardware Codec2 does, via the input sync
// flag; software x264 keeps its own GOP).
//
// A stream that carries a single IDR at its start leaves a hardware decoder
// that lost its reference — one dropped frame, one resync — waiting for ever:
// the picture freezes and the head unit stops consuming the channel. The
// official phone sends a keyframe every couple of seconds.
func (r *Renderer) RequestIDR() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.enc == nil {
		return
	}
	if ir, ok := r.enc.(interface{ IDR() }); ok {
		ir.IDR()
		return
	}
	r.forceIDR = true
}

// Encode paints now, when a painter is set, and returns one Annex-B AU.
// A nil painter leaves the RGBA buffer as it is (black until something paints).
// Only call when the AU will be sent — encoding without send desyncs the HU.
func (r *Renderer) Encode(now time.Time) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.forceIDR {
		r.forceIDR = false
		if err := r.openEncoder(); err != nil {
			return nil, err
		}
	}
	if r.fatalErr != nil {
		return nil, r.fatalErr
	}
	if r.enc == nil {
		return nil, fmt.Errorf("livevid: closed")
	}
	if r.paint != nil {
		r.paint(r.rgba, now)
	}
	return r.encodeLocked()
}

// EncodeStall is how long one codec call may run before the encoder is
// declared wedged. Codec2 occasionally stops returning from the push, and the
// old code simply waited forever: no frame, no ack, no session, no log.
var EncodeStall = 2 * time.Second

// MaxEncodeRecovery bounds how many encoders are rebuilt per session. Past
// that the hardware is not coming back and the session should restart.
const MaxEncodeRecovery = 3

// encodeLocked runs the codec call with a deadline. On a stall the encoder is
// abandoned, a fresh one is created, and the frame is dropped: the next frame
// is a keyframe from the new encoder, so the picture comes back instead of
// freezing until the session is torn down.
func (r *Renderer) encodeLocked() ([]byte, error) {
	if r.fatalErr != nil {
		return nil, r.fatalErr
	}
	if r.enc == nil {
		return nil, fmt.Errorf("livevid: closed")
	}
	enc := r.enc
	type result struct {
		au  []byte
		err error
	}
	done := make(chan result, 1)
	go func() {
		au, err := enc.Encode(r.rgba)
		done <- result{au, err}
	}()
	select {
	case out := <-done:
		if out.err != nil {
			return nil, out.err
		}
		return r.acceptLocked(out.au)
	case <-time.After(EncodeStall):
		r.recoveries++
		r.abandoned[enc] = true
		r.enc = nil
		log.Printf("livevid: encoder stalled for %s — abandoning it (%d/%d)", EncodeStall, r.recoveries, MaxEncodeRecovery)
		if r.recoveries > MaxEncodeRecovery {
			r.fatalErr = fmt.Errorf("livevid: encoder stalled %d times, ending the session", r.recoveries)
			return nil, r.fatalErr
		}
		// The codec lives in the Android media service, not in this process:
		// abandoning it keeps the slot busy there. Try to release it anyway,
		// detached, because droid_close blocks on the same wedge.
		r.releaseAbandoned(enc)
		if err := r.openEncoder(); err != nil {
			return nil, fmt.Errorf("livevid: encoder rebuild: %w (restart the phone if it stays unavailable: the media service has no free codec)", err)
		}
		return nil, fmt.Errorf("livevid: encoder stalled, rebuilt")
	}
}

// releaseAbandoned asks the wedged encoder to close on a detached goroutine.
// droid_close goes through the same wedged pipeline, so it may never return;
// the goroutine is left behind rather than blocking the video loop. When it
// does return the media service gets its codec slot back, which is what keeps
// the next create from failing.
func (r *Renderer) releaseAbandoned(enc codec) {
	if enc == nil {
		return
	}
	go func() {
		done := make(chan struct{})
		go func() {
			_ = enc.Close()
			close(done)
		}()
		select {
		case <-done:
			log.Printf("livevid: abandoned encoder released")
		case <-time.After(5 * time.Second):
			log.Printf("livevid: abandoned encoder did not close — its codec slot stays taken")
		}
	}()
}

// acceptLocked validates the access unit and hands it back.
func (r *Renderer) acceptLocked(au []byte) ([]byte, error) {
	if len(au) == 0 {
		return nil, fmt.Errorf("livevid: empty AU")
	}
	// Send the picture as-is. writeMedia already splits it into 12KB USB
	// records. Cutting CBR and reopening Codec2 to squeeze a fat frame did
	// not shrink the next one, and the create killed JP2026 inside droid create.
	r.considerClimbLocked(len(au))
	return au, nil
}

// Bitrate is the live CBR target in bits per second.
func (r *Renderer) Bitrate() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.bps
}

// NotePipeSlow records a choked USB video write or an abandoned MediaAck.
// Two reports inside a few seconds step CBR down one quarter, not below the
// crisp floor: a silent head unit is not a reason to potato the picture.
// The step is applied on the next ForceIDR / encode reopen.
func (r *Renderer) NotePipeSlow() {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	if r.chokeStart.IsZero() || now.Sub(r.chokeStart) > 3*time.Second {
		r.chokeStart = now
		r.chokeN = 0
	}
	r.chokeN++
	if r.chokeN < 2 {
		return
	}
	r.chokeN = 0
	r.chokeStart = time.Time{}
	if now.Sub(r.lastChange) < bitrateCooldown {
		return
	}
	next := stepDown(r.bps, crispFloor)
	if next == r.bps {
		return
	}
	r.applyDownLocked(next, "pipe slow")
	r.forceIDR = true
}

func (r *Renderer) applyDownLocked(next int, why string) {
	if next == r.bps || next <= 0 {
		return
	}
	old := r.bps
	r.noteCutLocked(next)
	r.bps = next
	r.lastChange = time.Now()
	r.healthy = 0
	log.Printf("livevid: %s — bitrate %d→%d", why, old, next)
}

// noteCutLocked pins the climb ceiling when a rate we just raised does not fit.
func (r *Renderer) noteCutLocked(next int) {
	if r.climbedAt.IsZero() || time.Since(r.climbedAt) >= 3*time.Second {
		return
	}
	if next < r.ceiling {
		r.ceiling = next
		log.Printf("livevid: bitrate ceiling %d", r.ceiling)
	}
	r.climbedAt = time.Time{}
}

func (r *Renderer) considerClimbLocked(n int) {
	if r.bps >= r.ceiling || n > comfortAU {
		if n > comfortAU {
			r.healthy = 0
		}
		return
	}
	r.healthy++
	need := climbAfter
	if need < 1 {
		need = 120
	}
	if r.healthy < need || time.Since(r.lastChange) < bitrateCooldown {
		return
	}
	next := stepUp(r.bps, r.ceiling)
	if next == r.bps {
		r.healthy = 0
		return
	}
	old := r.bps
	r.bps = next
	r.lastChange = time.Now()
	r.climbedAt = r.lastChange
	r.healthy = 0
	r.forceIDR = true
	log.Printf("livevid: pipe clear — bitrate %d→%d", old, next)
}

func (r *Renderer) Size() (int, int) { return r.w, r.h }

// EncoderName is the active backend (droidmedia-hw or x264-sw).
func (r *Renderer) EncoderName() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.enc == nil {
		return ""
	}
	return r.enc.Name()
}

// DrawString paints s onto dst. The shell uses it for the init line.
func DrawString(dst *image.RGBA, s string, cx, cy, scale int, col color.Color, center bool) {
	if scale < 1 {
		scale = 1
	}
	face := basicfont.Face7x13
	advance := font.MeasureString(face, s).Ceil() * scale
	height := face.Height * scale
	x := cx
	y := cy
	if center {
		x = cx - advance/2
		y = cy - height/2
	}
	tmp := image.NewRGBA(image.Rect(0, 0, advance/scale+2, face.Height+2))
	d := &font.Drawer{
		Dst:  tmp,
		Src:  image.NewUniform(col),
		Face: face,
		Dot:  fixed.P(1, face.Ascent),
	}
	d.DrawString(s)
	for py := 0; py < tmp.Bounds().Dy(); py++ {
		for px := 0; px < tmp.Bounds().Dx(); px++ {
			_, _, _, a := tmp.At(px, py).RGBA()
			if a == 0 {
				continue
			}
			for sy := 0; sy < scale; sy++ {
				for sx := 0; sx < scale; sx++ {
					dst.Set(x+px*scale+sx, y+py*scale+sy, col)
				}
			}
		}
	}
}
