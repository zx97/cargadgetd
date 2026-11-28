package session

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cargadgetd/cargadgetd/internal/cfg"
	"github.com/cargadgetd/cargadgetd/internal/frame"
	aassl "github.com/cargadgetd/cargadgetd/internal/ssl"
)

// Control message types (channel 0).
const (
	MsgVersionRequest           = 0x0001
	MsgVersionResponse          = 0x0002
	MsgSslHandshake             = 0x0003
	MsgAuthComplete             = 0x0004
	MsgServiceDiscoveryRequest  = 0x0005
	MsgServiceDiscoveryResponse = 0x0006
	MsgChannelOpenRequest       = 0x0007
	MsgChannelOpenResponse      = 0x0008
	MsgPingRequest              = 0x000b
	MsgPingResponse             = 0x000c
	MsgNavigationFocusRequest   = 0x000d
	MsgNavigationFocusResponse  = 0x000e
	MsgVoiceSessionRequest      = 0x0011
	MsgAudioFocusRequest        = 0x0012
	MsgAudioFocusResponse       = 0x0013

	MsgChannelCloseNotification    = 0x0009
	MsgByeByeRequest               = 0x000f
	MsgByeByeResponse              = 0x0010
	MsgCarConnectedDevicesRequest  = 0x0014
	MsgCarConnectedDevicesResponse = 0x0015
	MsgUserSwitchRequest           = 0x0016
	MsgBatteryStatusNotification   = 0x0017
	MsgCallAvailabilityStatus      = 0x0018
	MsgUserSwitchResponse          = 0x0019
	MsgServiceDiscoveryUpdate      = 0x001a
	MsgUnexpectedMessage           = 0x00ff
	MsgInternalError               = 0x0fff
	MsgHardShutdown                = 0xdead
)

// Media / input (channel-specific).
const (
	// 0x0000 carries an 8-byte timestamp then the payload; 0x0001 carries
	// parameter sets only. "MediaIndication" was a wrong name for 0x0001, and a
	// frame sent with it reached the head unit as if it were SPS/PPS.
	MediaWithTimestamp = 0x0000
	MediaCodecConfig   = 0x0001
	MediaSetupRequest  = 0x8000
	MediaStartInd      = 0x8001
	MediaStopInd       = 0x8002
	MediaSetupResponse = 0x8003
	MediaAck           = 0x8004
	VideoFocusInd      = 0x8008

	MediaMicRequest               = 0x8005
	MediaMicResponse              = 0x8006
	VideoFocusRequest             = 0x8007
	MediaUpdateUIConfigRequest    = 0x8009
	MediaUpdateUIConfigReply      = 0x800a
	MediaActionTaken              = 0x800c
	MediaOverlayParams            = 0x800d
	MediaOverlayStart             = 0x800e
	MediaOverlayStop              = 0x800f
	MediaOverlaySession           = 0x8010
	MediaUpdateHUUIConfigRequest  = 0x8011
	MediaUpdateHUUIConfigResponse = 0x8012
	MediaStats                    = 0x8013
	MediaOptions                  = 0x8014
	MediaCriticalUI               = 0x8015

	SensorStartRequest  = 0x8001
	SensorStartResponse = 0x8002
	SensorEvent         = 0x8003

	AudioUnderflow = 0x800b

	InputEvent             = 0x8001
	InputHandshakeRequest  = 0x8002
	InputHandshakeResponse = 0x8003
)

const (
	ChanSensor = 1
	ChanVideo  = 2
	ChanInput  = 3
	ChanAudio  = 4
)

const MaxUSBAudioInFlight int32 = 2

type Event struct {
	Type    string
	Channel uint8
	Payload []byte
}

type MD struct {
	rw  io.ReadWriter
	ssl *aassl.Session
	log *log.Logger
	mu  sync.Mutex

	VideoCh      uint8
	InputCh      uint8
	AudioCh      uint8
	SensorCh     uint8
	videoW       int
	videoH       int
	videoMW      int // total margin_width (both sides)
	videoMH      int
	videoCfgIdx  int // StartIndication config_index (matches video_configs[])
	videoCfgs    []videoCfg
	audioCfgs    []audioCfg
	audioType    int
	audioRate    int
	audioChans   int
	audioCfgIdx  int
	inputW       int
	inputH       int
	inputButtons []int
	sensorTypes  []int

	// side is every advertised service we do not drive (bluetooth, navigation,
	// the audio sinks we did not pick, ...) so messages can be logged.
	side       []sideChannel
	sideOpened map[uint8]struct{}

	Events   chan Event
	ready    chan struct{}
	done     chan struct{} // closed when the session ends; stops the writer
	doneOnce sync.Once
	errc     chan error

	openWait  map[uint8]chan struct{}
	setupWait map[uint8]chan struct{}
	videoGo   chan struct{} // closed when StartIndication has been sent
	started   bool

	sendQ  chan outMsg // video / other bulk
	ctrlQ  chan outMsg // control replies - writer prefers these (pings!)
	audioQ chan outMsg // constant PCM - between ctrl and video so large IDRs cannot starve the sink

	// usbBulk: FunctionFS transport - larger TLS-safe chunks + inter-fragment pause.
	usbBulk bool

	// pingNanos is how often the phone emits its own PingRequests (0 = never).
	// The official Android Auto pings on wireless links only, about once a
	// second, and the answer carries the round trip.
	pingNanos atomic.Int64
	pingRTT   atomic.Int64 // last round trip, ns
	pingRTTN  atomic.Int64 // answered pings so far

	// videoCredit wakes ReserveVideoCredit when a MediaAck arrives.
	// videoInFlight caps unacked video AUs on USB (max 1).
	// videoUnackedSince is when that AU's USB write finished (0 if none).
	videoCredit       chan struct{}
	videoInFlight     atomic.Int32
	videoUnackedSince atomic.Int64
	// audioCredit / audioInFlight: PCM had no flow control while video was
	// credit-gated - capture showed DHU OUT dying right after a 9.6KiB audio
	// URB, then our video-hold idled the pipe. Gate audio the same way.
	// Cap is MaxUSBAudioInFlight (2): MediaAck RTT is often ~70–80ms, so a
	// single outstanding 42ms chunk made the FIFO play at ~0.5×.
	audioCredit      chan struct{}
	audioInFlight    atomic.Int32
	ackLogN          atomic.Uint64
	audioAckLogN     atomic.Uint64
	touchLogN        atomic.Uint64
	txLogN           atomic.Uint64
	videoTxN         atomic.Uint64
	lastVideoAckUnix atomic.Int64
	// videoStreaming: false after MediaStop or native video focus (mode 2/3)
	// until a projected VideoFocus (mode 1 or 4) sends StartInd.
	videoStreaming atomic.Bool
	// audioFocusOK: closed once when AudioFocusResponse grants focus.
	audioFocusOK    chan struct{}
	audioFocusOnce  sync.Once
	audioFocusGrant atomic.Bool

	audioUnderflowN atomic.Uint64

	protoMaj uint16
	protoMin uint16
}

type outMsg struct {
	channel, flags uint8
	payload        []byte
	done           chan error
	// drop is set when the enqueuer timed out; writer must not put it on the wire
	// (otherwise abandoned frames pile up in sendQ → USB flood → BAD_DECRYPT).
	drop *atomic.Bool
}

func NewMD(rw io.ReadWriter, ssl *aassl.Session, logger *log.Logger) *MD {
	if logger == nil {
		logger = log.Default()
	}
	return &MD{
		rw:           rw,
		ssl:          ssl,
		log:          logger,
		Events:       make(chan Event, 64),
		ready:        make(chan struct{}),
		done:         make(chan struct{}),
		errc:         make(chan error, 1),
		openWait:     make(map[uint8]chan struct{}),
		setupWait:    make(map[uint8]chan struct{}),
		videoGo:      make(chan struct{}),
		sendQ:        make(chan outMsg, 2), // shallow - USB backpressure
		ctrlQ:        make(chan outMsg, 128),
		audioQ:       make(chan outMsg, MaxUSBAudioInFlight), // queued PCM while AIO writes
		videoCredit:  make(chan struct{}, 1),
		audioCredit:  make(chan struct{}, MaxUSBAudioInFlight),
		audioFocusOK: make(chan struct{}),
		VideoCh:      ChanVideo,
		InputCh:      ChanInput,
		AudioCh:      ChanAudio,
		SensorCh:     0,
	}
}

// SetUSBBulk enables USB FunctionFS-oriented media framing (TLS-safe chunk size).
func (m *MD) SetUSBBulk(v bool) {
	m.usbBulk = v
}

// Run drives the MD side until the connection fails.
func (m *MD) Run() error {
	go m.writer()
	go m.pingLoop()
	err := m.reader()
	m.doneOnce.Do(func() { close(m.done) }) // stop writer - one leaked per session otherwise
	return err
}

// EnablePings makes the phone send its own PingRequests at the given period;
// zero (the default) keeps it silent and only answering the head unit's. The
// official Android Auto does this on wireless links and not on USB.
func (m *MD) EnablePings(every time.Duration) {
	if every < 0 {
		every = 0
	}
	m.pingNanos.Store(int64(every))
}

// pingLoop emits PingRequests while EnablePings asked for them. It never runs
// before the session is up: a ping is an encrypted frame, and sending one while
// the reader is still feeding the TLS handshake leaves it half-negotiated.
func (m *MD) pingLoop() {
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	var last time.Time
	for {
		select {
		case <-m.done:
			return
		case <-tick.C:
		}
		every := time.Duration(m.pingNanos.Load())
		if every <= 0 {
			continue
		}
		select {
		case <-m.ready:
		default:
			continue
		}
		if !last.IsZero() && time.Since(last) < every {
			continue
		}
		last = time.Now()
		// Control.PingRequest: field 1 = timestamp (int64), in nanoseconds.
		body := appendVarint([]byte{0x08}, uint64(last.UnixNano()))
		p := frame.PutU16(nil, MsgPingRequest)
		p = append(p, body...)
		m.enqueueAsync(0, frame.FlagBulk|frame.FlagEncrypted, p)
	}
}

