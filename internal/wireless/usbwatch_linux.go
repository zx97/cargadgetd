//go:build linux

package wireless

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/cargadgetd/cargadgetd/internal/cfg"
)

// USBSnapshot is what the status page shows about the wired path. The USB
// daemon is a separate process (systemd unit cargadgetd-aoa) that the wireless
// daemon does not own, so it is observed from the outside: the unit state, the
// gadget mode in configfs, the process table and the USB log.
type USBSnapshot struct {
	// Transport is the configured mode ("usb" or "wireless").
	Transport string
	// ServiceState is systemctl is-active for cargadgetd-aoa.service.
	ServiceState string
	// Mode is the gadget identity: "ask" before AOA, "accessory" once the
	// head unit switched the phone to 18d1:2d00.
	Mode string
	// VendorID is the live idVendor (0x0e8d ask, 0x18d1 accessory).
	VendorID string
	// AccessoryLinked is true when an accessory/ffs function is linked into
	// the active configuration, i.e. the phone answers AOA GET_PROTOCOL.
	AccessoryLinked bool
	// SessionRunning is true while a cargadgetd-md session process runs
	// over FunctionFS (the wireless daemon excluded).
	SessionRunning bool
	// HostPresent is true when the launcher last saw a USB host on the cable.
	HostPresent bool
	// AASeen is true when a wired Android Auto session reached the service
	// discovery. That session is what makes the head unit learn the phone as an
	// Android Auto device, so it is a one-time step of the procedure.
	AASeen bool
	// Log is the tail of the USB daemon log.
	Log []string
	// LogPath is where Log came from.
	LogPath string
}

const usbLogName = "cargadgetd.log"

// USBStatus samples the wired path. Every part degrades to its zero value: a
// missing unit or file must never break the page.
func USBStatus() USBSnapshot {
	var s USBSnapshot
	s.Transport = "usb"
	if c := cfg.Get().Daemon.Transport; c != "" {
		s.Transport = c
	}
	s.ServiceState = unitState("cargadgetd-aoa.service")
	vendor, mode := gadgetState()
	s.VendorID = vendor
	s.LogPath = usbLogPath()
	s.Log = tailLines(s.LogPath, 8)
	// AccessoryLinked and HostPresent when configfs is not readable
	// (g1 is 0770 root:system after a usb-moded restart): fall back to the
	// launcher log, which says what it linked and whether a host is seen.
	if mode == "inconnu" && logMentions(s.Log, "linked accessory") {
		mode = "ask"
	}
	s.Mode = mode
	// 18d1:2d00 is the accessory mode itself. Before the head unit sends AOA
	// START the launcher still links accessory.gs2 while keeping the ask ids,
	// and says so in its log; that is the "ready for the cable" state.
	s.AccessoryLinked = s.VendorID == "0x18d1" || logMentions(s.Log, "linked accessory")
	s.HostPresent = lastHostPresent(s.Log)
	s.SessionRunning = usbSessionRunning()
	s.AASeen = aaSessionSeen()
	return s
}

// aaSessionSeen looks for a completed service discovery in the newest wired
// logs. It reads only the tail of each file: a session writes a few hundred KB
// at most and the marker is near the start of a session.
func aaSessionSeen() bool {
	paths, _ := filepath.Glob("/home/*/cargadgetd*.log")
	sort.Slice(paths, func(i, j int) bool {
		fi, erri := os.Stat(paths[i])
		fj, errj := os.Stat(paths[j])
		if erri != nil || errj != nil {
			return false
		}
		return fi.ModTime().After(fj.ModTime())
	})
	for _, p := range paths {
		f, err := os.Open(p)
		if err != nil {
			continue
		}
		st, err := f.Stat()
		if err != nil {
			f.Close()
			continue
		}
		const tail = 256 << 10
		off := st.Size() - tail
		if off < 0 {
			off = 0
		}
		if _, err := f.Seek(off, io.SeekStart); err != nil {
			f.Close()
			continue
		}
		b, _ := io.ReadAll(io.LimitReader(f, tail))
		f.Close()
		if strings.Contains(string(b), "session ready (SDR received)") {
			return true
		}
	}
	return false
}

