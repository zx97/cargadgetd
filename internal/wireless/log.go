package wireless

import (
	"encoding/hex"
	"fmt"
	"time"
)

// hexPreview renders packet bytes for the log: full hex up to limit bytes, then
// a byte count. It makes a WPP exchange decodable offline without a tcpdump.
func hexPreview(b []byte) string {
	const limit = 192
	if len(b) <= limit {
		return hex.EncodeToString(b)
	}
	return fmt.Sprintf("%s…(%d bytes)", hex.EncodeToString(b[:limit]), len(b))
}

// sinceText formats an elapsed time for the log.
func sinceText(t time.Time) string {
	return time.Since(t).Round(time.Millisecond).String()
}
