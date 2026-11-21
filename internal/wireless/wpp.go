package wireless

import (
	"encoding/binary"
	"fmt"
	"io"
	"strings"
)

// WPP message ids (Wi-Fi Projection Protocol, RFCOMM/TCP before GAL).
const (
	wppStartRequest    = 1
	wppInfoRequest     = 2
	wppInfoResponse    = 3
	wppVersionRequest  = 4
	wppVersionResponse = 5
	wppConnectStatus   = 6
	wppStartResponse   = 7
	wppPingRequest     = 8
	wppPingResponse    = 9
	wppSetupInfo       = 11
)

// WPP status values.
const (
	wppStatusSuccess = 12
)

// wppPacket is one WPP frame: [payload_len u16be][msg_id u16be][payload].
type wppPacket struct {
	ID      uint16
	Payload []byte
}

func readWPP(r io.Reader) (wppPacket, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return wppPacket{}, err
	}
	n := binary.BigEndian.Uint16(hdr[0:2])
	id := binary.BigEndian.Uint16(hdr[2:4])
	p := make([]byte, n)
	if n > 0 {
		if _, err := io.ReadFull(r, p); err != nil {
			return wppPacket{}, err
		}
	}
	return wppPacket{ID: id, Payload: p}, nil
}

func writeWPP(w io.Writer, id uint16, payload []byte) error {
	var hdr [4]byte
	binary.BigEndian.PutUint16(hdr[0:2], uint16(len(payload)))
	binary.BigEndian.PutUint16(hdr[2:4], id)
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	if len(payload) > 0 {
		_, err := w.Write(payload)
		return err
	}
	return nil
}

// --- minimal protobuf -------------------------------------------------------

type pbField struct {
	Num     int
	Var     uint64
	Bytes   []byte
	IsBytes bool
}

func pbParse(b []byte) []pbField {
	var out []pbField
	i := 0
	for i < len(b) {
		tag, n := pbVarintAt(b[i:])
		if n == 0 {
			break
		}
		i += n
		num, wt := int(tag>>3), int(tag&7)
		switch wt {
		case 0:
			v, n2 := pbVarintAt(b[i:])
			if n2 == 0 {
				return out
			}
			i += n2
			out = append(out, pbField{Num: num, Var: v})
		case 2:
			l, n2 := pbVarintAt(b[i:])
			if n2 == 0 {
				return out
			}
			i += n2
			if i+int(l) > len(b) {
				return out
			}
			out = append(out, pbField{Num: num, Bytes: b[i : i+int(l)], IsBytes: true})
			i += int(l)
		case 1:
			i += 8
		case 5:
			i += 4
		default:
			return out
		}
	}
	return out
}

func pbVarintAt(b []byte) (uint64, int) {
	var v uint64
	for i := 0; i < len(b) && i < 10; i++ {
		v |= uint64(b[i]&0x7f) << (7 * uint(i))
		if b[i]&0x80 == 0 {
			return v, i + 1
		}
	}
	return 0, 0
}

func pbLastVarint(fields []pbField, num int) (uint64, bool) {
	for i := len(fields) - 1; i >= 0; i-- {
		if fields[i].Num == num && !fields[i].IsBytes {
			return fields[i].Var, true
		}
	}
	return 0, false
}

func pbLastBytes(fields []pbField, num int) ([]byte, bool) {
	for i := len(fields) - 1; i >= 0; i-- {
		if fields[i].Num == num && fields[i].IsBytes {
			return fields[i].Bytes, true
		}
	}
	return nil, false
}

func pbLastString(fields []pbField, num int) string {
	b, _ := pbLastBytes(fields, num)
	return string(b)
}

func pbAppendVarint(b []byte, num int, v uint64) []byte {
	b = pbAppendUvarint(b, uint64(num<<3|0))
	return pbAppendUvarint(b, v)
}

func pbAppendString(b []byte, num int, s string) []byte {
	b = pbAppendUvarint(b, uint64(num<<3|2))
	b = pbAppendUvarint(b, uint64(len(s)))
	return append(b, s...)
}

func pbAppendBytes(b []byte, num int, p []byte) []byte {
	b = pbAppendUvarint(b, uint64(num<<3|2))
	b = pbAppendUvarint(b, uint64(len(p)))
	return append(b, p...)
}

func pbAppendUvarint(b []byte, v uint64) []byte {
	for v >= 0x80 {
		b = append(b, byte(v)|0x80)
		v >>= 7
	}
	return append(b, byte(v))
}

// --- message helpers --------------------------------------------------------

