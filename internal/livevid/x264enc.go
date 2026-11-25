package livevid

import (
	"bytes"
	"fmt"
	"image"

	x264 "github.com/gen2brain/x264-go"
)

type x264Enc struct {
	enc *x264.Encoder
	buf bytes.Buffer
}

func newX264(w, h, fps int) (codec, error) {
	e := &x264Enc{}
	enc, err := x264.NewEncoder(&e.buf, &x264.Options{
		Width:     w,
		Height:    h,
		FrameRate: fps,
		Tune:      "zerolatency",
		Preset:    "ultrafast",
		Profile:   "baseline",
		// A four-second GOP. x264-go has no "keyframe now" call, so this is the
		// software codec's only bound on how long a decoder that lost its
		// reference stays frozen; the hardware path asks for one directly
		// (Renderer.RequestIDR → the input sync flag).
		KeyInt:       fps * 4,
		IntraRefresh: false,
		RateControl:  "cqp",
		RateConstant: 18, // larger loop P (~300–500B); tiny 94B+PCM died ~20s on Xperia
		LogLevel:     x264.LogError,
	})
	if err != nil {
		return nil, err
	}
	e.buf.Reset()
	e.enc = enc
	return e, nil
}

func (e *x264Enc) Name() string { return "x264-sw" }

// IDR is a no-op: x264 honours its own GOP (fps*2), and forcing a keyframe here
// would reopen the encoder for every request.
func (e *x264Enc) IDR() {}

func (e *x264Enc) Encode(img *image.RGBA) ([]byte, error) {
	e.buf.Reset()
	if err := e.enc.Encode(img); err != nil {
		return nil, err
	}
	b := e.buf.Bytes()
	if len(b) == 0 {
		return nil, fmt.Errorf("livevid: empty AU")
	}
	out := make([]byte, len(b))
	copy(out, b)
	return out, nil
}

func (e *x264Enc) Close() error {
	if e.enc == nil {
		return nil
	}
	err := e.enc.Close()
	e.enc = nil
	return err
}
