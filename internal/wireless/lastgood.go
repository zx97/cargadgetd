package wireless

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// lastGood remembers the head unit that last carried a session, so the next
// cycle dials it first.
//
// Measured on the bench: a cycle with the laptop, a headset and two units
// paired spent 11.7s paging the unit in the car park (a 7.7s page timeout plus
// 4s of polling) before reaching the one in front, and the whole connection
// took 41.6s. The unit that ran the previous session is the best available
// guess, and it needs no configuration: it is learned.
//
// The class rule still comes first, so this can never put a computer or a phone
// in front of an audio/video device.
const lastGoodFile = ".cache/cargadgetd-last-peer"

var (
	lastGoodMu   sync.Mutex
	lastGoodAddr string
	lastGoodRead bool
)

func lastGoodPath() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, lastGoodFile)
}

// NoteGood records the unit that just carried a session (RFCOMM or later).
func NoteGood(addr string) {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return
	}
	lastGoodMu.Lock()
	lastGoodAddr, lastGoodRead = addr, true
	lastGoodMu.Unlock()
	path := lastGoodPath()
	if path == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	// Write-then-rename: a reader must never see half an address.
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(addr+"\n"), 0o644); err != nil {
		return
	}
	_ = os.Rename(tmp, path)
}

// LastGood returns the address of the unit that last ran a session, or "".
func LastGood() string {
	lastGoodMu.Lock()
	defer lastGoodMu.Unlock()
	if lastGoodRead {
		return lastGoodAddr
	}
	lastGoodRead = true
	path := lastGoodPath()
	if path == "" {
		return ""
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	lastGoodAddr = strings.TrimSpace(string(b))
	return lastGoodAddr
}

// forgetLastGood clears the memory (tests, and a unit that was unpaired).
func forgetLastGood() {
	lastGoodMu.Lock()
	lastGoodAddr, lastGoodRead = "", true
	lastGoodMu.Unlock()
	_ = os.Remove(lastGoodPath())
}