func (m *MD) writer() {
	for {
		select {
		case <-m.done:
			return // session over - do NOT drain queues against a closing SSL/transport
		default:
		}
		// Pings always first. Prefer video over audio so 25/s PCM cannot bury
		// 10fps H.264 (fair select starved video under load on Xperia).
		m.serviceCtrl()
		select {
		case msg := <-m.ctrlQ:
			m.writeOut(msg)
		default:
			select {
			case <-m.done:
				return
			case msg := <-m.ctrlQ:
				m.writeOut(msg)
			case msg := <-m.sendQ:
				m.writeMedia(msg)
				m.serviceCtrl()
			default:
				select {
				case <-m.done:
					return
				case msg := <-m.ctrlQ:
					m.writeOut(msg)
				case msg := <-m.sendQ:
					m.writeMedia(msg)
					m.serviceCtrl()
				case msg := <-m.audioQ:
					m.writeOut(msg)
					m.serviceCtrl()
				}
			}
		}
	}
}

// serviceCtrl drains pending control frames (pings) without blocking.
func (m *MD) serviceCtrl() {
	for {
		select {
		case msg := <-m.ctrlQ:
			m.writeOut(msg)
		default:
			return
		}
	}
}

// serviceAudioOne writes at most one pending PCM frame (used between video fragments).
func (m *MD) serviceAudioOne() {
	select {
	case msg := <-m.audioQ:
		m.writeOut(msg)
	default:
	}
}

// mediaChannels returns the video and audio channel ids parsed from the SDR.
// Run starts the writer before the reader parses the SDR, so a read from the
// writer goroutine can overlap the SDR writes. Take m.mu to pair with them.
// Callers that run after WaitReady already have the happens-before edge from
// the closed ready channel and may read the fields directly.
func (m *MD) mediaChannels() (video, audio uint8) {
	m.mu.Lock()
	video, audio = m.VideoCh, m.AudioCh
	m.mu.Unlock()
	return
}

func (m *MD) writeOut(msg outMsg) {
	// Snapshot the audio channel: the SDR may still be parsing on the reader
	// goroutine while the writer services pings/handshake frames.
	_, audioCh := m.mediaChannels()
	// USB media (incl. PCM) must use AIO+yield. Ctrl/pongs stay on the raw
	// writer so a nested sync pong during media AIO can still land; switching
	// pongs to AIO regressed video-only soaks (2026-08-12).
	w := io.Writer(m.rw)
	if m.usbBulk && msg.channel != 0 {
		if eps, ok := m.rw.(yieldWriter); ok {
			w = &aioYieldWriter{e: eps, yield: m.serviceCtrl}
		} else {
			w = m.preemptW()
		}
	}
	var err error
	writeOnce := func() error {
		if msg.flags&frame.FlagEncrypted != 0 {
			chunk := frame.MaxPlainChunk
			if m.usbBulk {
				chunk = frame.MaxPlainChunkUSB
			}
			return frame.WriteEncryptedYield(w, msg.channel, msg.flags, msg.payload, m.ssl.Encrypt, nil, chunk)
		}
		return frame.Write(w, frame.Frame{Channel: msg.channel, Flags: msg.flags, Payload: msg.payload})
	}
	for i := 0; i < 2000; i++ {
		err = writeOnce()
		if !isUSBWriteBusy(err) {
			break
		}
		time.Sleep(1 * time.Millisecond)
	}
	if f, ok := m.rw.(interface{ Flush() error }); ok {
		_ = f.Flush()
	}
	m.logTX(msg.channel, msg.flags, len(msg.payload))
	if msg.channel == 0 && len(msg.payload) >= 2 {
		mt := binary.BigEndian.Uint16(msg.payload[:2])
		if mt == MsgPingResponse {
			m.log.Printf("pong_tx len=%d err=%v", len(msg.payload), err)
		}
	}
	if msg.done != nil {
		msg.done <- err
	}
	if err != nil && (msg.channel == 0 || !m.usbBulk) {
		if msg.channel != 0 {
			m.log.Printf("tx error: %v", err)
		}
		m.fail(err)
	} else if err != nil {
		m.log.Printf("media tx error (drop): %v", err)
		if m.usbBulk && msg.channel == audioCh && m.audioInFlight.Load() > 0 {
			m.audioInFlight.Add(-1)
		}
	}
	if m.usbBulk && msg.channel == audioCh && err == nil {
		m.serviceCtrl()
		// Brief gap after PCM so the HU can emit video MediaAck/pings on OUT
		// before the next IN URB (TCP A/V is green; USB needs breathing room).
		if gap := usbMediaGap(); gap > 0 {
			time.Sleep(gap)
		}
	}
}

func usbMediaGap() time.Duration {
	ms := cfg.Get().USB.MediaGapMs
	if ms <= 0 {
		return 0
	}
	return time.Duration(ms) * time.Millisecond
}

func (m *MD) logTX(channel, flags uint8, n int) {
	// Avoid flooding Sailfish journal with every media frame (was drowning
	// useful protocol lines and made soaks hard to read).
	if channel == 0 {
		m.log.Printf("tx ch=%d flags=%#x len=%d", channel, flags, n)
		return
	}
	c := m.txLogN.Add(1)
	if c <= 3 || c%50 == 0 {
		m.log.Printf("tx ch=%d flags=%#x len=%d (n=%d)", channel, flags, n, c)
	}
}

// writeMedia sends media, fragmenting so each AA frame is one TLS record.
func (m *MD) writeMedia(msg outMsg) {
	videoCh, audioCh := m.mediaChannels()
	if msg.drop != nil && msg.drop.Load() {
		if m.usbBulk && msg.channel == videoCh {
			if m.videoInFlight.Load() > 0 {
				m.videoInFlight.Add(-1)
			}
		}
		if m.usbBulk && msg.channel == audioCh {
			if m.audioInFlight.Load() > 0 {
				m.audioInFlight.Add(-1)
			}
		}
		if msg.done != nil {
			msg.done <- errSendCanceled
		}
		return
	}
	// Drain pings before starting a (possibly multi-fragment) media message.
	m.serviceCtrl()
	w := m.preemptW()
	if m.usbBulk {
		// While AIO media URB awaits the host, drain ctrl/pongs (same goroutine,
		// nested AIO wait - see ffs/aio_linux.go).
		if eps, ok := m.rw.(yieldWriter); ok {
			w = &aioYieldWriter{e: eps, yield: m.serviceCtrl}
		}
	}
	if msg.flags&frame.FlagEncrypted == 0 {
		m.writeOut(msg)
		m.serviceCtrl()
		return
	}
	chunk := frame.MaxPlainChunk
	if m.usbBulk {
		chunk = frame.MaxPlainChunkUSB
	}
	// On USB do not interleave other channels between First…Last (breaks HU
	// reassembly). On TCP, yield to pings/audio between fragments.
	var between func()
	if !m.usbBulk {
		between = func() {
			m.serviceCtrl()
			m.serviceAudioOne()
		}
	}
	err := frame.WriteEncryptedYield(w, msg.channel, msg.flags, msg.payload, m.ssl.Encrypt, between, chunk)
	if err != nil && !m.usbBulk {
		m.log.Printf("tx error: %v", err)
		m.logTX(msg.channel, msg.flags, len(msg.payload))
		if msg.done != nil {
			msg.done <- err
		}
		m.fail(err)
		return
	}
	if f, ok := m.rw.(interface{ Flush() error }); ok {
		_ = f.Flush()
	}
	m.serviceCtrl()
	// On USB, do not auto-flush audio after video (main uses an independent PCM ticker).
	if !m.usbBulk {
		m.serviceAudioOne()
	}
	if msg.drop != nil && msg.drop.Load() {
		err = errSendCanceled
	}
	m.logTX(msg.channel, msg.flags, len(msg.payload))
	if msg.done != nil {
		msg.done <- err
	}
	if err != nil && !errors.Is(err, errSendCanceled) {
		m.log.Printf("media tx error (drop): %v", err)
		if m.usbBulk && msg.channel == videoCh && m.videoInFlight.Load() > 0 {
			m.videoInFlight.Add(-1)
		}
		if m.usbBulk && msg.channel == audioCh && m.audioInFlight.Load() > 0 {
			m.audioInFlight.Add(-1)
		}
	}
}

// preemptW returns m.rw, or a wrapper that drains ctrlQ when FFS would block
// before any byte of the current Write is committed.
func (m *MD) preemptW() io.Writer {
	if !m.usbBulk {
		return m.rw
	}
	return &preemptWriter{m: m, w: m.rw}
}

// yieldWriter is the FunctionFS bulk writer. The session does not import that
// package; Endpoints already has this method.
type yieldWriter interface {
	WriteWithYield(p []byte, yield func()) (int, error)
}

// writeBusy is the "nothing committed, try again" error from that writer.
type writeBusy interface {
	USBWriteBusy()
}

func isUSBWriteBusy(err error) bool {
	var b writeBusy
	return errors.As(err, &b)
}

// endpointStopper unblocks a reader stuck in a bulk Read when the session ends.
type endpointStopper interface {
	Shutdown(error)
}

// aioYieldWriter routes Writes through FunctionFS AIO with a yield hook so
// pongs can be submitted while a media URB is in flight.
type aioYieldWriter struct {
	e     yieldWriter
	yield func()
}

func (w *aioYieldWriter) Write(p []byte) (int, error) {
	return w.e.WriteWithYield(p, w.yield)
}

type preemptWriter struct {
	m *MD
	w io.Writer
}

func (p *preemptWriter) Write(b []byte) (int, error) {
	off := 0
	deadline := time.Now().Add(800 * time.Millisecond)
	for off < len(b) {
		n, err := p.w.Write(b[off:])
		if n > 0 {
			off += n
			deadline = time.Now().Add(800 * time.Millisecond) // progress - extend
		}
		if err == nil {
			if n == 0 {
				return off, io.ErrShortWrite
			}
			continue
		}
		if isUSBWriteBusy(err) {
			if off == 0 && time.Now().After(deadline) {
				return 0, fmt.Errorf("ffs: write busy timeout")
			}
			// Nothing of this buffer is on the wire yet - pong freely, retry.
			p.m.serviceCtrl()
			time.Sleep(1 * time.Millisecond)
			continue
		}
		return off, err
	}
	return off, nil
}

