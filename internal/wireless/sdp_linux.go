//go:build linux

package wireless

import (
	"encoding/binary"
	"fmt"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

// aaUUID is the 128-bit Android Auto service class UUID.
var aaUUID = [16]byte{
	0x4d, 0xe1, 0x7a, 0x00, 0x52, 0xcb, 0x11, 0xe6,
	0xbd, 0xf4, 0x08, 0x00, 0x20, 0x0c, 0x9a, 0x66,
}

const sdpPSM = 1

// sdpRFCOMMChannel asks the head unit's SDP server which RFCOMM channel its
// Android Auto service uses.
func sdpRFCOMMChannel(addr string, logf func(string, ...any)) (uint8, error) {
	bd, err := parseBDAddr(addr)
	if err != nil {
		return 0, err
	}
	fd, err := unix.Socket(unix.AF_BLUETOOTH, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, unix.BTPROTO_L2CAP)
	if err != nil {
		return 0, fmt.Errorf("sdp l2cap socket: %w", err)
	}
	start := time.Now()
	if err := unix.Connect(fd, &unix.SockaddrL2{PSM: sdpPSM, Addr: bd}); err != nil {
		unix.Close(fd)
		return 0, fmt.Errorf("sdp l2cap connect %s psm %d after %s: %w", addr, sdpPSM, sinceText(start), err)
	}
	if logf != nil {
		logf("sdp: l2cap connected to %s psm %d in %s", addr, sdpPSM, sinceText(start))
	}
	f := os.NewFile(uintptr(fd), "sdp")
	if f == nil {
		unix.Close(fd)
		return 0, fmt.Errorf("sdp: os.NewFile failed")
	}
	defer f.Close()
	_ = f.SetDeadline(time.Now().Add(5 * time.Second))

	req := sdpAARequest()
	if logf != nil {
		logf("sdp: request %d bytes: %s", len(req), hexPreview(req))
	}
	t := time.Now()
	if _, err := f.Write(req); err != nil {
		return 0, fmt.Errorf("sdp write after %s: %w", sinceText(t), err)
	}
	buf := make([]byte, 4096)
	n, err := f.Read(buf)
	if err != nil {
		return 0, fmt.Errorf("sdp read after %s: %w", sinceText(t), err)
	}
	resp := buf[:n]
	if logf != nil {
		logf("sdp: response %d bytes in %s: %s", len(resp), sinceText(t), hexPreview(resp))
	}
	if len(resp) < 5 || resp[0] != 0x07 {
		return 0, fmt.Errorf("sdp: unexpected response (%d bytes) %s", len(resp), hexPreview(resp))
	}
	if ch, ok := rfcommChannelFromSDP(resp); ok {
		if logf != nil {
			logf("sdp: Android Auto descriptor carries RFCOMM channel %d", ch)
		}
		return ch, nil
	}
	return 0, fmt.Errorf("sdp: no RFCOMM channel in the %d-byte response", len(resp))
}

func sdpAARequest() []byte {
	b := []byte{0x06, 0x00, 0x01} // ServiceSearchAttributeRequest, txid 1
	var p []byte
	// ServiceSearchPattern: one UUID128.
	p = append(p, 0x35, 0x11, 0x1C)
	p = append(p, aaUUID[:]...)
	// MaximumAttributeByteCount.
	p = append(p, 0xFF, 0xFF)
	// AttributeIDList: range 0x0000-0xFFFF.
	p = append(p, 0x35, 0x05, 0x0A, 0x00, 0x00, 0xFF, 0xFF)
	// ContinuationState.
	p = append(p, 0x00)
	var l [2]byte
	binary.BigEndian.PutUint16(l[:], uint16(len(p)))
	b = append(b, l[:]...)
	return append(b, p...)
}

// rfcommChannelFromSDP scans the attribute list for the RFCOMM protocol
// descriptor. The channel is a uint8 data element right after UUID16 0x0003.
func rfcommChannelFromSDP(resp []byte) (uint8, bool) {
	for i := 0; i+4 < len(resp); i++ {
		if resp[i] == 0x19 && resp[i+1] == 0x00 && resp[i+2] == 0x03 && resp[i+3] == 0x08 {
			return resp[i+4], true
		}
	}
	return 0, false
}
