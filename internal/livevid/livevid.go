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
}

// Options configure the live encoder.
type Options struct {
	Width  int // default 1280
	Height int // default 720
	FPS    int // default 10
	// Bitrate is a kbps hint for hardware CBR. At or below 1000 kbps it
	// leaves the USB frame budget (~20KB/frame). A higher hint only raises.
	Bitrate int
	Paint   func(*image.RGBA, time.Time)
	// Encoder is "droidmedia" (Sailfish HAL), "software" (x264), or "software-fast".
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
	r := &Renderer{
		rgba:    image.NewRGBA(image.Rect(0, 0, opts.Width, opts.Height)),
		w:       opts.Width,
		h:       opts.Height,
		fps:     opts.FPS,
		bitrate: opts.Bitrate,
		bps:     hwBitrate(opts.Width, opts.Height, opts.FPS, opts.Bitrate),
		paint:   opts.Paint,
		want:    opts.Encoder,
	}
	r.ceiling = r.bps
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

// Close frees the encoder.
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

// CodecConfig returns the H.264 parameter sets (SPS/PPS, Annex-B) the encoder
// produced, or nil when the codec keeps them to itself.
//
// The official phone sends them as their own CODEC_CONFIG message instead of
// relying on them being in-band: a head unit that reads that message and gets
// nothing never learns the picture size and acks frames it never displays.
func (r *Renderer) CodecConfig() []byte {
	r.mu.Lock()
	enc := r.enc
	r.mu.Unlock()
	if enc == nil {
		return nil
	}
	if c, ok := enc.(interface{ CodecConfig() []byte }); ok {
		return c.CodecConfig()
	}
	return nil
}

// SetPaint replaces the frame painter. The painter captures the shell and its
// session, so it changes with every session while the encoder can stay.
func (r *Renderer) SetPaint(f func(*image.RGBA, time.Time)) {
	r.mu.Lock()
	r.paint = f
	r.mu.Unlock()
}

// ForceIDR makes the next Encode start a fresh IDR (reopens the encoder).
func (r *Renderer) ForceIDR() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.forceIDR = true
}

// Encode paints now, when a painter is set, and returns one Annex-B AU.
// A nil painter leaves the RGBA buffer as it is (black until something paints).
// Only call when the AU will be sent - encoding without send desyncs the HU.
func (r *Renderer) Encode(now time.Time) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.forceIDR {
		r.forceIDR = false
		if err := r.openEncoder(); err != nil {
			return nil, err
		}
	}
	if r.enc == nil {
		return nil, fmt.Errorf("livevid: closed")
	}
	if r.paint != nil {
		r.paint(r.rgba, now)
	}
	return r.encodeLocked()
}

func (r *Renderer) encodeLocked() ([]byte, error) {
	au, err := r.enc.Encode(r.rgba)
	if err != nil {
		return nil, err
	}
	if len(au) == 0 {
		// Hardware keeps the first frame in flight so the next convert
		// overlaps the codec. Nothing to send yet.
		return nil, nil
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
	log.Printf("livevid: %s - bitrate %d→%d", why, old, next)
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
	log.Printf("livevid: pipe clear - bitrate %d→%d", old, next)
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