var (
	errSendCanceled = fmt.Errorf("send canceled")
	// ErrNoVideoCredit: USB still has an unacked video AU - skip this tick.
	ErrNoVideoCredit = fmt.Errorf("no video credit")
	// ErrNoAudioCredit: USB still has an unacked PCM chunk - skip this chunk.
	ErrNoAudioCredit = fmt.Errorf("no audio credit")
	errSendQueueFull = fmt.Errorf("send queue full")
	errSendTimeout   = fmt.Errorf("send timeout")
)

func (m *MD) fail(err error) {
	select {
	case m.errc <- err:
	default:
	}
	m.doneOnce.Do(func() { close(m.done) })
	if eps, ok := m.rw.(endpointStopper); ok {
		eps.Shutdown(err)
		return
	}
	if c, ok := m.rw.(io.Closer); ok {
		_ = c.Close()
	}
}

// enqueue writes via the writer goroutine so the reader never blocks on net.Pipe.
func (m *MD) enqueue(channel, flags uint8, payload []byte) error {
	return m.enqueueTimeout(channel, flags, payload, 3*time.Second)
}

// enqueueAsync is for the reader path (e.g. StartIndication, Ping) - must not deadlock
// behind a stalled media USB write.
func (m *MD) enqueueAsync(channel, flags uint8, payload []byte) {
	msg := outMsg{channel: channel, flags: flags, payload: append([]byte{}, payload...)}
	q := m.sendQ
	if channel == 0 || channel == m.SensorCh {
		q = m.ctrlQ
	} else if channel == m.AudioCh {
		q = m.audioQ
	}
	select {
	case q <- msg:
	default:
		m.log.Printf("send queue full; dropping ch=%d len=%d", channel, len(payload))
	}
}

func (m *MD) sendPlain(channel uint8, msgType uint16, rest []byte) error {
	p := frame.PutU16(nil, msgType)
	p = append(p, rest...)
	return m.enqueue(channel, frame.FlagBulk, p)
}

// sendEnc writes an encrypted bulk frame. extraFlags may include FlagSpecific
// (control/ChannelOpen only - AACS/DHU reject Specific on media setup/data).
func (m *MD) sendEnc(channel, extraFlags uint8, msgType uint16, rest []byte) error {
	p := frame.PutU16(nil, msgType)
	p = append(p, rest...)
	return m.enqueue(channel, frame.FlagBulk|frame.FlagEncrypted|extraFlags, p)
}

func (m *MD) reader() error {
	var pending []byte
	var pendingFlags uint8
	var pendingCh uint8
	var pendingTotal int
	t0 := time.Now()
	var nFrames atomic.Uint64
	var gotFirst atomic.Bool
	m.log.Printf("handshake: waiting for version request")
	stopProg := make(chan struct{})
	defer close(stopProg)
	go func() {
		tick := time.NewTicker(2 * time.Second)
		defer tick.Stop()
		for {
			select {
			case <-stopProg:
				return
			case <-tick.C:
				if gotFirst.Load() {
					return
				}
				m.log.Printf("handshake: still waiting for first frame after %v", time.Since(t0).Round(time.Second))
			}
		}
	}()

	for {
		select {
		case err := <-m.errc:
			m.log.Printf("handshake: session fail after %v frames=%d: %v", time.Since(t0).Round(time.Millisecond), nFrames.Load(), err)
			return err
		default:
		}
		f, err := frame.Read(m.rw)
		if err != nil {
			m.log.Printf("handshake: read failed after %v frames=%d first=%v: %v",
				time.Since(t0).Round(time.Millisecond), nFrames.Load(), gotFirst.Load(), err)
			return err
		}
		nFrames.Add(1)
		if gotFirst.CompareAndSwap(false, true) {
			mt := uint16(0)
			if len(f.Payload) >= 2 && f.Flags&frame.FlagEncrypted == 0 {
				mt = binary.BigEndian.Uint16(f.Payload[:2])
			}
			m.log.Printf("handshake: first frame ch=%d flags=%#x len=%d msg=0x%04x after %v",
				f.Channel, f.Flags, len(f.Payload), mt, time.Since(t0).Round(time.Millisecond))
			if f.Channel != 0 || mt != MsgVersionRequest {
				m.log.Printf("handshake: first frame is not version request (want ch=0 msg=0x%04x)", MsgVersionRequest)
			}
		}
		payload := f.Payload
		flags := f.Flags

		// Decrypt each on-wire frame first (AACS: one TLS record per frame).
		if flags&frame.FlagEncrypted != 0 {
			plain, err := m.ssl.Decrypt(payload)
			if err != nil {
				return fmt.Errorf("decrypt: %w (ch=%d flags=%#x len=%d head=%x)",
					err, f.Channel, flags, len(payload), headHex(payload, 32))
			}
			payload = plain
		}

		// Reassemble plaintext fragments.
		ft := flags & frame.FlagBulk
		switch {
		case ft == frame.FlagBulk:
			// complete
		case ft == frame.FlagFirst:
			pending = append([]byte{}, payload...)
			pendingFlags = flags
			pendingCh = f.Channel
			pendingTotal = int(f.TotalLen)
			continue
		case ft == 0:
			pending = append(pending, payload...)
			continue
		case ft == frame.FlagLast:
			pending = append(pending, payload...)
			payload = pending
			flags = pendingFlags | frame.FlagLast
			f.Channel = pendingCh
			if pendingTotal > 0 && len(payload) != pendingTotal {
				m.log.Printf("fragment size mismatch got=%d want=%d", len(payload), pendingTotal)
			}
			pending = nil
			pendingTotal = 0
		}

		if err := m.handle(f.Channel, flags, payload); err != nil {
			mt := uint16(0)
			if len(payload) >= 2 {
				mt = binary.BigEndian.Uint16(payload[:2])
			}
			m.log.Printf("handshake: handle ch=%d flags=%#x msg=0x%04x len=%d after %v: %v",
				f.Channel, flags, mt, len(payload), time.Since(t0).Round(time.Millisecond), err)
			return err
		}
	}
}

func (m *MD) handle(channel uint8, flags uint8, payload []byte) error {
	if len(payload) < 2 {
		return fmt.Errorf("short payload")
	}
	msgType := binary.BigEndian.Uint16(payload[:2])
	body := payload[2:]

	if channel != 0 {
		return m.handleChannel(channel, flags, msgType, body, payload)
	}

	switch msgType {
	case MsgVersionRequest:
		if len(body) < 4 {
			return fmt.Errorf("bad version request")
		}
		maj := binary.BigEndian.Uint16(body[0:2])
		min := binary.BigEndian.Uint16(body[2:4])
		m.protoMaj, m.protoMin = maj, min
		m.log.Printf("version request %d.%d body=%x video_ack=%v audio_ack=%v", maj, min, body, m.videoAckRequired(), m.audioAckRequired())
		// if maj != 1 {
		// return fmt.Errorf("unsupported major %d", maj)
		// }
		// Echo requested minor (DHU asks 1.7). AACS AAServer uses 1.5 for cars.
		respMaj, respMin := maj, min
		// if respMin == 0 {
		// respMin = 7
		// }
		resp := frame.PutU16(nil, MsgVersionResponse)
		resp = frame.PutU16(resp, respMaj)
		resp = frame.PutU16(resp, respMin)
		resp = frame.PutU16(resp, 0) // match
		m.log.Printf("version response %d.%d", respMaj, respMin)
		return m.enqueue(0, frame.FlagBulk, resp)

	case MsgSslHandshake:
		m.log.Printf("ssl handshake in (%d bytes)", len(body))
		out, done, err := m.ssl.FeedHandshake(body)
		if err != nil {
			return fmt.Errorf("ssl handshake: %w (in=%dB head=%x)", err, len(body), headHex(body, 32))
		}
		if len(out) > 0 {
			m.log.Printf("ssl handshake out (%d bytes)", len(out))
			msg := frame.PutU16(nil, MsgSslHandshake)
			msg = append(msg, out...)
			if err := m.enqueue(0, frame.FlagBulk, msg); err != nil {
				return err
			}
		}
		if done {
			m.log.Printf("ssl handshake complete (md)")
		}
		return nil

	case MsgAuthComplete:
		m.log.Printf("auth complete")
		// AACS: MD sends ServiceDiscoveryRequest
		req := []byte{}
		// protobuf: manufacturer=5 "cargadgetd", model=4 "linux"
		req = append(req, encodeProtoString(5, "cargadgetd")...)
		req = append(req, encodeProtoString(4, "linux")...)
		if err := m.sendEnc(0, 0, MsgServiceDiscoveryRequest, req); err != nil {
			return err
		}
		return nil

	case MsgServiceDiscoveryResponse:
		m.log.Printf("got service discovery response (%d bytes)", len(body))
		m.parseSDR(body)
		select {
		case <-m.ready:
		default:
			close(m.ready)
		}
		m.emit(Event{Type: "sdr", Payload: append([]byte{}, body...)})
		return nil

	case MsgPingRequest:
		// Never block the reader on ping - media USB writes can stall waiting for the HU,
		// and a blocked reader cannot drain ep2 / reply, which deadlocks DHU (ping timeout
		// + BAD_DECRYPT / stream broken). Control queue is prioritized in writer().
		p := frame.PutU16(nil, MsgPingResponse)
		p = append(p, body...)
		m.log.Printf("ping_rx → pong_async body=%d", len(body))
		m.enqueueAsync(0, frame.FlagBulk|frame.FlagEncrypted, p)
		return nil

	case MsgPingResponse:
		// Answer to a PingRequest we sent: the payload is the timestamp it
		// carried, so the difference is the round trip.
		if len(body) >= 2 {
			ts, _ := readVarint(body[1:])
			if sent := int64(ts); sent > 0 {
				rtt := time.Since(time.Unix(0, sent))
				if rtt > 0 && rtt < time.Minute {
					m.pingRTT.Store(int64(rtt))
					n := m.pingRTTN.Add(1)
					if n <= 3 || n%60 == 0 {
						m.log.Printf("ping rtt %v (n=%d)", rtt.Round(time.Millisecond), n)
					}
				}
			}
		}
		return nil

	case MsgAudioFocusResponse:
		// protobuf AudioFocusState: typically field 1 = state (1=STATE_GAIN).
		m.log.Printf("audio focus response body=%x", body)
		granted := len(body) >= 2 && body[0] == 0x08 && body[1] != 0
		m.capturef("AUDIO_FOCUS", "granted=%v body=%x", granted, body)
		if granted {
			m.audioFocusGrant.Store(true)
			m.audioFocusOnce.Do(func() { close(m.audioFocusOK) })
		} else {
			m.audioFocusGrant.Store(false)
			m.log.Printf("audio focus NOT granted - PCM must stop")
		}
		m.emit(Event{Type: "audio_focus", Payload: append([]byte{}, body...)})
		return nil

	case MsgNavigationFocusRequest:
		// Some OEM HUs send this toward the MD. Unhandled is fatal - ACK projected.
		m.log.Printf("nav focus request body=%x", body)
		m.capturef("NAV_FOCUS_REQ", "body=%x", body)
		return m.sendEnc(0, 0, MsgNavigationFocusResponse, []byte{0x08, 0x01})

	case MsgNavigationFocusResponse:
		m.log.Printf("nav focus response body=%x", body)
		m.capturef("NAV_FOCUS_RESP", "body=%x", body)
		return nil

	case MsgVoiceSessionRequest:
		// MD normally *sends* this; if the HU sends one, log body and ignore
		// only if empty/no-op. Unknown shapes are fatal.
		m.log.Printf("voice session request from HU body=%x", body)
		m.capturef("VOICE_SESSION", "body=%x", body)
		return nil

	case MsgServiceDiscoveryUpdate:
		m.logKnown("control", 0, msgType, body)
		m.scheduleSideServices(body)
		return nil

	default:
		if m.logKnown("control", 0, msgType, body) {
			return nil
		}
		return m.unhandled("control", 0, msgType, body)
	}
}

