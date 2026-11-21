package wireless

import (
	"fmt"
	"sync"
	"time"
)

// Check is one prerequisite shown on the status page.
type Check struct {
	Name   string
	OK     bool
	Detail string
}

// Snapshot is a consistent copy of the status for the web page.
type Snapshot struct {
	Phase    string
	Running  bool
	Started  time.Time
	Checks   []Check
	Error    string
	WPP      []string
	WiFiWant string
}

// Status is the live state of the wireless bootstrap. All methods are safe on a
// nil receiver so the package can be used without a status page.
type Status struct {
	mu       sync.Mutex
	phase    string
	running  bool
	started  time.Time
	checks   map[string]*Check
	order    []string
	err      string
	wpp      []string
	wifiWant string
}

const wppLogMax = 60

// NewStatus returns an empty status.
func NewStatus() *Status {
	return &Status{checks: make(map[string]*Check)}
}

// Reset clears a previous run.
func (s *Status) Reset() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.phase = ""
	s.started = time.Now()
	s.checks = make(map[string]*Check)
	s.order = nil
	s.err = ""
	s.wpp = nil
	s.wifiWant = ""
}

// Set upserts a check.
func (s *Status) Set(name string, ok bool, detail string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	c, exists := s.checks[name]
	if !exists {
		c = &Check{Name: name}
		s.checks[name] = c
		s.order = append(s.order, name)
	}
	c.OK = ok
	c.Detail = detail
}

// Phase sets the current bootstrap stage.
func (s *Status) Phase(p string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.phase = p
	s.mu.Unlock()
}

// Running marks the controller as running.
func (s *Status) Running(v bool) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.running = v
	if v {
		s.started = time.Now()
	}
	s.mu.Unlock()
}

// Error records the last error.
func (s *Status) Error(err error) {
	if s == nil {
		return
	}
	s.mu.Lock()
	if err != nil {
		s.err = err.Error()
	} else {
		s.err = ""
	}
	s.mu.Unlock()
}

// WPP appends one handshake line to the log.
func (s *Status) WPP(line string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.wpp = append(s.wpp, line)
	if len(s.wpp) > wppLogMax {
		s.wpp = s.wpp[len(s.wpp)-wppLogMax:]
	}
	s.mu.Unlock()
}

// SetWiFiWant records the head unit's SSID.
func (s *Status) SetWiFiWant(ssid string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.wifiWant = ssid
	s.mu.Unlock()
}

// WiFiWant returns the head unit's SSID.
func (s *Status) WiFiWant() string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.wifiWant
}

// Snapshot returns a copy for rendering.
func (s *Status) Snapshot() Snapshot {
	if s == nil {
		return Snapshot{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	snap := Snapshot{
		Phase:    s.phase,
		Running:  s.running,
		Started:  s.started,
		Error:    s.err,
		WiFiWant: s.wifiWant,
	}
	for _, name := range s.order {
		snap.Checks = append(snap.Checks, *s.checks[name])
	}
	snap.WPP = append(snap.WPP, s.wpp...)
	return snap
}

// Detail is a convenience for building check details.
func Detail(format string, args ...any) string {
	return fmt.Sprintf(format, args...)
}
