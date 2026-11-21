package session

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
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
)

// Media / input (channel-specific).
const (
	MediaWithTimestamp = 0x0000
	MediaIndication    = 0x0001
	MediaSetupRequest  = 0x8000
	MediaStartInd      = 0x8001
	MediaStopInd       = 0x8002
	MediaSetupResponse = 0x8003
	MediaAck           = 0x8004
	VideoFocusInd      = 0x8008
	// Sensor channel: MD opens it and StartRequests types the HU advertised.
	// HU replies with SensorStartResponse and streams SensorEvent (car → phone).
	SensorStartRequest  = 0x8001
	SensorStartResponse = 0x8002
	SensorEvent         = 0x8003
	// AudioUnderflow (0x800b): HU → MD when the audio sink starves (newer AA).
	// Seen after one-shot beep PCM with no continuous stream → session death under tap storms.
	AudioUnderflow = 0x800b

	InputEvent             = 0x8001
	InputHandshakeRequest  = 0x8002
	InputHandshakeResponse = 0x8003
)

// Channel IDs we expect from a typical HU SDR (test peer uses these).
const (
	ChanSensor = 1
	ChanVideo  = 2
	ChanInput  = 3
	ChanAudio  = 4
)

// Bluetooth channel (service 10) message ids. The MD opens the channel and
// announces its Bluetooth address so the head unit can associate this session
// with the phone's Bluetooth bond: that association is what makes a car offer
// Wireless Android Auto after the first wired session.
const (
	MsgBluetoothPairingRequest  = 0x8001
	MsgBluetoothPairingResponse = 0x8002
)

// MaxUSBAudioInFlight is how many unacked PCM messages USB may have in flight.
// Video stays at 1. Audio needs 2: DHU MediaAck often lands in ~70–80ms, and
// waiting for that ack before the next 42ms tick stretched playback to ~0.5×.
const MaxUSBAudioInFlight int32 = 2

// Event delivered to control clients / tests.
type Event struct {
	Type    string
	Channel uint8
	Payload []byte
}