func (m *MD) handleChannel(channel uint8, flags uint8, msgType uint16, body, full []byte) error {
	if msgType == MsgChannelOpenResponse {
		m.log.Printf("channel %d open ok body=%x", channel, body)
		m.signalWait(m.openWait, channel)
		m.emit(Event{Type: "channel_open_ok", Channel: channel})
		return nil
	}
	if kind, ok := m.sideKind(channel); ok {
		if m.handleSide(kind, channel, msgType, body, full) {
			return nil
		}
		m.logSide(kind, channel, msgType, body)
		return nil
	}

	switch channel {
	case m.InputCh:
		switch msgType {
		case InputEvent:
			n := m.touchLogN.Add(1)
			if n <= 8 || n%50 == 0 {
				m.log.Printf("touch event #%d len=%d body=%x", n, len(full), full)
			}
			_, isTouch := ParseTouch(body)
			btns := ParseButtons(body)
			known, unknown := 0, 0
			for _, b := range btns {
				if _, ok := HuInputName(b.ScanCode); ok {
					known++
				} else {
					unknown++
					m.capturef("INPUT", "unknown_button scan=%d pressed=%v body=%x", b.ScanCode, b.Pressed, body)
				}
			}
			if isTouch || (known == 0 && unknown == 0) {
				m.capturef("INPUT", "ch=%d %s body=%x", channel, formatInputCapture(body), body)
			}
			m.emit(Event{Type: "touch", Channel: channel, Payload: append([]byte{}, full...)})
			return nil
		case InputHandshakeResponse:
			m.log.Printf("input handshake ok")
			m.emit(Event{Type: "input_ready", Channel: channel})
			return nil
		default:
			return m.unhandled("input", channel, msgType, body)
		}
	case m.SensorCh:
		switch msgType {
		case SensorStartRequest:
			st, _ := protoVarintField(body, 1)
			m.log.Printf("sensor start request from HU type=%d (%s) body=%x - ACK only (MD does not send SensorEvent)",
				st, sensorTypeName(int(st)), body)
			m.capturef("SENSOR_START_REQ", "type=%d (%s) body=%x", st, sensorTypeName(int(st)), body)
			resp := frame.PutU16(nil, SensorStartResponse)
			resp = append(resp, 0x08, 0x00)
			m.enqueueAsync(channel, frame.FlagBulk|frame.FlagEncrypted, resp)
			return nil
		case SensorStartResponse:
			st, _ := protoVarintField(body, 1)
			m.log.Printf("sensor start response type=%d body=%x", st, body)
			m.capturef("SENSOR_START_RESP", "type=%d body=%x", st, body)
			return nil
		case SensorEvent:
			m.log.Printf("sensor event %s body=%x", formatSensorBatch(body), body)
			m.capturef("SENSOR", "%s body=%x", formatSensorBatch(body), body)
			return nil
		default:
			return m.unhandled("sensor", channel, msgType, body)
		}
	case m.VideoCh:
		switch msgType {
		case MediaSetupResponse:
			m.log.Printf("video setup ok body=%x", body)
			m.signalWait(m.setupWait, channel)
			m.emit(Event{Type: "video_setup_ok", Channel: channel})
			return nil
		case VideoFocusInd:
			return m.onVideoFocus(channel, body)
		case MediaStopInd:
			m.log.Printf("video MediaStopInd body=%x - pause streaming", body)
			m.capturef("VIDEO_STOP", "body=%x", body)
			m.videoStreaming.Store(false)
			m.clearVideoCredit("MediaStop")
			m.emit(Event{Type: "video_stop", Channel: channel, Payload: append([]byte{}, body...)})
			return nil
		case MediaAck:
			m.onVideoAck()
			return nil
		default:
			if m.logKnown("video", channel, msgType, body) {
				return nil
			}
			return m.unhandled("video", channel, msgType, body)
		}
	case m.AudioCh:
		switch msgType {
		case MediaSetupResponse:
			m.log.Printf("audio setup ok body=%x", body)
			m.signalWait(m.setupWait, channel)
			m.emit(Event{Type: "audio_setup_ok", Channel: channel})
			return nil
		case MediaStopInd:
			m.log.Printf("audio MediaStopInd body=%x - stop PCM until re-Start", body)
			m.capturef("AUDIO_STOP", "body=%x", body)
			if m.audioInFlight.Load() > 0 {
				m.audioInFlight.Store(0)
			}
			m.emit(Event{Type: "audio_stop", Channel: channel, Payload: append([]byte{}, body...)})
			return nil
		case MediaAck:
			if m.audioInFlight.Load() > 0 {
				m.audioInFlight.Add(-1)
			}
			select {
			case m.audioCredit <- struct{}{}:
			default:
			}
			n := m.audioAckLogN.Add(1)
			if n <= 3 || n%100 == 0 {
				m.log.Printf("audio MediaAck #%d (in_flight=%d)", n, m.audioInFlight.Load())
			}
			return nil
		case AudioUnderflow:
			n := m.audioUnderflowN.Add(1)
			if n <= 5 || n%25 == 0 {
				m.log.Printf("audio underflow #%d (sink starved - need continuous PCM)", n)
			}
			m.capturef("AUDIO_UNDERFLOW", "#%d body=%x", n, body)
			return nil
		default:
			if m.logKnown("audio", channel, msgType, body) {
				return nil
			}
			return m.unhandled("audio", channel, msgType, body)
		}
	default:
		if m.logKnown("ch", channel, msgType, body) {
			return nil
		}
		return m.unhandled("ch", channel, msgType, body)
	}
}

// videoFocusProjected is GAL VideoFocusMode 1 (projected) or 4 (projected,
// input stays on the head unit). Both still show our video. Mode 2 is native
// and 3 is native transient. Treating 4 as a stop paused the picture, DHU
// answered with focus=1, and each of those reopened Codec2 until droid start
// killed the process.
func videoFocusProjected(focus int) bool {
	return focus == 1 || focus == 4
}

// onVideoFocus handles VideoFocusIndication. body field1=focus mode.
func (m *MD) onVideoFocus(channel uint8, body []byte) error {
	focus := protoFirstVarint(body)
	m.log.Printf("video focus ind body=%x focus=%d", body, focus)
	m.capturef("VIDEO_FOCUS", "focus=%d body=%x", focus, body)
	if !videoFocusProjected(focus) {
		m.log.Printf("video focus lost (focus=%d) - pause streaming", focus)
		m.videoStreaming.Store(false)
		m.clearVideoCredit("focus_lost")
		m.emit(Event{Type: "video_focus_lost", Channel: channel, Payload: append([]byte{}, body...)})
		return nil
	}
	if m.videoStreaming.Load() {
		m.log.Printf("video focus projected (focus=%d) - already streaming", focus)
		m.emit(Event{Type: "video_focus", Channel: channel, Payload: append([]byte{}, body...)})
		return nil
	}
	m.sendVideoStart(channel)
	m.emit(Event{Type: "video_focus", Channel: channel, Payload: append([]byte{}, body...)})
	return nil
}

func (m *MD) sendVideoStart(channel uint8) {
	m.mu.Lock()
	cfgIdx := m.videoCfgIdx
	vw, vh := m.videoW, m.videoH
	mw, mh := m.videoMW, m.videoMH
	m.mu.Unlock()
	if cfgIdx < 0 || cfgIdx > 127 {
		cfgIdx = 0
	}
	ind := frame.PutU16(nil, MediaStartInd)
	ind = append(ind, 0x08, 0x00, 0x10, byte(cfgIdx))
	m.log.Printf("video StartInd session=0 config_index=%d size=%dx%d m=%dx%d", cfgIdx, vw, vh, mw, mh)
	m.enqueueAsync(channel, frame.FlagBulk|frame.FlagEncrypted, ind)
	m.videoStreaming.Store(true)
	m.mu.Lock()
	if !m.started {
		m.started = true
		close(m.videoGo)
	}
	m.mu.Unlock()
}

