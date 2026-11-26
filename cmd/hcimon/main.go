// Command hcimon records the Bluetooth HCI traffic into a btsnoop file.
//
// Sailfish ships neither btmon nor hcidump, and the interesting question —
// what a head unit actually sends when it tries to reach the phone's Android
// Auto RFCOMM profile — can only be answered from the controller's own view.
// This opens the kernel's HCI monitor channel (root) and writes btsnoop v1, the
// same format BlueZ's btmon produces and that captures/cargadgetd/btsnoop_dump.py
// reads:
//
//	sudo ./hcimon -seconds 120 -out /home/defaultuser/hci.btsnoop
//
// Every record is tagged with the host-side direction (command/ACL out vs
// event/ACL in) so SDP requests and RFCOMM connects show up in order.
package main

import (
	"encoding/binary"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// hci_mon_hdr is struct hci_mon_hdr from the kernel's bluetooth monitor.
type monHdr struct {
	Opcode uint16
	Index  uint16
	Len    uint16
}

const (
	monNewIndex   = 0
	monDelIndex   = 1
	monCommand    = 2
	monEvent      = 3
	monACLTX      = 4
	monACLRX      = 5
	monSCOTX      = 6
	monSCORX      = 7
	monOpenIndex  = 8
	monCloseIndex = 9
	monIndexInfo  = 10
	monHardware   = 11
)

// btsnoop flags: bit 0 clear = sent by the host (command/ACL out),
// set = received by the host (event/ACL in).
const (
	flagSent     = 0
	flagReceived = 1
)

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

func main() {
	seconds := flag.Int("seconds", 120, "how long to record")
	out := flag.String("out", "/home/defaultuser/hci.btsnoop", "btsnoop file to write")
	flag.Parse()

	fd, err := unix.Socket(unix.AF_BLUETOOTH, unix.SOCK_RAW|unix.SOCK_CLOEXEC, unix.BTPROTO_HCI)
	if err != nil {
		log.Fatalf("socket: %v (root?)", err)
	}
	// HCI_DEV_NONE (0xffff) is not exported by x/sys: the monitor channel is
	// not tied to one adapter, so any index would do, but keep the documented
	// value so a future kernel change is recognisable.
	const hciDevNone = 0xffff
	if err := unix.Bind(fd, &unix.SockaddrHCI{Dev: hciDevNone, Channel: unix.HCI_CHANNEL_MONITOR}); err != nil {
		log.Fatalf("bind monitor channel: %v (root?)", err)
	}
	f, err := os.Create(*out)
	if err != nil {
		log.Fatalf("create %s: %v", *out, err)
	}
	// btsnoop v1, datalink 1002 (HCI UART/H4).
	hdr := make([]byte, 16)
	copy(hdr, "btsnoop\x00")
	binary.BigEndian.PutUint32(hdr[8:], 1)
	binary.BigEndian.PutUint32(hdr[12:], 1002)
	if _, err := f.Write(hdr); err != nil {
		log.Fatalf("write header: %v", err)
	}

	stop := make(chan struct{})
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		select {
		case <-sig:
		case <-time.After(time.Duration(*seconds) * time.Second):
		}
		close(stop)
	}()

	buf := make([]byte, 64*1024)
	var records int
	start := time.Now()
	for {
		select {
		case <-stop:
			log.Printf("%d records in %s -> %s", records, time.Since(start).Round(time.Millisecond), *out)
			f.Close()
			return
		default:
		}
		n, _, err := unix.Recvfrom(fd, buf, 0)
		if err != nil {
			if err == unix.EINTR || err == unix.EAGAIN {
				continue
			}
			log.Printf("recv: %v", err)
			break
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
			log.Printf("write: %v", err)
			break
		}
		records++
		if records%200 == 0 {
			fmt.Printf("%d records\r", records)
		}
	}
	f.Close()
}