// MD is the phone/MD session endpoint.
type MD struct {
	rw  io.ReadWriter
	ssl *aassl.Session
	log *log.Logger
	mu  sync.Mutex

	VideoCh      uint8
	InputCh      uint8
	AudioCh      uint8
	SensorCh     uint8
	BluetoothCh  uint8
	btMethods    []uint32
	btAnnounced  atomic.Bool
	localBTAddr  string
	localBTName  string
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
	ctrlQ  chan outMsg // control replies — writer prefers these (pings!)
	audioQ chan outMsg // constant PCM — between ctrl and video so large IDRs cannot starve the sink

	// usbBulk: FunctionFS transport — larger TLS-safe chunks + inter-fragment pause.
	usbBulk bool

	// videoCredit wakes ReserveVideoCredit when a MediaAck arrives.
	// videoInFlight caps unacked video AUs on USB (max 1).
	// videoUnackedSince is when that AU's USB write finished (0 if none).
	videoCredit       chan struct{}
	videoInFlight     atomic.Int32
	videoUnackedSince atomic.Int64
	// audioCredit / audioInFlight: PCM had no flow control while video was
	// credit-gated — capture showed DHU OUT dying right after a 9.6KiB audio
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
		sendQ:        make(chan outMsg, 2), // shallow — USB backpressure
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

// SetLocalBluetooth records the phone's Bluetooth address and name. They are
// sent to the head unit on the Bluetooth channel (service 10) so it can tie
// this USB/Wi-Fi session to the phone's Bluetooth bond — the link a car uses
// before it offers Wireless Android Auto.
func (m *MD) SetLocalBluetooth(addr, name string) {
	m.mu.Lock()
	m.localBTAddr, m.localBTName = addr, name
	m.mu.Unlock()
}

// Run drives the MD side until the connection fails.
func (m *MD) Run() error {
	go m.writer()
	err := m.reader()
	m.doneOnce.Do(func() { close(m.done) }) // stop writer — one leaked per session otherwise
	return err
}

func (m *MD) writer() {
	for {
		select {
		case <-m.done:
			return // session over — do NOT drain queues against a closing SSL/transport
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
	if err != nil && msg.channel == 0 {
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
		// nested AIO wait — see ffs/aio_linux.go).
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
			deadline = time.Now().Add(800 * time.Millisecond) // progress — extend
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
			// Nothing of this buffer is on the wire yet — pong freely, retry.
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
	// ErrNoVideoCredit: USB still has an unacked video AU — skip this tick.
	ErrNoVideoCredit = fmt.Errorf("no video credit")
	// ErrNoAudioCredit: USB still has an unacked PCM chunk — skip this chunk.
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
	// Unblock a reader stuck in ep2 Read so Run() returns promptly.
	if eps, ok := m.rw.(endpointStopper); ok {
		eps.Shutdown(err)
	}
}

// enqueue writes via the writer goroutine so the reader never blocks on net.Pipe.
func (m *MD) enqueue(channel, flags uint8, payload []byte) error {
	return m.enqueueTimeout(channel, flags, payload, 3*time.Second)
}

// enqueueAsync is for the reader path (e.g. StartIndication, Ping) — must not deadlock
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
// (control/ChannelOpen only — AACS/DHU reject Specific on media setup/data).
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
		m.log.Printf("version request %d.%d body=%x", maj, min, body)
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
		// Open the Bluetooth channel and announce our address. Must not run on
		// the reader goroutine: openChannel waits for ChannelOpenResponse,
		// which only the reader can deliver.
		go m.sendBluetoothPairing()
		return nil

	case MsgPingRequest:
		// Never block the reader on ping — media USB writes can stall waiting for the HU,
		// and a blocked reader cannot drain ep2 / reply, which deadlocks DHU (ping timeout
		// + BAD_DECRYPT / stream broken). Control queue is prioritized in writer().
		p := frame.PutU16(nil, MsgPingResponse)
		p = append(p, body...)
		m.log.Printf("ping_rx → pong_async body=%d", len(body))
		m.enqueueAsync(0, frame.FlagBulk|frame.FlagEncrypted, p)
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
			m.log.Printf("audio focus NOT granted — PCM must stop")
		}
		m.emit(Event{Type: "audio_focus", Payload: append([]byte{}, body...)})
		return nil

	case MsgNavigationFocusRequest:
		// Some OEM HUs send this toward the MD. Unhandled is fatal — ACK projected.
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

	default:
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

	switch channel {
	case m.BluetoothCh:
		if msgType == MsgBluetoothPairingResponse {
			already, status := parseBluetoothPairingResponse(body)
			m.log.Printf("bluetooth: pairing response already_paired=%v status=%d (0 none, 1 ok, 2 fail)", already, status)
			m.emit(Event{Type: "bluetooth_pairing", Channel: channel, Payload: append([]byte{}, full...)})
			return nil
		}
		return m.unhandled("bluetooth", channel, msgType, body)
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
			m.log.Printf("sensor start request from HU type=%d (%s) body=%x — ACK only (MD does not send SensorEvent)",
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
			m.log.Printf("video MediaStopInd body=%x — pause streaming", body)
			m.capturef("VIDEO_STOP", "body=%x", body)
			m.videoStreaming.Store(false)
			m.clearVideoCredit("MediaStop")
			m.emit(Event{Type: "video_stop", Channel: channel, Payload: append([]byte{}, body...)})
			return nil
		case MediaAck:
			m.onVideoAck()
			return nil
		default:
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
			m.log.Printf("audio MediaStopInd body=%x — stop PCM until re-Start", body)
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
				m.log.Printf("audio underflow #%d (sink starved — need continuous PCM)", n)
			}
			m.capturef("AUDIO_UNDERFLOW", "#%d body=%x", n, body)
			return nil
		default:
			return m.unhandled("audio", channel, msgType, body)
		}
	default:
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
		m.log.Printf("video focus lost (focus=%d) — pause streaming", focus)
		m.videoStreaming.Store(false)
		m.clearVideoCredit("focus_lost")
		m.emit(Event{Type: "video_focus_lost", Channel: channel, Payload: append([]byte{}, body...)})
		return nil
	}
	if m.videoStreaming.Load() {
		m.log.Printf("video focus projected (focus=%d) — already streaming", focus)
		m.emit(Event{Type: "video_focus", Channel: channel, Payload: append([]byte{}, body...)})
		return nil
	}
	// StartIndication: session=0, config_index = chosen video_configs[] entry.
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
	m.emit(Event{Type: "video_focus", Channel: channel, Payload: append([]byte{}, body...)})
	return nil
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

func (m *MD) parseSDR(body []byte) {
	m.log.Printf("SDR raw (%d bytes)=%x", len(body), body)
	// ServiceDiscoveryResponse: repeated Channel channels = 1
	var audioSinks []audioSink
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
			m.mu.Lock()
			m.VideoCh = uint8(id)
			m.videoCfgs = parseMediaVideoConfigs(channelMedia(msg))
			cfgs := append([]videoCfg(nil), m.videoCfgs...)
			m.mu.Unlock()
			m.log.Printf("SDR video channel=%d configs=%s", id, formatVideoCfgs(cfgs))
		case "audio":
			media := channelMedia(msg)
			typ := parseMediaAudioType(media)
			cfgs := parseMediaAudioConfigs(media)
			m.log.Printf("SDR audio channel=%d type=%s configs=%s", id, audioTypeName(typ), formatAudioCfgs(cfgs))
			audioSinks = append(audioSinks, audioSink{Ch: uint8(id), Type: typ, Cfgs: cfgs})
		case "input":
			w, h := parseInputScreenSize(msg)
			btns := parseInputButtons(msg)
			m.mu.Lock()
			m.InputCh = uint8(id)
			m.inputW, m.inputH = w, h
			m.inputButtons = btns
			m.mu.Unlock()
			if w > 0 && h > 0 {
				m.log.Printf("SDR input channel=%d touch=%dx%d buttons=%v", id, w, h, btns)
			} else {
				m.log.Printf("SDR input channel=%d touch=unset buttons=%v blob=%x", id, btns, msg)
			}
			m.capturef("SDR", "input ch=%d touch=%dx%d buttons=%v blob=%x", id, w, h, btns, msg)
		case "bt":
			addr, methods := parseBluetoothChannel(msg)
			m.mu.Lock()
			m.BluetoothCh = uint8(id)
			m.btMethods = methods
			m.mu.Unlock()
			m.log.Printf("SDR bluetooth channel=%d adapter=%q methods=%v blob=%x", id, addr, methods, msg)
			m.capturef("SDR", "bluetooth ch=%d adapter=%q methods=%v", id, addr, methods)
		case "sensor":
			types := parseSensorTypes(msg)
			m.mu.Lock()
			m.SensorCh = uint8(id)
			m.sensorTypes = types
			m.mu.Unlock()
			m.log.Printf("SDR sensor channel=%d types=%s blob=%x", id, formatSensorTypes(types), msg)
			m.capturef("SDR", "sensor ch=%d types=%s blob=%x", id, formatSensorTypes(types), msg)
		default:
			m.log.Printf("SDR channel=%d kind=%s blob=%x", id, kind, msg)
			m.capturef("SDR", "channel=%d kind=%s blob=%x", id, kind, msg)
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
	audioCfgs := append([]audioCfg(nil), m.audioCfgs...)
	audioCh, audioKind := m.AudioCh, audioTypeName(m.audioType)
	m.mu.Unlock()
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
			kind = "bt"
		case 8:
			kind = "nav"
		case 12:
			kind = "vendor"
		}
	}
	_ = mediaType
	_ = hasVideoCfg
	return id, kind
}