func (m *MD) onVideoAck() {
	if m.videoInFlight.Load() > 0 && m.videoInFlight.Add(-1) == 0 {
		m.videoUnackedSince.Store(0)
	}
	select {
	case m.videoCredit <- struct{}{}:
	default:
	}
	n := m.ackLogN.Add(1)
	if n <= 5 || n%25 == 0 {
		m.log.Printf("video MediaAck #%d (in_flight=%d)", n, m.videoInFlight.Load())
	}
	m.lastVideoAckUnix.Store(time.Now().UnixNano())
}

// LastVideoAckAge returns time since last video MediaAck (0 if never).
func (m *MD) LastVideoAckAge() time.Duration {
	t := m.lastVideoAckUnix.Load()
	if t == 0 {
		return 0
	}
	return time.Duration(time.Now().UnixNano() - t)
}

func (m *MD) LastVideoAckAgeLabel() string {
	if m.lastVideoAckUnix.Load() == 0 {
		return "never"
	}
	return m.LastVideoAckAge().Round(time.Millisecond).String()
}

// videoAckRequired is true when this protocol still flow-controls video with
// MediaAck. Head units at 6.0 and above (LIVI 6.1) do not ack video.
func (m *MD) videoAckRequired() bool {
	return m.protoMaj < 6
}

func (m *MD) audioAckRequired() bool {
	return m.protoMaj < 5
}

func (m *MD) clearVideoCredit(why string) {
	if m.videoInFlight.Load() > 0 {
		m.log.Printf("clear video credit (%s) was_in_flight=%d", why, m.videoInFlight.Load())
	}
	m.videoInFlight.Store(0)
	m.videoUnackedSince.Store(0)
	select {
	case m.videoCredit <- struct{}{}:
	default:
	}
}

// protoFirstVarint returns the first protobuf varint value (field 1 wire), or -1.
func protoFirstVarint(body []byte) int {
	if len(body) < 2 || body[0] != 0x08 {
		return -1
	}
	return int(body[1]) // single-byte varint (our observed HU bodies)
}

func (m *MD) signalWait(wait map[uint8]chan struct{}, id uint8) {
	m.mu.Lock()
	ch := wait[id]
	delete(wait, id)
	m.mu.Unlock()
	if ch != nil {
		select {
		case <-ch:
		default:
			close(ch)
		}
	}
}

func mediaSinkCluster(media []byte) bool {
	if len(media) == 0 {
		return false
	}
	if id, ok := protoVarintField(media, 6); ok && id != 0 {
		return true
	}
	if typ, ok := protoVarintField(media, 7); ok && typ != 0 {
		return true
	}
	return false
}

func inputMainTouch(ch []byte, w, h int) bool {
	if w <= 0 || h <= 0 {
		return false
	}
	in := lengthField(ch, 4)
	if len(in) == 0 {
		return true
	}
	if id, ok := protoVarintField(in, 5); ok && id != 0 {
		return false
	}
	return true
}

func (m *MD) parseSDR(body []byte) {
	m.log.Printf("SDR raw (%d bytes)=%x", len(body), body)
	// ServiceDiscoveryResponse: repeated Channel channels = 1
	var audioSinks []audioSink
	var side []sideChannel
	var sawVideo, sawMainVideo, sawInput, sawMainInput, sawSensor bool
	i := 0
	for i < len(body) {
		tag, n := readVarint(body[i:])
		if n == 0 {
			break
		}
		i += n
		fn, wt := int(tag>>3), int(tag&7)
		if wt != 2 {
			if wt == 0 {
				_, n2 := readVarint(body[i:])
				i += n2
				continue
			}
			break
		}
		l, n2 := readVarint(body[i:])
		i += n2
		msg := body[i : i+int(l)]
		i += int(l)
		if fn != 1 {
			continue
		}
		id, kind := parseChannelBlob(msg)
		if id == 0 {
			continue
		}
		switch kind {
		case "video":
			media := channelMedia(msg)
			cfgs := parseMediaVideoConfigs(media)
			cluster := mediaSinkCluster(media)
			m.mu.Lock()
			take := false
			if !cluster && !sawMainVideo {
				if sawVideo && m.VideoCh != 0 {
					side = append(side, sideChannel{id: m.VideoCh, kind: "video"})
				}
				take = true
				sawVideo = true
				sawMainVideo = true
			} else if !sawVideo {
				take = true
				sawVideo = true
			} else {
				side = append(side, sideChannel{id: uint8(id), kind: "video"})
			}
			if take {
				m.VideoCh = uint8(id)
				m.videoCfgs = cfgs
			}
			m.mu.Unlock()
			role := "main"
			if cluster {
				role = "cluster"
			}
			m.log.Printf("SDR video channel=%d %s configs=%s", id, role, formatVideoCfgs(cfgs))
		case "audio":
			media := channelMedia(msg)
			typ := parseMediaAudioType(media)
			cfgs := parseMediaAudioConfigs(media)
			m.log.Printf("SDR audio channel=%d type=%s configs=%s", id, audioTypeName(typ), formatAudioCfgs(cfgs))
			audioSinks = append(audioSinks, audioSink{Ch: uint8(id), Type: typ, Cfgs: cfgs})
		case "input":
			w, h := parseInputScreenSize(msg)
			btns := parseInputButtons(msg)
			mainTouch := inputMainTouch(msg, w, h)
			m.mu.Lock()
			take := false
			if mainTouch && !sawMainInput {
				if sawInput && m.InputCh != 0 {
					side = append(side, sideChannel{id: m.InputCh, kind: "input"})
				}
				take = true
				sawInput = true
				sawMainInput = true
			} else if !sawInput {
				take = true
				sawInput = true
			} else {
				side = append(side, sideChannel{id: uint8(id), kind: "input"})
			}
			if take {
				m.InputCh = uint8(id)
				m.inputW, m.inputH = w, h
				m.inputButtons = btns
			}
			m.mu.Unlock()
			if w > 0 && h > 0 {
				m.log.Printf("SDR input channel=%d touch=%dx%d buttons=%v", id, w, h, btns)
			} else {
				m.log.Printf("SDR input channel=%d touch=unset buttons=%v blob=%x", id, btns, msg)
			}
			m.capturef("SDR", "input ch=%d touch=%dx%d buttons=%v blob=%x", id, w, h, btns, msg)
		case "sensor":
			types := parseSensorTypes(msg)
			m.mu.Lock()
			if sawSensor && m.SensorCh != 0 {
				side = append(side, sideChannel{id: m.SensorCh, kind: "sensor"})
			}
			sawSensor = true
			m.SensorCh = uint8(id)
			m.sensorTypes = types
			m.mu.Unlock()
			m.log.Printf("SDR sensor channel=%d types=%s blob=%x", id, formatSensorTypes(types), msg)
			m.capturef("SDR", "sensor ch=%d types=%s blob=%x", id, formatSensorTypes(types), msg)
		default:
			if kind == "" {
				kind = "unknown"
			}
			m.log.Printf("SDR channel=%d kind=%s blob=%x", id, kind, msg)
			m.capturef("SDR", "channel=%d kind=%s blob=%x", id, kind, msg)
			if id > 0 && id < 256 {
				side = append(side, sideChannel{id: uint8(id), kind: kind})
			}
		}
	}
	wantIdx, wantW, wantH := videoWant()
	wantAudioIdx := audioWant()
	wantAudioType := audioTypeWant()
	m.mu.Lock()
	c, idx := selectVideoConfig(m.videoCfgs, m.inputW, m.inputH, wantIdx, wantW, wantH)
	m.videoW, m.videoH, m.videoMW, m.videoMH, m.videoCfgIdx = c.W, c.H, c.MarginW, c.MarginH, idx
	sink, ac, aidx := selectAudioSink(audioSinks, wantAudioType, wantAudioIdx)
	if sink.Ch != 0 {
		m.AudioCh = sink.Ch
	}
	m.audioType = sink.Type
	m.audioCfgs = sink.Cfgs
	m.audioRate, m.audioChans, m.audioCfgIdx = ac.Rate, ac.Chans, aidx
	for _, s := range audioSinks {
		if s.Ch != 0 && s.Ch != m.AudioCh {
			side = append(side, sideChannel{id: s.Ch, kind: "audio/" + audioTypeName(s.Type)})
		}
	}
	m.side = side
	audioCfgs := append([]audioCfg(nil), m.audioCfgs...)
	audioCh, audioKind := m.AudioCh, audioTypeName(m.audioType)
	m.mu.Unlock()
	m.log.Printf("SDR side channels=%s", formatSide(side))
	ox, oy, cw, ch := c.pad()
	m.log.Printf("SDR video using %dx%d m=%dx%d ui=%dx%d pad=%d,%d config_index=%d (want idx=%d size=%dx%d)",
		c.W, c.H, c.MarginW, c.MarginH, cw, ch, ox, oy, idx, wantIdx, wantW, wantH)
	wantKind := "auto"
	if wantAudioType != 0 {
		wantKind = audioTypeName(wantAudioType)
	}
	m.log.Printf("SDR audio using type=%s ch=%d %s config_index=%d (want type=%s idx=%d advertised=%s)",
		audioKind, audioCh, ac, aidx, wantKind, wantAudioIdx, formatAudioCfgs(audioCfgs))
}