func unitState(unit string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "systemctl", "is-active", unit).Output()
	state := strings.TrimSpace(string(out))
	if state != "" {
		return state
	}
	if err != nil {
		return "inconnu"
	}
	return "inconnu"
}

// gadgetState reads the live gadget vendor id. It must not use filepath.Glob:
// Go's Glob opens every intermediate directory, and configfs gadget directories
// are 0771 root:system — unreadable for the user running the daemon, while
// their files (idVendor) are readable. ReadDir on the parent plus a direct
// ReadFile only needs the execute (traverse) bit.
func gadgetState() (vendor, mode string) {
	entries, err := os.ReadDir("/sys/kernel/config/usb_gadget")
	if err != nil {
		return "", "absent"
	}
	for _, e := range entries {
		p := filepath.Join("/sys/kernel/config/usb_gadget", e.Name(), "idVendor")
		b, err := os.ReadFile(p)
		if err != nil {
			// Configfs gadget dirs are 0770 root:system: the file exists but a
			// normal user cannot read it. Do not claim the gadget is absent.
			return "", "inconnu"
		}
		vendor = strings.TrimSpace(string(b))
		switch vendor {
		case "0x18d1":
			mode = "accessory"
		case "0x0e8d":
			mode = "ask"
		default:
			mode = "inconnu"
		}
		return vendor, mode
	}
	return "", "absent"
}

// lastHostPresent reads the launcher's latest USB-host line: "aoa: USB host
// present" or "aoa: no USB host; sleeping until the cable is connected".
func lastHostPresent(lines []string) bool {
	present := false
	for _, l := range lines {
		low := strings.ToLower(l)
		if !strings.Contains(low, "usb host") {
			continue
		}
		present = strings.Contains(low, "present")
	}
	return present
}

// logMentions reports whether any of lines contains want (case-insensitive).
func logMentions(lines []string, want string) bool {
	want = strings.ToLower(want)
	for _, l := range lines {
		if strings.Contains(strings.ToLower(l), want) {
			return true
		}
	}
	return false
}

// usbSessionRunning looks for a cargadgetd-md process that is not the wireless
// daemon. /proc comm is not enough: the binary name is shared. ReadDir("/proc")
// is allowed; another user's cmdline simply fails to read and is skipped.
func usbSessionRunning() bool {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return false
	}
	self := strconv.Itoa(os.Getpid())
	for _, e := range entries {
		if e.Name() == self {
			continue
		}
		b, err := os.ReadFile(filepath.Join("/proc", e.Name(), "cmdline"))
		if err != nil || len(b) == 0 {
			continue
		}
		cmd := strings.ReplaceAll(string(b), "\x00", " ")
		if !strings.Contains(cmd, "cargadgetd-md") {
			continue
		}
		if strings.Contains(cmd, "-wireless") {
			continue
		}
		return true
	}
	return false
}

// RepairUSB starts the wired launcher through the setuid helper. It is the
// one-tap fix for the state that looks like "the car refuses the phone": the
// AOA waiter was not running when the cable was plugged in.
func RepairUSB() error {
	bin := cfg.Get().Gadget.Bin
	if bin == "" {
		return fmt.Errorf("no gadget binary configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "-aoa-service", "start").CombinedOutput()
	if err != nil {
		return fmt.Errorf("service USB : %v (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// usbLogPath returns the log written by the wired launcher and session daemon.
func usbLogPath() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, usbLogName)
}

// tailLines returns at most n non-empty trailing lines of path.
func tailLines(path string, n int) []string {
	if path == "" {
		return nil
	}
	b, err := os.ReadFile(path)
	if err != nil || len(b) == 0 {
		return nil
	}
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		if strings.TrimSpace(l) != "" {
			out = append(out, strings.TrimRight(l, "\r"))
		}
	}
	return out
}