// parseBluetoothChannel reads a Channel descriptor and returns the head unit's
// Bluetooth adapter address and the pairing methods it advertises
// (BluetoothChannel: adapter_address=1, supported_pairing_methods=2).
func parseBluetoothChannel(channelBlob []byte) (string, []uint32) {
	var addr string
	var methods []uint32
	i := 0
	for i < len(channelBlob) {
		tag, n := readVarint(channelBlob[i:])
		if n == 0 {
			break
		}
		i += n
		fn, wt := int(tag>>3), int(tag&7)
		if wt == 0 {
			_, n2 := readVarint(channelBlob[i:])
			i += n2
			continue
		}
		if wt != 2 {
			break
		}
		l, n2 := readVarint(channelBlob[i:])
		if n2 == 0 || i+n2+int(l) > len(channelBlob) {
			break
		}
		i += n2
		sub := channelBlob[i : i+int(l)]
		i += int(l)
		if fn == 6 { // bluetooth_channel
			addr, methods = parseBluetoothData(sub)
		}
	}
	return addr, methods
}

// parseBluetoothData reads the BluetoothChannel message. Supported methods are
// a proto3 repeated enum, so they arrive packed (wire type 2) in practice; the
// unpacked form is accepted too.
func parseBluetoothData(b []byte) (string, []uint32) {
	var addr string
	var methods []uint32
	i := 0
	for i < len(b) {
		tag, n := readVarint(b[i:])
		if n == 0 {
			break
		}
		i += n
		fn, wt := int(tag>>3), int(tag&7)
		switch wt {
		case 0:
			v, n2 := readVarint(b[i:])
			if n2 == 0 {
				return addr, methods
			}
			i += n2
			if fn == 2 {
				methods = append(methods, uint32(v))
			}
		case 2:
			l, n2 := readVarint(b[i:])
			if n2 == 0 || i+n2+int(l) > len(b) {
				return addr, methods
			}
			i += n2
			sub := b[i : i+int(l)]
			i += int(l)
			switch fn {
			case 1:
				addr = string(sub)
			case 2:
				j := 0
				for j < len(sub) {
					v, n3 := readVarint(sub[j:])
					if n3 == 0 {
						break
					}
					j += n3
					methods = append(methods, uint32(v))
				}
			}
		default:
			return addr, methods
		}
	}
	return addr, methods
}