func parseChannelBlob(b []byte) (id uint64, kind string) {
	i := 0
	hasVideoCfg := false
	mediaType := -1
	for i < len(b) {
		tag, n := readVarint(b[i:])
		if n == 0 {
			break
		}
		i += n
		fn, wt := int(tag>>3), int(tag&7)
		if wt == 0 {
			v, n2 := readVarint(b[i:])
			i += n2
			if fn == 1 {
				id = v
			}
			continue
		}
		if wt != 2 {
			break
		}
		l, n2 := readVarint(b[i:])
		i += n2
		sub := b[i : i+int(l)]
		i += int(l)
		switch fn {
		case 2:
			kind = "sensor"
		case 3: // MediaChannel
			mt, hasVC := peekMedia(sub)
			mediaType = mt
			hasVideoCfg = hasVC
			if hasVC || mt == 3 {
				kind = "video"
			} else if mt == 1 {
				kind = "audio"
			} else if kind == "" {
				kind = "media"
			}
		case 4:
			kind = "input"
		case 5:
			kind = "media_input"
		case 6:
			kind = "bluetooth"
		case 7:
			kind = "radio"
		case 8:
			kind = "navigation"
		case 9:
			kind = "media_playback"
		case 10:
			kind = "phone_status"
		case 11:
			kind = "media_browser"
		case 12:
			kind = "vendor"
		case 13:
			kind = "notification"
		case 14:
			kind = "wifi_projection"
		case 15:
			kind = "car_control"
		case 16:
			kind = "car_local_media"
		case 17:
			kind = "buffered_media"
		case 18:
			kind = "car_intent"
		}
	}
	_ = mediaType
	_ = hasVideoCfg
	return id, kind
}

func channelMedia(b []byte) []byte {
	i := 0
	for i < len(b) {
		tag, n := readVarint(b[i:])
		if n == 0 {
			break
		}
		i += n
		fn, wt := int(tag>>3), int(tag&7)
		if wt == 0 {
			_, n2 := readVarint(b[i:])
			i += n2
			continue
		}
		if wt != 2 {
			break
		}
		l, n2 := readVarint(b[i:])
		i += n2
		sub := b[i : i+int(l)]
		i += int(l)
		if fn == 3 {
			return sub
		}
	}
	return nil
}

func peekMedia(b []byte) (mediaType int, hasVideoCfg bool) {
	mediaType = -1
	i := 0
	for i < len(b) {
		tag, n := readVarint(b[i:])
		if n == 0 {
			break
		}
		i += n
		fn, wt := int(tag>>3), int(tag&7)
		if wt == 0 {
			v, n2 := readVarint(b[i:])
			i += n2
			if fn == 1 {
				mediaType = int(v)
			}
			continue
		}
		if wt == 2 {
			l, n2 := readVarint(b[i:])
			i += n2
			i += int(l)
			if fn == 4 {
				hasVideoCfg = true
			}
			continue
		}
		break
	}
	return mediaType, hasVideoCfg
}

func readVarint(b []byte) (uint64, int) {
	var x uint64
	var s uint
	for i := 0; i < len(b); i++ {
		x |= uint64(b[i]&0x7f) << s
		if b[i]&0x80 == 0 {
			return x, i + 1
		}
		s += 7
		if s > 63 {
			break
		}
	}
	return 0, 0
}

func (m *MD) emit(e Event) {
	select {
	case m.Events <- e:
	default:
		m.log.Printf("event dropped: %s", e.Type)
	}
}

// WaitReady waits until ServiceDiscoveryResponse was processed.
func (m *MD) WaitReady() <-chan struct{} { return m.ready }

// Done is closed when the session fails or the transport shuts down.
func (m *MD) Done() <-chan struct{} { return m.done }

// WaitVideoStarted waits until video StartIndication was sent after focus.
func (m *MD) WaitVideoStarted() <-chan struct{} { return m.videoGo }

// OpenVideo starts channel open + setup on the video channel and waits for setup OK.
func (m *MD) OpenVideo() error {
	if err := m.openChannel(m.VideoCh); err != nil {
		return err
	}
	wait := m.armWait(m.setupWait, m.VideoCh)
	// SetupRequest: NO Specific flag (AACS VideoChannelHandler).
	if err := m.sendEnc(m.VideoCh, 0, MediaSetupRequest, []byte{0x08, 0x03}); err != nil {
		return err
	}
	return m.await(wait, 5*time.Second, "video setup")
}

// OpenInput opens input and sends handshake.
func (m *MD) OpenInput() error {
	if err := m.openChannel(m.InputCh); err != nil {
		return err
	}
	return m.sendInputBinding(m.InputCh)
}

func (m *MD) sendInputBinding(ch uint8) error {
	m.mu.Lock()
	hu := append([]int(nil), m.inputButtons...)
	m.mu.Unlock()
	btns := handshakeButtons(hu)
	body := handshakeBody(btns)
	m.log.Printf("input handshake channel=%d buttons=%v (hu_sdr=%v)", ch, btns, hu)
	m.capturef("INPUT_HANDSHAKE", "channel=%d buttons=%v hu_sdr=%v", ch, btns, hu)
	return m.sendEnc(ch, 0, InputHandshakeRequest, body)
}

// OpenSensor opens the HU sensor channel (car → phone). Do not send
// SensorStartRequest - DHU/Subaru treat our encoding as a bad gal.SensorRequest
// and drop the link. If the HU StartRequests the other way, ACK only.
func (m *MD) OpenSensor() error {
	if m.SensorCh == 0 {
		return nil
	}
	if err := m.openChannel(m.SensorCh); err != nil {
		return err
	}
	m.mu.Lock()
	types := append([]int(nil), m.sensorTypes...)
	m.mu.Unlock()
	m.log.Printf("sensor channel open types=%s - wait for HU events (no StartRequest)", formatSensorTypes(types))
	return nil
}

// sideChannel is an advertised service we open and then only log.
type sideChannel struct {
	id   uint8
	kind string
}

func (m *MD) OpenSide() {
	m.mu.Lock()
	sides := append([]sideChannel(nil), m.side...)
	m.mu.Unlock()
	for _, s := range sides {
		m.openSideOnce(s.id, s.kind)
	}
}

func (m *MD) openSideOnce(id uint8, kind string) {
	if id == 0 {
		return
	}
	m.mu.Lock()
	if id == m.VideoCh || id == m.AudioCh || id == m.InputCh || (m.SensorCh != 0 && id == m.SensorCh) {
		m.mu.Unlock()
		return
	}
	if m.sideOpened == nil {
		m.sideOpened = map[uint8]struct{}{}
	}
	if _, ok := m.sideOpened[id]; ok {
		m.mu.Unlock()
		return
	}
	m.sideOpened[id] = struct{}{}
	found := false
	for _, s := range m.side {
		if s.id == id {
			found = true
			break
		}
	}
	if !found {
		m.side = append(m.side, sideChannel{id: id, kind: kind})
	}
	m.mu.Unlock()
	m.log.Printf("open side channel=%d kind=%s", id, kind)
	if err := m.openChannel(id); err != nil {
		m.log.Printf("open side channel=%d kind=%s: %v", id, kind, err)
		return
	}
	if kind == "input" {
		if err := m.sendInputBinding(id); err != nil {
			m.log.Printf("input handshake channel=%d: %v", id, err)
		}
	}
}

func (m *MD) handleSide(kind string, channel uint8, msgType uint16, body, full []byte) bool {
	switch kind {
	case "input":
		switch msgType {
		case InputEvent:
			m.emit(Event{Type: "touch", Channel: channel, Payload: append([]byte{}, full...)})
			return true
		case InputHandshakeResponse:
			m.log.Printf("input handshake ok channel=%d", channel)
			return true
		}
	case "media_playback":
		if msgType == MediaPlaybackInput {
			m.onClusterPad(channel, body)
			return true
		}
	}
	return false
}

func (m *MD) scheduleSideServices(body []byte) {
	id, kind, ok := sideFromUpdate(body)
	if !ok {
		return
	}
	go m.openSideOnce(id, kind)
}

func sideFromUpdate(body []byte) (uint8, string, bool) {
	svc, ok := protoBytesField(body, 1)
	if !ok {
		return 0, "", false
	}
	id, kind := parseChannelBlob(svc)
	if id == 0 || id > 255 {
		return 0, "", false
	}
	if kind == "" {
		kind = "unknown"
	}
	if kind == "audio" {
		kind = "audio/" + audioTypeName(parseMediaAudioType(channelMedia(svc)))
	}
	return uint8(id), kind, true
}

func (m *MD) sideKind(id uint8) (string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, s := range m.side {
		if s.id == id {
			return s.kind, true
		}
	}
	return "", false
}

func (m *MD) logSide(kind string, channel uint8, msgType uint16, body []byte) {
	if m.logKnown(kind, channel, msgType, body) {
		return
	}
	hex := headHex(body, 32)
	m.log.Printf("%s ch=%d msg=0x%04x body=%x", kind, channel, msgType, hex)
	m.capturef("PROTO", "%s ch=%d msg=0x%04x body=%x", kind, channel, msgType, hex)
}

func formatSide(side []sideChannel) string {
	if len(side) == 0 {
		return "(none)"
	}
	var b strings.Builder
	for i, s := range side {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, "%d:%s", s.id, s.kind)
	}
	return b.String()
}

func (m *MD) logKnown(scope string, channel uint8, msgType uint16, body []byte) bool {
	detail, ok := knownMessage(msgType, body)
	if !ok {
		return false
	}
	hex := headHex(body, 32)
	m.log.Printf("%s ch=%d %s body=%x", scope, channel, detail, hex)
	m.capturef("PROTO", "%s ch=%d %s body=%x", scope, channel, detail, hex)
	return true
}

