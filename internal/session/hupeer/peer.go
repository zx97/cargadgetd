// Package hupeer implements a minimal HU (head unit) peer for tests.
package hupeer

import (
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"sync"
	"sync/atomic"

	"github.com/cargadgetd/cargadgetd/internal/frame"
	"github.com/cargadgetd/cargadgetd/internal/session"
	aassl "github.com/cargadgetd/cargadgetd/internal/ssl"
)

// Peer speaks the HU side of AA over an io.ReadWriter.
type Peer struct {
	rw  io.ReadWriter
	ssl *aassl.Session
	log *log.Logger

	videoFrames int
	audioFrames int
	touchesSent int
	btRequests  int
	btAddress   string
	btMethods   []uint32
	// SkipVideoAck, when set, receives video but does not MediaAck (n-ACK stall).
	SkipVideoAck atomic.Bool
	// byeByeResp counts the ByeByeResponse frames the phone sent back.
	byeByeResp atomic.Int32
	// phonePings counts the PingRequests the phone sent (see MD.EnablePings).
	phonePings atomic.Int32

	mu    sync.Mutex
	sendQ chan peerOut
}

type peerOut struct {
	channel, flags uint8
	payload        []byte
	done           chan error
}

// Frames returns how many video and audio frames this peer has received.
// The counters are updated by the reader goroutine, so tests must read them
// through this accessor rather than the fields.
func (p *Peer) Frames() (video, audio int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.videoFrames, p.audioFrames
}

// Touches returns how many touches this peer has sent.
func (p *Peer) Touches() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.touchesSent
}

// BluetoothPairing returns how many BluetoothPairingRequest messages arrived,
// the last phone address announced and the pairing methods that came with it.
func (p *Peer) BluetoothPairing() (requests int, address string, methods []uint32) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.btRequests, p.btAddress, append([]uint32(nil), p.btMethods...)
}

// PhonePings returns how many PingRequest messages the phone sent.
func (p *Peer) PhonePings() int { return int(p.phonePings.Load()) }

// parseBTRequest reads BluetoothPairingRequest: phone_address=1 (string),
// pairing_method=2 (varint enum).
func parseBTRequest(b []byte) (string, []uint32) {
	var addr string
	var methods []uint32
	i := 0
	for i < len(b) {
		tag, n := readVarintHu(b[i:])
		if n == 0 {
			break
		}
		i += n
		fn, wt := int(tag>>3), int(tag&7)
		switch wt {
		case 0:
			v, n2 := readVarintHu(b[i:])
			if n2 == 0 {
				return addr, methods
			}
			i += n2
			if fn == 2 {
				methods = append(methods, uint32(v))
			}
		case 2:
			l, n2 := readVarintHu(b[i:])
			if n2 == 0 || i+n2+int(l) > len(b) {
				return addr, methods
			}
			i += n2
			if fn == 1 {
				addr = string(b[i : i+int(l)])
			}
			i += int(l)
		default:
			return addr, methods
		}
	}
	return addr, methods
}

