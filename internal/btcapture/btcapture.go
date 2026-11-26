// Package btcapture records Bluetooth HCI traffic into a btsnoop file.
//
// Sailfish ships neither btmon nor hcidump, and what a head unit actually sends
// when it tries to reach the phone's Android Auto RFCOMM profile can only be
// answered from the controller's own view. The kernel's HCI monitor channel
// gives exactly that, to root only — the setuid helper runs it on the daemon's
// behalf so the user only has to press a button.
package btcapture

import (
	"encoding/binary"
	"fmt"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

// monHdr is struct hci_mon_hdr from the kernel's bluetooth monitor.
type monHdr struct {
	Opcode uint16
	Index  uint16
	Len    uint16
}

const (
	monCommand = 2
	monEvent   = 3
	monACLTX   = 4
	monACLRX   = 5
	monSCOTX   = 6
	monSCORX   = 7
	// hciDevNone (HCI_DEV_NONE) is not exported by x/sys.
	hciDevNone = 0xffff
)

// btsnoop flags: bit 0 clear = sent by the host, set = received by the host.
const (
	flagSent     = 0
	flagReceived = 1
)

// h4Type maps a monitor opcode to the H4 type byte and the btsnoop direction.
func h4Type(op uint16) (byte, uint32, bool) {
	switch op {
	case monCommand:
		return 0x01, flagSent, true
	case monEvent:
		return 0x04, flagReceived, true
	case monACLTX:
		return 0x02, flagSent, true
	case monACLRX:
		return 0x02, flagReceived, true
	case monSCOTX:
		return 0x03, flagSent, true
	case monSCORX:
		return 0x03, flagReceived, true
	}
	return 0, 0, false
}

// Record writes the HCI traffic of the whole controller to path for d, and
// returns how many records were written. Root only.
func Record(path string, d time.Duration) (int, error) {
	fd, err := unix.Socket(unix.AF_BLUETOOTH, unix.SOCK_RAW|unix.SOCK_CLOEXEC, unix.BTPROTO_HCI)
	if err != nil {
		return 0, fmt.Errorf("socket: %w (root?)", err)
	}
	defer unix.Close(fd)
	if err := unix.Bind(fd, &unix.SockaddrHCI{Dev: hciDevNone, Channel: unix.HCI_CHANNEL_MONITOR}); err != nil {
		return 0, fmt.Errorf("bind monitor channel: %w (root?)", err)
	}
	f, err := os.Create(path)
	if err != nil {
		return 0, fmt.Errorf("create %s: %w", path, err)
	}
	defer f.Close()
	// btsnoop v1, datalink 1002 (HCI UART/H4).
	hdr := make([]byte, 16)
	copy(hdr, "btsnoop\x00")
	binary.BigEndian.PutUint32(hdr[8:], 1)
	binary.BigEndian.PutUint32(hdr[12:], 1002)
	if _, err := f.Write(hdr); err != nil {
		return 0, err
	}

	deadline := time.Now().Add(d)
	buf := make([]byte, 64*1024)
	records := 0
	for time.Now().Before(deadline) {
		// Bounded wait per read so the deadline is honoured.
		tv := unix.NsecToTimeval(time.Until(deadline).Nanoseconds())
		if tv.Sec > 1 {
			tv = unix.NsecToTimeval(time.Second.Nanoseconds())
		}
		_ = unix.SetsockoptTimeval(fd, unix.SOL_SOCKET, unix.SO_RCVTIMEO, &tv)
		n, _, err := unix.Recvfrom(fd, buf, 0)
		if err != nil {
			if err == unix.EINTR || err == unix.EAGAIN {
				continue
			}
			return records, fmt.Errorf("recv: %w", err)
		}
		if n < 6 {
			continue
		}
		h := monHdr{
			Opcode: binary.LittleEndian.Uint16(buf[0:2]),
			Index:  binary.LittleEndian.Uint16(buf[2:4]),
			Len:    binary.LittleEndian.Uint16(buf[4:6]),
		}
		payload := buf[6:n]
		if int(h.Len) < len(payload) {
			payload = payload[:h.Len]
		}
		typ, flags, ok := h4Type(h.Opcode)
		if !ok {
			continue
		}
		pkt := append([]byte{typ}, payload...)
		rec := make([]byte, 24)
		binary.BigEndian.PutUint32(rec[0:], uint32(len(pkt)))
		binary.BigEndian.PutUint32(rec[4:], uint32(len(pkt)))
		binary.BigEndian.PutUint32(rec[8:], flags)
		binary.BigEndian.PutUint32(rec[12:], 0)
		binary.BigEndian.PutUint64(rec[16:], uint64(time.Now().UnixMicro()))
		if _, err := f.Write(append(rec, pkt...)); err != nil {
			return records, err
		}
		records++
	}
	return records, nil
}