func knownMessage(msgType uint16, body []byte) (string, bool) {
	switch msgType {
	case MsgChannelCloseNotification:
		return "channel close", true
	case MsgByeByeRequest:
		if v, ok := protoVarintField(body, 1); ok {
			return fmt.Sprintf("byebye request reason=%s", byeByeReasonName(int(v))), true
		}
		return "byebye request", true
	case MsgByeByeResponse:
		return "byebye response", true
	case MsgCarConnectedDevicesRequest:
		return "car connected devices request", true
	case MsgCarConnectedDevicesResponse:
		return "car connected devices response", true
	case MsgUserSwitchRequest:
		return "user switch request", true
	case MsgBatteryStatusNotification:
		return formatBattery(body), true
	case MsgCallAvailabilityStatus:
		if v, ok := protoVarintField(body, 1); ok {
			return fmt.Sprintf("call availability available=%t", v != 0), true
		}
		return "call availability", true
	case MsgUserSwitchResponse:
		return "user switch response", true
	case MsgServiceDiscoveryUpdate:
		return "service discovery update", true
	case MsgUnexpectedMessage:
		return "unexpected message", true
	case MsgInternalError:
		return "internal error", true
	case MsgHardShutdown:
		return "hard shutdown", true
	case MediaMicRequest:
		return "microphone request", true
	case MediaMicResponse:
		return "microphone response", true
	case VideoFocusRequest:
		mode, hasMode := protoVarintField(body, 2)
		reason, hasReason := protoVarintField(body, 3)
		switch {
		case hasMode && hasReason:
			return fmt.Sprintf("video focus request mode=%s reason=%s", videoFocusModeName(int(mode)), videoFocusReasonName(int(reason))), true
		case hasMode:
			return fmt.Sprintf("video focus request mode=%s", videoFocusModeName(int(mode))), true
		case hasReason:
			return fmt.Sprintf("video focus request reason=%s", videoFocusReasonName(int(reason))), true
		default:
			return "video focus request", true
		}
	case MediaUpdateUIConfigRequest:
		return "update ui config request", true
	case MediaUpdateUIConfigReply:
		return "update ui config reply", true
	case MediaActionTaken:
		return "action taken", true
	case MediaOverlayParams:
		return "integrated overlay parameters", true
	case MediaOverlayStart:
		return "integrated overlay start", true
	case MediaOverlayStop:
		return "integrated overlay stop", true
	case MediaOverlaySession:
		return "integrated overlay session", true
	case MediaUpdateHUUIConfigRequest:
		return "update head unit ui config request", true
	case MediaUpdateHUUIConfigResponse:
		return "update head unit ui config response", true
	case MediaStats:
		return "media stats", true
	case MediaOptions:
		return "media options", true
	case MediaCriticalUI:
		if v, ok := protoVarintField(body, 1); ok {
			return fmt.Sprintf("critical ui focus=%s", criticalUIFocusName(int(v))), true
		}
		return "critical ui", true
	default:
		return "", false
	}
}

func formatBattery(body []byte) string {
	s := "battery status"
	if v, ok := protoVarintField(body, 1); ok {
		s += fmt.Sprintf(" level=%d", v)
	}
	if v, ok := protoVarintField(body, 2); ok {
		s += fmt.Sprintf(" seconds=%d", v)
	}
	if v, ok := protoVarintField(body, 3); ok {
		s += fmt.Sprintf(" low_power=%t", v != 0)
	}
	return s
}

func byeByeReasonName(v int) string {
	switch v {
	case 1:
		return "USER_SELECTION"
	case 2:
		return "DEVICE_SWITCH"
	case 3:
		return "NOT_SUPPORTED"
	case 4:
		return "NOT_CURRENTLY_SUPPORTED"
	case 5:
		return "PROBE_SUPPORTED"
	case 6:
		return "WIRELESS_PROJECTION_DISABLED"
	case 7:
		return "POWER_DOWN"
	case 8:
		return "USER_PROFILE_SWITCH"
	default:
		return fmt.Sprintf("%d", v)
	}
}

func videoFocusModeName(v int) string {
	switch v {
	case 1:
		return "PROJECTED"
	case 2:
		return "NATIVE"
	case 3:
		return "NATIVE_TRANSIENT"
	case 4:
		return "PROJECTED_NO_INPUT_FOCUS"
	default:
		return fmt.Sprintf("%d", v)
	}
}

func videoFocusReasonName(v int) string {
	switch v {
	case 0:
		return "UNKNOWN"
	case 1:
		return "PHONE_SCREEN_OFF"
	case 2:
		return "LAUNCH_NATIVE"
	case 3:
		return "LAST_MODE"
	case 4:
		return "USER_SELECTION"
	default:
		return fmt.Sprintf("%d", v)
	}
}

func criticalUIFocusName(v int) string {
	switch v {
	case 0:
		return "UNKNOWN"
	case 1:
		return "PROJECTED"
	case 2:
		return "NATIVE"
	default:
		return fmt.Sprintf("%d", v)
	}
}

func (m *MD) unhandled(scope string, channel uint8, msgType uint16, body []byte) error {
	m.log.Printf("UNHANDLED %s ch=%d msg=0x%04x len=%d body=%x", scope, channel, msgType, len(body), body)
	m.capturef("UNHANDLED", "%s ch=%d msg=0x%04x len=%d body=%x", scope, channel, msgType, len(body), body)
	if cfg.Get().Apps.Strict {
		return fmt.Errorf("unhandled %s ch=%d msg=0x%04x len=%d body=%x", scope, channel, msgType, len(body), body)
	}
	return nil
}

// OpenAudio opens audio media channel + setup, waits for AudioFocus grant, then StartInd.
func (m *MD) OpenAudio() error {
	if err := m.openChannel(m.AudioCh); err != nil {
		return err
	}
	wait := m.armWait(m.setupWait, m.AudioCh)
	// SetupRequest type=PCM (1). Config index selects rate; channel count is
	// whatever the HU advertised (Pixel→DHU used stereo-sized ~8KiB PCM URBs).
	if err := m.sendEnc(m.AudioCh, 0, MediaSetupRequest, []byte{0x08, 0x01}); err != nil {
		return err
	}
	if err := m.await(wait, 5*time.Second, "audio setup"); err != nil {
		return err
	}
	// Request audio focus (GAIN=1) on control channel - must be granted before PCM.
	if err := m.sendEnc(0, 0, MsgAudioFocusRequest, []byte{0x08, 0x01}); err != nil {
		return err
	}
	select {
	case <-m.audioFocusOK:
		m.log.Printf("audio focus granted - sending StartIndication")
	case <-time.After(2 * time.Second):
		return fmt.Errorf("audio focus grant timeout")
	case <-m.done:
		return fmt.Errorf("session done waiting for audio focus")
	}
	// StartIndication: session=0, config_index from SDR (48kHz stereo when advertised).
	// audio.config_index selects that audio_configs[] entry.
	_, _, cfgIdx := m.AudioFormat()
	if cfgIdx < 0 {
		cfgIdx = 1
	}
	m.log.Printf("audio StartInd config_index=%d", cfgIdx)
	start := []byte{0x08, 0x00, 0x10, byte(cfgIdx)}
	if err := m.sendEnc(m.AudioCh, 0, MediaStartInd, start); err != nil {
		return err
	}
	// Default: skip silence primes. Priming then stopping underflows the DHU
	// sink and correlated with video MediaAck death; the AV loop's first
	// sendTone covers StartInd. audio.force_prime restores the old primes.
	if !cfg.Get().Audio.ForcePrime {
		m.log.Printf("audio StartInd sent - skipping PCM primes")
		return nil
	}
	// Prime several 20ms PCM chunks immediately - one is not enough before the
	// AV loop; DHU underflows within milliseconds of StartIndication.
	silence := make([]byte, 48000*20/1000*2)
	for i := 0; i < 5; i++ {
		if err := m.SendPCM(uint64(i+1), silence); err != nil {
			if errors.Is(err, ErrNoAudioCredit) {
				break
			}
			return err
		}
	}
	return nil
}

func (m *MD) armWait(wait map[uint8]chan struct{}, id uint8) chan struct{} {
	ch := make(chan struct{})
	m.mu.Lock()
	wait[id] = ch
	m.mu.Unlock()
	return ch
}

func (m *MD) await(ch <-chan struct{}, d time.Duration, what string) error {
	select {
	case <-ch:
		return nil
	case <-time.After(d):
		return fmt.Errorf("%s timeout", what)
	}
}

func (m *MD) openChannel(id uint8) error {
	wait := m.armWait(m.openWait, id)
	// ChannelOpenRequest: Specific flag required.
	body := []byte{0x08, 0x00, 0x10, byte(id)}
	if err := m.sendEnc(id, frame.FlagSpecific, MsgChannelOpenRequest, body); err != nil {
		return err
	}
	return m.await(wait, 5*time.Second, fmt.Sprintf("channel %d open", id))
}

// SendH264 sends one video NALu/bitstream chunk (USB credit via SendH264Credit).
func (m *MD) SendH264(tsMicros uint64, annexB []byte) error {
	_, err := m.SendH264Credit(tsMicros, annexB)
	return err
}

func (m *MD) AllowVideo() {
	if m.videoStreaming.Load() {
		return
	}
	m.log.Printf("video start without focus - sending StartInd")
	m.sendVideoStart(m.VideoCh)
}

// VideoStreaming reports whether the HU currently wants video (after focus/Start).
func (m *MD) VideoStreaming() bool { return m.videoStreaming.Load() }

// AudioFocusGranted is true after a non-zero AudioFocusResponse.
func (m *MD) AudioFocusGranted() bool { return m.audioFocusGrant.Load() }

// ReserveVideoCredit waits up to wait for a free USB video slot (inFlight++).
// wait<=0 is a single non-blocking try. Call Encode only after this succeeds;
// on encode failure call ReleaseVideoCredit. The live loop must skip the tick
// on ErrNoVideoCredit (drop the stale frame). A short stall keeps that
// credit; GiveUpVideoAck is the only path that drops it.
func (m *MD) ReserveVideoCredit(wait time.Duration) error {
	if !m.usbBulk || !m.videoAckRequired() {
		return nil
	}
	if !m.videoStreaming.Load() {
		return ErrNoVideoCredit
	}
	if m.videoInFlight.Load() < 1 {
		m.videoInFlight.Add(1)
		return nil
	}
	if wait <= 0 {
		select {
		case <-m.videoCredit:
		default:
		}
		if m.videoInFlight.Load() >= 1 {
			return ErrNoVideoCredit
		}
		m.videoInFlight.Add(1)
		return nil
	}
	deadline := time.After(wait)
	for m.videoInFlight.Load() >= 1 {
		select {
		case <-m.videoCredit:
		case <-deadline:
			return ErrNoVideoCredit
		case <-m.done:
			return fmt.Errorf("session done")
		}
	}
	m.videoInFlight.Add(1)
	return nil
}

