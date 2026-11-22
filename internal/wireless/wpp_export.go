package wireless

import "io"

// WPP ids and helpers exported for cmd/fakecar, which plays the head unit half
// of the wireless bootstrap on a Linux host. The wire format and field numbers
// are the ones the MD side parses (see wpp.go).
const (
	WPPStartRequest    = wppStartRequest
	WPPInfoRequest     = wppInfoRequest
	WPPInfoResponse    = wppInfoResponse
	WPPVersionRequest  = wppVersionRequest
	WPPVersionResponse = wppVersionResponse
	WPPConnectStatus   = wppConnectStatus
	WPPPingRequest     = wppPingRequest
	WPPPingResponse    = wppPingResponse
	WPPStatusSuccess   = wppStatusSuccess
)

// WPPPacket is one WPP frame.
type WPPPacket struct {
	ID      uint16
	Payload []byte
}

// ReadWPPPacket reads one frame (2-byte length, 2-byte id, payload).
func ReadWPPPacket(r io.Reader) (WPPPacket, error) {
	p, err := readWPP(r)
	return WPPPacket{ID: p.ID, Payload: p.Payload}, err
}

// WriteWPPPacket writes one frame.
func WriteWPPPacket(w io.Writer, id uint16, payload []byte) error {
	return writeWPP(w, id, payload)
}

// BuildWifiInfoResponse is the head unit's answer to WifiInfoRequest:
// ssid=2, bssid=3, key=4, security=5 (the layout parseInfoResponse expects).
func BuildWifiInfoResponse(ssid, bssid, key string, security uint64) []byte {
	var b []byte
	b = pbAppendString(b, 2, ssid)
	if bssid != "" {
		b = pbAppendString(b, 3, bssid)
	}
	if key != "" {
		b = pbAppendString(b, 4, key)
	}
	return pbAppendVarint(b, 5, security)
}

// BuildStartRequest tells the phone where to open the GAL TCP/TLS session:
// ip=2, port=3.
func BuildStartRequest(ip string, port int) []byte {
	b := pbAppendString(nil, 2, ip)
	return pbAppendVarint(b, 3, uint64(port))
}

// BuildPingResponse echoes a ping timestamp (field 2).
func BuildPingResponse(ts uint64) []byte { return buildPingResponse(ts) }

// BuildVersionResponse answers WifiVersionRequest with success.
func BuildVersionResponse(major, minor uint64) []byte { return buildVersionResponse(major, minor) }

// DescribeWPP renders a packet for logs.
func DescribeWPP(p WPPPacket) string {
	return describeWPP(wppPacket{ID: p.ID, Payload: p.Payload})
}