// parseBluetoothPairingResponse reads BluetoothPairingResponse:
// already_paired=1 (bool), status=2 (enum: 0 none, 1 ok, 2 fail).
func parseBluetoothPairingResponse(b []byte) (alreadyPaired bool, status uint32) {
	i := 0
	for i < len(b) {
		tag, n := readVarint(b[i:])
		if n == 0 {
			break
		}
		i += n
		fn, wt := int(tag>>3), int(tag&7)
		if wt != 0 {
			if wt == 2 {
				l, n2 := readVarint(b[i:])
				if n2 == 0 || i+n2+int(l) > len(b) {
					break
				}
				i += n2 + int(l)
				continue
			}
			break
		}
		v, n2 := readVarint(b[i:])
		if n2 == 0 {
			break
		}
		i += n2
		switch fn {
		case 1:
			alreadyPaired = v != 0
		case 2:
			status = uint32(v)
		}
	}
	return alreadyPaired, status
}

// bluetoothPairingBody builds BluetoothPairingRequest:
// phone_address=1 (string), pairing_method=2 (enum, omitted for NONE).
func bluetoothPairingBody(addr string, method uint32) []byte {
	body := encodeProtoString(1, addr)
	if method != 0 {
		body = append(body, 0x10, byte(method))
	}
	return body
}