// describeWPP renders a packet for logs.
func describeWPP(p wppPacket) string {
	var b strings.Builder
	fmt.Fprintf(&b, "id=%d len=%d hex=%x", p.ID, len(p.Payload), p.Payload)
	if len(p.Payload) > 0 {
		for _, f := range pbParse(p.Payload) {
			if f.IsBytes {
				fmt.Fprintf(&b, " f%d=%q", f.Num, printable(f.Bytes))
			} else {
				fmt.Fprintf(&b, " f%d=%d", f.Num, f.Var)
			}
		}
	}
	return b.String()
}

func printable(b []byte) string {
	if len(b) == 0 || len(b) > 64 {
		return fmt.Sprintf("<%d bytes>", len(b))
	}
	for _, c := range b {
		if c < 0x20 || c > 0x7e {
			return fmt.Sprintf("%x", b)
		}
	}
	return string(b)
}

// parseVersionSSID reads the Wi-Fi Direct group name (the AP SSID) from
// WifiVersionRequest's WifiDirectGroupInfo.
func parseVersionSSID(p []byte) string {
	f := pbParse(p)
	if g, ok := pbLastBytes(f, 7); ok {
		return pbLastString(pbParse(g), 2)
	}
	return ""
}

// parseStartEndpoint handles WifiStartRequest (layout A: ip=2/port=3) and the
// older layout (ip=1/port=2).
func parseStartEndpoint(p []byte) (ip string, port int) {
	f := pbParse(p)
	if s := pbLastString(f, 2); s != "" {
		if v, ok := pbLastVarint(f, 3); ok {
			return s, int(v)
		}
		return s, 0
	}
	if s := pbLastString(f, 1); s != "" {
		if v, ok := pbLastVarint(f, 2); ok {
			return s, int(v)
		}
		return s, 0
	}
	return "", 0
}

// parseInfoResponse handles WifiInfoResponse. Two layouts exist:
//
//	A: ssid=2, bssid=3, password=4 (string), security=5 (varint)
//	B: ssid=1, bssid=2, password=3 (string), security=4 (varint)
//
// The security field number disambiguates them: layout A has field 4 as a
// string and field 5 as a varint; layout B has field 4 as a varint.
func parseInfoResponse(p []byte) (ssid, key, bssid string, security uint64) {
	f := pbParse(p)
	key4 := pbLastString(f, 4)
	sec4, hasSec4 := pbLastVarint(f, 4)
	_, hasSec5 := pbLastVarint(f, 5)

	switch {
	case key4 != "" || hasSec5:
		ssid = pbLastString(f, 2)
		bssid = pbLastString(f, 3)
		key = key4
		security, _ = pbLastVarint(f, 5)
	case hasSec4:
		ssid = pbLastString(f, 1)
		bssid = pbLastString(f, 2)
		key = pbLastString(f, 3)
		security = sec4
	default:
		ssid = pbLastString(f, 2)
		bssid = pbLastString(f, 3)
		key = pbLastString(f, 4)
		security, _ = pbLastVarint(f, 5)
	}
	return
}

// parseSetupInfo handles WifiSetupInfo (msg 11): endpoint is field 4, access
// point info is field 5. Field numbers inside the sub-messages are not fully
// documented, so both plausible layouts are tried and the raw bytes are logged
// by the caller.
func parseSetupInfo(p []byte) (ip string, port int, ssid, key, bssid string, security uint64) {
	f := pbParse(p)
	if ep, ok := pbLastBytes(f, 4); ok {
		ef := pbParse(ep)
		ip = pbLastString(ef, 1)
		if v, ok := pbLastVarint(ef, 2); ok {
			port = int(v)
		}
		if ip == "" {
			ip = pbLastString(ef, 2)
			if v, ok := pbLastVarint(ef, 3); ok {
				port = int(v)
			}
		}
	}
	if ap, ok := pbLastBytes(f, 5); ok {
		af := pbParse(ap)
		ssid = pbLastString(af, 2)
		bssid = pbLastString(af, 3)
		key = pbLastString(af, 4)
		security, _ = pbLastVarint(af, 5)
		if ssid == "" {
			ssid = pbLastString(af, 1)
			bssid = pbLastString(af, 2)
			key = pbLastString(af, 3)
			security, _ = pbLastVarint(af, 4)
		}
	}
	return
}

func buildVersionResponse(major, minor uint64) []byte {
	var b []byte
	if major != 0 {
		b = pbAppendVarint(b, 2, major)
	}
	if minor != 0 {
		b = pbAppendVarint(b, 3, minor)
	}
	b = pbAppendVarint(b, 5, wppStatusSuccess)
	return b
}

func buildConnectStatus(status uint64) []byte {
	return pbAppendVarint(nil, 2, status)
}

func buildPingResponse(ts uint64) []byte {
	return pbAppendVarint(nil, 2, ts)
}