func readVarintHu(b []byte) (uint64, int) {
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

// CustomSDR, when set, replaces the discovery response this peer sends: a
// recorded head unit's own bytes can be replayed without the car.
var CustomSDR []byte

func New(rw io.ReadWriter, ssl *aassl.Session, logger *log.Logger) *Peer {
	if logger == nil {
		logger = log.Default()
	}
	return &Peer{rw: rw, ssl: ssl, log: logger, sendQ: make(chan peerOut, 64)}
}

func (p *Peer) enqueue(channel, flags uint8, payload []byte) error {
	done := make(chan error, 1)
	p.sendQ <- peerOut{channel: channel, flags: flags, payload: append([]byte{}, payload...), done: done}
	return <-done
}

func (p *Peer) writer() {
	for msg := range p.sendQ {
		var err error
		if msg.flags&frame.FlagEncrypted != 0 {
			err = frame.WriteEncrypted(p.rw, msg.channel, msg.flags, msg.payload, p.ssl.Encrypt)
		} else {
			err = frame.Write(p.rw, frame.Frame{Channel: msg.channel, Flags: msg.flags, Payload: msg.payload})
		}
		if msg.done != nil {
			msg.done <- err
		}
	}
}

// Run performs HU handshake and serves until EOF.
func (p *Peer) Run() error {
	go p.writer()

	// Version request
	vr := frame.PutU16(nil, session.MsgVersionRequest)
	vr = frame.PutU16(vr, 1)
	vr = frame.PutU16(vr, 1)
	if err := p.enqueue(0, frame.FlagBulk, vr); err != nil {
		return err
	}

	sslDone := false
	var pending []byte
	var pendingFlags uint8
	var pendingCh uint8

	for {
		f, err := frame.Read(p.rw)
		if err != nil {
			return err
		}
		payload := f.Payload
		flags := f.Flags

		if flags&frame.FlagEncrypted != 0 {
			payload, err = p.ssl.Decrypt(payload)
			if err != nil {
				return fmt.Errorf("hu decrypt: %w", err)
			}
		}

		ft := flags & frame.FlagBulk
		switch {
		case ft == frame.FlagBulk:
			// complete
		case ft == frame.FlagFirst:
			pending = append([]byte{}, payload...)
			pendingFlags = flags
			pendingCh = f.Channel
			continue
		case ft == 0 && pending != nil:
			pending = append(pending, payload...)
			continue
		case ft == frame.FlagLast && pending != nil:
			payload = append(pending, payload...)
			flags = pendingFlags | frame.FlagLast
			f.Channel = pendingCh
			pending = nil
		}
		if len(payload) < 2 {
			continue
		}
		msgType := binary.BigEndian.Uint16(payload[:2])
		body := payload[2:]

		if f.Channel != 0 {
			if err := p.onChannel(f.Channel, msgType, body, payload); err != nil {
				return err
			}
			continue
		}

		switch msgType {
		case session.MsgVersionResponse:
			p.log.Printf("hu: version ok")
			out, done, err := p.ssl.FeedHandshake(nil)
			if err != nil {
				return err
			}
			if err := p.sendSSL(out); err != nil {
				return err
			}
			sslDone = done

		case session.MsgSslHandshake:
			out, done, err := p.ssl.FeedHandshake(body)
			if err != nil {
				return err
			}
			if err := p.sendSSL(out); err != nil {
				return err
			}
			if done && !sslDone {
				sslDone = true
				p.log.Printf("hu: ssl complete")
				// AuthComplete protobuf status=0 → 08 00
				ac := frame.PutU16(nil, session.MsgAuthComplete)
				ac = append(ac, 0x08, 0x00)
				if err := p.enqueue(0, frame.FlagBulk, ac); err != nil {
					return err
				}
			}

		case session.MsgServiceDiscoveryRequest:
			p.log.Printf("hu: SDR request")
			sdr := buildTestSDR()
			if len(CustomSDR) > 0 {
				sdr = CustomSDR
			}
			msg := frame.PutU16(nil, session.MsgServiceDiscoveryResponse)
			msg = append(msg, sdr...)
			if err := p.enqueue(0, frame.FlagBulk|frame.FlagEncrypted, msg); err != nil {
				return err
			}

		case session.MsgPingRequest:
			// The phone pings on wireless links; answer like a head unit does,
			// echoing the timestamp it sent.
			p.phonePings.Add(1)
			resp := frame.PutU16(nil, session.MsgPingResponse)
			resp = append(resp, body...)
			if err := p.enqueue(0, frame.FlagBulk|frame.FlagEncrypted, resp); err != nil {
				return err
			}

		case session.MsgPingResponse:
			// ignore

		case session.MsgByeByeResponse:
			p.byeByeResp.Add(1)

		case session.MsgAudioFocusRequest:
			p.log.Printf("hu: audio focus request body=%x", body)
			resp := frame.PutU16(nil, session.MsgAudioFocusResponse)
			resp = append(resp, 0x08, 0x01) // STATE_GAIN
			if err := p.enqueue(0, frame.FlagBulk|frame.FlagEncrypted, resp); err != nil {
				return err
			}

		default:
			p.log.Printf("hu: unhandled ctrl 0x%04x len=%d — fixture ignores", msgType, len(body))
		}
	}
}

func (p *Peer) sendSSL(out []byte) error {
	if len(out) == 0 {
		return nil
	}
	msg := frame.PutU16(nil, session.MsgSslHandshake)
	msg = append(msg, out...)
	return p.enqueue(0, frame.FlagBulk, msg)
}

func (p *Peer) onChannel(ch uint8, msgType uint16, body, full []byte) error {
	// 0x8001 is reused on several channels (InputEvent, MediaStartInd): the
	// Bluetooth pairing request is only defined on channel 10.
	if ch == 10 && msgType == session.MsgBluetoothPairingRequest {
		addr, methods := parseBTRequest(body)
		p.mu.Lock()
		p.btRequests++
		p.btAddress = addr
		p.btMethods = append(p.btMethods, methods...)
		p.mu.Unlock()
		// already_paired=true, status=OK: the car recognised the bond.
		resp := frame.PutU16(nil, session.MsgBluetoothPairingResponse)
		resp = append(resp, 0x08, 0x01, 0x10, 0x01)
		return p.enqueue(ch, frame.FlagBulk|frame.FlagEncrypted|frame.FlagSpecific, resp)
	}
	switch msgType {
	case session.MsgChannelOpenRequest:
		resp := frame.PutU16(nil, session.MsgChannelOpenResponse)
		resp = append(resp, 0x08, 0x00) // status=0
		return p.enqueue(ch, frame.FlagBulk|frame.FlagEncrypted|frame.FlagSpecific, resp)

	case session.MediaSetupRequest:
		resp := frame.PutU16(nil, session.MediaSetupResponse)
		resp = append(resp, 0x08, 0x00)
		// Media setup/focus/data: no Specific (matches AACS / DHU).
		if err := p.enqueue(ch, frame.FlagBulk|frame.FlagEncrypted, resp); err != nil {
			return err
		}
		if ch == session.ChanVideo {
			vf := frame.PutU16(nil, session.VideoFocusInd)
			vf = append(vf, 0x08, 0x01)
			return p.enqueue(ch, frame.FlagBulk|frame.FlagEncrypted, vf)
		}
		return nil

	case session.MediaCodecConfig, session.MediaWithTimestamp:
		p.mu.Lock()
		if ch == session.ChanVideo {
			p.videoFrames++
		} else if ch == session.ChanAudio {
			p.audioFrames++
		}
		p.mu.Unlock()
		if ch == session.ChanVideo && p.SkipVideoAck.Load() {
			return nil
		}
		return p.SendMediaAck(ch)

	case session.InputHandshakeRequest:
		resp := frame.PutU16(nil, session.InputHandshakeResponse)
		return p.enqueue(ch, frame.FlagBulk|frame.FlagEncrypted, resp)

	case session.MediaStartInd:
		return nil
	}
	return nil
}

// SendByeBye asks the phone to end the projection, as a car does when the
// driver leaves Android Auto.
func (p *Peer) SendByeBye(reason int) error {
	msg := frame.PutU16(nil, session.MsgByeByeRequest)
	msg = append(msg, 0x08, byte(reason))
	return p.enqueue(0, frame.FlagBulk|frame.FlagEncrypted, msg)
}

// ByeByeResponses is how many ByeByeResponse frames the phone sent.
func (p *Peer) ByeByeResponses() int { return int(p.byeByeResp.Load()) }

// SendMediaAck sends a MediaAck on ch (session=0). Used to release an n-ACK stall.
func (p *Peer) SendMediaAck(ch uint8) error {
	ack := frame.PutU16(nil, session.MediaAck)
	ack = append(ack, 0x08, 0x00)
	return p.enqueue(ch, frame.FlagBulk|frame.FlagEncrypted, ack)
}

// SendTouch injects a Press touch toward the MD.
func (p *Peer) SendTouch(x, y uint32) error {
	loc := append(encodeVarintField(1, uint64(x)), encodeVarintField(2, uint64(y))...)
	loc = append(loc, encodeVarintField(3, 0)...) // pointer id
	te := encodeKV(1, loc)
	te = append(te, encodeVarintField(3, 0)...) // action Press
	msg := frame.PutU16(nil, session.InputEvent)
	msg = append(msg, encodeKV(3, te)...)
	p.mu.Lock()
	p.touchesSent++
	p.mu.Unlock()
	return p.enqueue(session.ChanInput, frame.FlagBulk|frame.FlagEncrypted|frame.FlagSpecific, msg)
}

func encodeVarintField(field int, v uint64) []byte {
	return append([]byte{byte(field << 3)}, encodeVarint(v)...)
}

func encodeVarint(v uint64) []byte {
	var b []byte
	for v >= 0x80 {
		b = append(b, byte(v)|0x80)
		v >>= 7
	}
	return append(b, byte(v))
}

func encodeKV(field int, val []byte) []byte {
	tag := byte(field<<3 | 2)
	return append([]byte{tag, byte(len(val))}, val...)
}

// buildTestSDR returns a hand-built ServiceDiscoveryResponse protobuf advertising
// sensor(1), video(2), input(3), audio(4) and bluetooth(10).
func buildTestSDR() []byte {
	// We'll build channels as length-delimited field 1 messages.
	var out []byte
	out = append(out, channelMsg(session.ChanSensor, sensorChannel())...)
	out = append(out, channelMsg(session.ChanVideo, videoChannel())...)
	out = append(out, channelMsg(session.ChanInput, inputChannel())...)
	out = append(out, channelMsg(session.ChanAudio, audioChannel())...)
	out = append(out, channelMsg(10, bluetoothChannel())...)
	return out
}

// bluetoothChannel is BluetoothChannel{adapter_address, supported_pairing_methods}
// as a Channel descriptor payload (field 6 of Channel).
func bluetoothChannel() []byte {
	bt := encodeKV(1, []byte("10:32:2C:50:5D:5E"))
	bt = append(bt, 0x12, 0x02, 0x02, 0x04) // packed methods: A2DP(2), HFP(4)
	return encodeKV(6, bt)
}

func channelMsg(id uint8, specific []byte) []byte {
	var body []byte
	body = append(body, 0x08, id) // channel_id varint
	body = append(body, specific...)
	return encodeKV(1, body)
}

func sensorChannel() []byte {
	// sensor_channel field 2: sensors repeated int32 — driving status = 10
	inner := []byte{0x08, 10}
	return encodeKV(2, inner)
}

func videoChannel() []byte {
	// media_channel field 3: media_type=Video(3), video_configs
	vc := []byte{}
	vc = append(vc, 0x08, 1)       // resolution R480p
	vc = append(vc, 0x10, 1)       // fps 30
	vc = append(vc, 0x18, 0)       // margin_width
	vc = append(vc, 0x20, 0)       // margin_height
	vc = append(vc, 0x28, 0x90, 1) // dpi 144
	mc := []byte{0x08, 3}          // media_type Video
	mc = append(mc, encodeKV(4, vc)...)
	return encodeKV(3, mc)
}

func inputChannel() []byte {
	tc := []byte{0x08, 0xa0, 0x06, 0x10, 0xe0, 0x03} // width=800 height=480 varints
	ic := encodeKV(2, tc)                            // screen_config
	return encodeKV(4, ic)
}

func audioChannel() []byte {
	ac := []byte{0x08, 0x80, 0xbb, 0x01, 0x10, 16, 0x18, 2} // 48000, 16bit, stereo approx
	mc := []byte{0x08, 1, 0x10, 1}                          // Audio + Media
	mc = append(mc, encodeKV(3, ac)...)
	return encodeKV(3, mc)
}