// sendBluetoothPairing opens the head unit's Bluetooth channel and announces
// the phone's Bluetooth address. Some head units require this before they list
// the phone as a Wireless Android Auto device, because it is how they learn
// which Bluetooth bond belongs to this USB (or Wi-Fi) session.
func (m *MD) sendBluetoothPairing() {
	m.mu.Lock()
	ch := m.BluetoothCh
	addr, name := m.localBTAddr, m.localBTName
	methods := append([]uint32(nil), m.btMethods...)
	m.mu.Unlock()
	if ch == 0 {
		return
	}
	// One announcement per session: a second SDR (rare) must not open the
	// channel again.
	if !m.btAnnounced.CompareAndSwap(false, true) {
		return
	}
	if addr == "" {
		m.log.Printf("bluetooth: channel %d available but no local address configured, not announcing", ch)
		return
	}
	if err := m.openChannel(ch); err != nil {
		m.log.Printf("bluetooth: open channel %d: %v", ch, err)
		return
	}
	// The head unit advertises the pairing methods it supports (A2DP, HFP);
	// announce once per method, and once with NONE when it advertises none.
	if len(methods) == 0 {
		methods = []uint32{0}
	}
	seen := map[uint32]bool{}
	for _, method := range methods {
		if seen[method] {
			continue
		}
		seen[method] = true
		body := bluetoothPairingBody(addr, method)
		if err := m.sendEnc(ch, frame.FlagSpecific, MsgBluetoothPairingRequest, body); err != nil {
			m.log.Printf("bluetooth: pairing request (method %d) on channel %d: %v", method, ch, err)
			return
		}
		m.log.Printf("bluetooth: pairing request sent on channel %d address=%s name=%q method=%d", ch, addr, name, method)
	}
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
	// Claim every known GAL key (and any extra the HU advertised). The HU
	// only sends buttons we list here — a short echo meant Next/Call never
	// arrived.
	m.mu.Lock()
	hu := append([]int(nil), m.inputButtons...)
	m.mu.Unlock()
	btns := handshakeButtons(hu)
	body := handshakeBody(btns)
	m.log.Printf("input handshake buttons=%v (hu_sdr=%v)", btns, hu)
	m.capturef("INPUT_HANDSHAKE", "buttons=%v hu_sdr=%v", btns, hu)
	return m.sendEnc(m.InputCh, 0, InputHandshakeRequest, body)
}

// OpenSensor opens the HU sensor channel (car → phone). Do not send
// SensorStartRequest — DHU/Subaru treat our encoding as a bad gal.SensorRequest
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
	m.log.Printf("sensor channel open types=%s — wait for HU events (no StartRequest)", formatSensorTypes(types))
	return nil
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
	// Request audio focus (GAIN=1) on control channel — must be granted before PCM.
	if err := m.sendEnc(0, 0, MsgAudioFocusRequest, []byte{0x08, 0x01}); err != nil {
		return err
	}
	select {
	case <-m.audioFocusOK:
		m.log.Printf("audio focus granted — sending StartIndication")
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
		m.log.Printf("audio StartInd sent — skipping PCM primes")
		return nil
	}
	// Prime several 20ms PCM chunks immediately — one is not enough before the
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

// AllowVideo marks video as streamable (focus/Start already done, or start timeout).
func (m *MD) AllowVideo() {
	m.videoStreaming.Store(true)
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
	if !m.usbBulk {
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
	if !m.usbBulk {
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
	if !m.usbBulk {
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
	if !m.usbBulk {
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
	if !m.usbBulk {
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
	if !m.usbBulk || after <= 0 || m.VideoUnackedAge() < after {
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

// SendH264Reserved enqueues a video AU after ReserveVideoCredit.
func (m *MD) SendH264Reserved(tsMicros uint64, annexB []byte) error {
	var p []byte
	if tsMicros == 0 {
		p = frame.PutU16(nil, MediaIndication)
	} else {
		p = frame.PutU16(nil, MediaWithTimestamp)
		p = frame.PutU64(p, tsMicros)
	}
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
		if m.usbBulk {
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
	if m.usbBulk {
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
		if err != nil {
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