// WaitVideoAcked blocks until inFlight video is MediaAck'd (or timeout).
func (m *MD) WaitVideoAcked(wait time.Duration) error {
	if !m.usbBulk || !m.videoAckRequired() {
		return nil
	}
	deadline := time.After(wait)
	for m.videoInFlight.Load() >= 1 {
		select {
		case <-m.videoCredit:
		case <-deadline:
			return ErrNoVideoCredit
		case <-m.done:
			return fmt.Errorf("session done")
		}
	}
	return nil
}

// WaitAudioAcked blocks until inFlight PCM is MediaAck'd (or timeout).
func (m *MD) WaitAudioAcked(wait time.Duration) error {
	return m.waitAudioInFlightBelow(wait, 1)
}

// WaitAudioCredit blocks until a USB PCM send slot is free (inFlight < MaxUSBAudioInFlight).
func (m *MD) WaitAudioCredit(wait time.Duration) error {
	return m.waitAudioInFlightBelow(wait, MaxUSBAudioInFlight)
}

func (m *MD) waitAudioInFlightBelow(wait time.Duration, max int32) error {
	if !m.usbBulk || !m.audioAckRequired() {
		return nil
	}
	deadline := time.After(wait)
	for m.audioInFlight.Load() >= max {
		select {
		case <-m.audioCredit:
		case <-deadline:
			return ErrNoAudioCredit
		case <-m.done:
			return fmt.Errorf("session done")
		}
	}
	return nil
}

// AbandonAudioCredit clears a stuck USB PCM credit. usbmon: after a large
// audio Bi the host often submits Bo (MediaAck) that never completes; waiting
// forever then freezes video/pings. Prefer abandon + PCM cooldown over wedging.
func (m *MD) AbandonAudioCredit(reason string) {
	if !m.usbBulk || !m.audioAckRequired() {
		return
	}
	if n := m.audioInFlight.Load(); n > 0 {
		m.audioInFlight.Store(0)
		m.log.Printf("abandon audio credit: %s (was in_flight=%d)", reason, n)
	}
}

// VideoInFlight reports outstanding unacked video AUs (0 or 1 on USB).
func (m *MD) VideoInFlight() int {
	return int(m.videoInFlight.Load())
}

// AudioInFlight reports outstanding unacked PCM messages on USB.
func (m *MD) AudioInFlight() int {
	return int(m.audioInFlight.Load())
}

// ReleaseVideoCredit undoes ReserveVideoCredit when encode/send never went out.
func (m *MD) ReleaseVideoCredit() {
	if !m.usbBulk || !m.videoAckRequired() {
		return
	}
	if m.videoInFlight.Load() > 0 && m.videoInFlight.Add(-1) == 0 {
		m.videoUnackedSince.Store(0)
	}
}

// VideoAckGiveUp is how long an AU may stay unacked before the sender drops
// that credit. MediaAck RTT on USB is ~80ms, so this is a few frames past a
// slow ack, not a multi-second freeze. The dropped AU is not retransmitted.
const VideoAckGiveUp = 200 * time.Millisecond

// VideoUnackedAge is how long the current USB video AU has been waiting for
// MediaAck. Zero when nothing is in flight.
func (m *MD) VideoUnackedAge() time.Duration {
	if m.videoInFlight.Load() < 1 {
		return 0
	}
	t := m.videoUnackedSince.Load()
	if t == 0 {
		return 0
	}
	return time.Duration(time.Now().UnixNano() - t)
}

// GiveUpVideoAck drops the single unacked video AU once it has waited at
// least after. The next encode must be a new frame (an IDR), not a replay
// of the one the head unit may already be decoding. A late MediaAck for the
// dropped AU can free the following frame one RTT early; the head unit's
// max_unacked window covers that one extra.
func (m *MD) GiveUpVideoAck(after time.Duration) bool {
	if !m.usbBulk || !m.videoAckRequired() || after <= 0 || m.VideoUnackedAge() < after {
		return false
	}
	age := m.VideoUnackedAge()
	m.videoInFlight.Store(0)
	m.videoUnackedSince.Store(0)
	select {
	case m.videoCredit <- struct{}{}:
	default:
	}
	m.log.Printf("abandon video credit: no MediaAck for %s", age.Round(time.Millisecond))
	return true
}

// SendH264Credit is SendH264 plus a bypass flag (unused on USB skip path).
// Prefer ReserveVideoCredit → Encode → SendH264Reserved on the live USB path.
func (m *MD) SendH264Credit(tsMicros uint64, annexB []byte) (bypassed bool, err error) {
	if err := m.ReserveVideoCredit(400 * time.Millisecond); err != nil {
		return false, err
	}
	err = m.SendH264Reserved(tsMicros, annexB)
	return false, err
}

// SendVideoCodecConfig sends the H.264 parameter sets as their own CODEC_CONFIG
// message, which is what the official phone does instead of leaving them in-band
// only. A head unit that waits for this message acks every frame and shows
// nothing, because it never learns the picture size.
func (m *MD) SendVideoCodecConfig(annexB []byte) error {
	if len(annexB) == 0 {
		return nil
	}
	p := frame.PutU16(nil, MediaCodecConfig)
	p = append(p, annexB...)
	return m.enqueueTimeout(m.VideoCh, frame.FlagBulk|frame.FlagEncrypted, p, 3*time.Second)
}

// SendH264Reserved enqueues a video AU after ReserveVideoCredit.
func (m *MD) SendH264Reserved(tsMicros uint64, annexB []byte) error {
	// Always DATA (0x0000), timestamp included even when zero.
	p := frame.PutU16(nil, MediaWithTimestamp)
	p = frame.PutU64(p, tsMicros)
	p = append(p, annexB...)
	timeout := 3 * time.Second
	if m.usbBulk {
		timeout = 1500 * time.Millisecond
	}
	err := m.enqueueTimeout(m.VideoCh, frame.FlagBulk|frame.FlagEncrypted, p, timeout)
	if err != nil && m.usbBulk {
		if errors.Is(err, errSendQueueFull) {
			m.ReleaseVideoCredit()
		}
		return err
	}
	if err == nil {
		if m.usbBulk && m.videoAckRequired() {
			m.videoUnackedSince.Store(time.Now().UnixNano())
		}
		n := m.videoTxN.Add(1)
		m.log.Printf("video_tx #%d au=%dB", n, len(annexB))
	}
	return err
}

// enqueueTimeout is enqueue with a caller-chosen wait.
// On timeout the message is marked dropped so the writer will not put stale
// frames on USB after the caller has moved on (zombie sendQ → BAD_DECRYPT).
func (m *MD) enqueueTimeout(channel, flags uint8, payload []byte, wait time.Duration) error {
	done := make(chan error, 1)
	drop := &atomic.Bool{}
	msg := outMsg{
		channel: channel,
		flags:   flags,
		payload: append([]byte{}, payload...),
		done:    done,
		drop:    drop,
	}
	q := m.sendQ
	if channel == 0 {
		q = m.ctrlQ
	} else if channel == m.AudioCh {
		q = m.audioQ
	}
	select {
	case q <- msg:
	case <-time.After(wait):
		return errSendQueueFull
	}
	select {
	case err := <-done:
		return err
	case <-time.After(wait):
		drop.Store(true)
		return errSendTimeout
	}
}

// enqueuePosted queues a media frame and returns when it is on the writer
// queue (not when the USB URB completes). Used for PCM so the 42ms ticker
// is not slaved to IN URB completion (~80ms → 0.5× playback).
func (m *MD) enqueuePosted(channel, flags uint8, payload []byte, wait time.Duration) error {
	msg := outMsg{
		channel: channel,
		flags:   flags,
		payload: append([]byte{}, payload...),
	}
	q := m.sendQ
	if channel == 0 {
		q = m.ctrlQ
	} else if channel == m.AudioCh {
		q = m.audioQ
	}
	select {
	case q <- msg:
		return nil
	case <-time.After(wait):
		return errSendQueueFull
	case <-m.done:
		return fmt.Errorf("session done")
	}
}

// SendPCM sends raw PCM audio on the audio channel.
// On USB, waits until inFlight < MaxUSBAudioInFlight (pipeline of 2).
func (m *MD) SendPCM(tsMicros uint64, pcm []byte) error {
	trackAck := m.usbBulk && m.audioAckRequired()
	if trackAck {
		wait := 500 * time.Millisecond
		deadline := time.After(wait)
		for m.audioInFlight.Load() >= MaxUSBAudioInFlight {
			select {
			case <-m.audioCredit:
			case <-deadline:
				return ErrNoAudioCredit
			case <-m.done:
				return fmt.Errorf("session done")
			}
		}
		m.audioInFlight.Add(1)
	}
	p := frame.PutU16(nil, MediaWithTimestamp)
	p = frame.PutU64(p, tsMicros)
	p = append(p, pcm...)
	timeout := 500 * time.Millisecond
	if m.usbBulk {
		timeout = 1500 * time.Millisecond
		err := m.enqueuePosted(m.AudioCh, frame.FlagBulk|frame.FlagEncrypted, p, timeout)
		if err != nil && trackAck {
			m.audioInFlight.Add(-1)
		}
		return err
	}
	return m.enqueueTimeout(m.AudioCh, frame.FlagBulk|frame.FlagEncrypted, p, timeout)
}

// encodeProtoString encodes a protobuf wire string field.
func encodeProtoString(fieldNumber int, s string) []byte {
	tag := byte(fieldNumber<<3 | 2)
	b := []byte{tag, byte(len(s))}
	return append(b, s...)
}

func headHex(b []byte, n int) []byte {
	if n > len(b) {
		n = len(b)
	}
	return b[:n]
}

func encodeProtoVarint(fieldNumber int, v uint64) []byte {
	tag := byte(fieldNumber<<3 | 0)
	out := []byte{tag}
	for v >= 0x80 {
		out = append(out, byte(v)|0x80)
		v >>= 7
	}
	return append(out, byte(v))
}
