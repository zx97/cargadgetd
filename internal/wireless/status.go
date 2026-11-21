package wireless

import (
	"fmt"
	"strings"
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
	// Next is the one thing the driver should do now, rendered in a large box.
	Next string
	// NextKind is "ok", "wait" or "action" and drives the box colour.
	NextKind string
	// BluetoothName / USBName are how the head unit names the phone in each
	// mode; they differ by design and the page says so.
	BluetoothName string
	USBName       string
	// USB is the wired path: its systemd unit, gadget mode, live session and
	// log tail. Observed, not owned, by the wireless daemon.
	USB USBSnapshot
}

// Status is the live state of the wireless bootstrap. All methods are safe on a
// nil receiver so the package can be used without a status page.
type Status struct {
	mu          sync.Mutex
	phase       string
	running     bool
	started     time.Time
	checks      map[string]*Check
	order       []string
	static      map[string]*Check
	staticOrder []string
	err         string
	wpp         []string
	wifiWant    string
	btName      string
	usbName     string
}

const wppLogMax = 60

// NewStatus returns an empty status.
func NewStatus() *Status {
	return &Status{
		checks: make(map[string]*Check),
		static: make(map[string]*Check),
	}
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

// SetStatic upserts a check that survives Reset (certificates, hardware).
func (s *Status) SetStatic(name string, ok bool, detail string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	c, exists := s.static[name]
	if !exists {
		c = &Check{Name: name}
		s.static[name] = c
		s.staticOrder = append(s.staticOrder, name)
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

// SetNames records how the head unit names the phone over Bluetooth and over
// USB, so the page can point at the right one.
func (s *Status) SetNames(bt, usb string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.btName, s.usbName = bt, usb
	s.mu.Unlock()
}

// Snapshot returns a copy for rendering.
func (s *Status) Snapshot() Snapshot {
	if s == nil {
		return Snapshot{}
	}
	s.mu.Lock()
	snap := Snapshot{
		Phase:         s.phase,
		Running:       s.running,
		Started:       s.started,
		Error:         s.err,
		WiFiWant:      s.wifiWant,
		BluetoothName: s.btName,
		USBName:       s.usbName,
	}
	for _, name := range s.staticOrder {
		snap.Checks = append(snap.Checks, *s.static[name])
	}
	for _, name := range s.order {
		snap.Checks = append(snap.Checks, *s.checks[name])
	}
	snap.WPP = append(snap.WPP, s.wpp...)
	s.mu.Unlock()

	// Sampling the USB unit forks systemctl and reads /proc: do it outside the
	// lock, and never let it delay the wireless state.
	snap.USB = USBStatus()
	snap.Next, snap.NextKind = nextStep(snap)
	return snap
}

// nextStep turns the live state into one imperative instruction. The driver
// should never have to read the check list to know what to do.
func nextStep(s Snapshot) (string, string) {
	check := func(name string) (bool, bool) {
		for _, c := range s.Checks {
			if c.Name == name {
				return c.OK, true
			}
		}
		return false, false
	}
	name := s.BluetoothName
	if name == "" {
		name = "le téléphone"
	}
	// Nothing can work without the Bluetooth bond: say so before anything else.
	if s.Error != "" && strings.Contains(s.Error, "aucune voiture appairée") {
		return fmt.Sprintf("Appaire %s à la voiture (menu Bluetooth de l'écran), puis touche « Redémarrer ».", name), "action"
	}
	// Wired mode: the launcher unit is the first thing that can be wrong, and
	// it is the failure that looks like "the car refuses the phone".
	if s.USB.Transport == "usb" && s.USB.ServiceState != "active" {
		return "Le service USB est arrêté : touche « Réparer l'USB », puis rebranche le câble.", "action"
	}
	switch {
	case !s.Running && s.Error == "":
		return "Touche « Démarrer » ci-dessous.", "action"
	case !s.Running && strings.Contains(s.Error, "voiture absente"):
		return "Allume le contact de la voiture : le téléphone réessaie tout seul dès qu'elle apparaît.", "wait"
	case !s.Running:
		return "Touche « Démarrer » pour relancer une tentative.", "action"
	case s.Phase == "session":
		return "C'est connecté. Tu peux rouler.", "ok"
	case s.Phase == "bluetooth" || s.Phase == "profile":
		if ok, seen := check("Bluetooth appairé"); seen && !ok {
			return "Appaire d'abord le téléphone à la voiture en Bluetooth.", "action"
		}
		return fmt.Sprintf("Sur l'écran de la voiture : Appareils mobiles → « %s » → Android Auto.", name), "action"
	case s.Phase == "wifi":
		return "Le téléphone rejoint le Wi-Fi de la voiture : ne touche à rien.", "wait"
	default:
		return "Ne touche à rien : le téléphone parle à la voiture.", "wait"
	}
}

// Detail is a convenience for building check details.
func Detail(format string, args ...any) string {
	return fmt.Sprintf(format, args...)
}
