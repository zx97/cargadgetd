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

// BuildWifiInfoResponse is the head unit's answer to WifiInfoRequest, with the
// real field numbers: ssid=1, key=2, bssid=3, security_mode=4,
// access_point_type=5. The BSSID field is sent even when empty: the schema
// marks it required and a strict parser rejects the whole message otherwise.
func BuildWifiInfoResponse(ssid, bssid, key string, security uint64, apType uint64) []byte {
	var b []byte
	b = pbAppendString(b, 1, ssid)
	b = pbAppendString(b, 2, key)
	b = pbAppendString(b, 3, bssid)
	b = pbAppendVarint(b, 4, security)
	return pbAppendVarint(b, 5, apType)
}

// BuildStartRequest tells the phone where to open the GAL TCP/TLS session:
// ip_address=1, port=2, start_request_reason=3.
func BuildStartRequest(ip string, port int) []byte {
	b := pbAppendString(nil, 1, ip)
	b = pbAppendVarint(b, 2, uint64(port))
	return pbAppendVarint(b, 3, 0) // USER_REQUEST
}

// BuildPingResponse echoes a ping timestamp (field 2).
func BuildPingResponse(ts uint64) []byte { return buildPingResponse(ts) }

// BuildVersionResponse answers WifiVersionRequest with success.
func BuildVersionResponse(major, minor uint64) []byte { return buildVersionResponse(major, minor) }

// DescribeWPP renders a packet for logs.
func DescribeWPP(p WPPPacket) string {
	return describeWPP(wppPacket{ID: p.ID, Payload: p.Payload})
}
