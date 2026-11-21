package wireless

// SDPAAChannel asks addr for its Android Auto SDP record and returns the
// RFCOMM channel it advertises. Exported for cmd/sdpprobe, which runs on a
// Linux host to learn a head unit's channel without a phone.
func SDPAAChannel(addr string, logf func(string, ...any)) (uint8, error) {
	return sdpRFCOMMChannel(addr, logf)
}
