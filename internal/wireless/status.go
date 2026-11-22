package wireless

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

// Stage is one step of the connection workflow shown as a diagram.
type Stage struct {
	Label  string
	Detail string
	// State is "done", "current", "failed" or "pending".
	State string
}

// Check is one prerequisite shown on the status page. Detail is the English
// rendering; when the check was set with Setf the template and its arguments
// are kept too, so Localize can re-render the detail in the page language.
type Check struct {
	Name   string
	OK     bool
	Detail string
	tmpl   string
	args   []any
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
	// Steps is a human-readable feed of what the daemon discovered and did,
	// with absolute times, newest last. Localize formats it in the page
	// language from stepEntries.
	Steps       []string
	stepEntries []stepEntry
	// Waiting / WiFiLabel are the few labels the page script needs to write
	// after a poll; server-side so the script holds no language.
	Waiting   string
	WiFiLabel string
	// Stages is the workflow, in order, with the current one marked.
	Stages []Stage
	// Next is the one thing the driver should do now, rendered in a large box.
	Next string
	// NextKind is "ok", "wait" or "action" and drives the box colour.
	NextKind string
	// Banner / BannerCls and PhaseLine are translated server-side so the page
	// script carries no language of its own.
	Banner    string
	BannerCls string
	PhaseLine string
	// BluetoothName / USBName are how the head unit names the phone in each
	// mode; they differ by design and the page says so.
	BluetoothName string
	USBName       string
	// USB is the wired path: its systemd unit, gadget mode, live session and
	// log tail. Observed, not owned, by the wireless daemon.
	USB USBSnapshot
	// USBSvc / USBMode / USBSess are the same three facts already rendered in
	// the page language, so the page script needs no text of its own.
	USBSvc, USBMode, USBSess          string
	USBSvcCls, USBModeCls, USBSessCls string
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
	steps       []stepEntry
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
	s.steps = nil
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
	c.tmpl = ""
	c.args = nil
}

// Setf is Set with a template, so the detail can be translated at render time.
// The template is English.
func (s *Status) Setf(name string, ok bool, format string, args ...any) {
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
	c.tmpl = format
	c.args = args
	c.Detail = fmt.Sprintf(format, args...)
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
	c.tmpl = ""
	c.args = nil
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

// stepEntry is one feed line kept as template plus arguments: the page
// language is only known at render time, so the line cannot be formatted when
// it is produced. Every value comes from what was discovered at runtime.
type stepEntry struct {
	at   time.Time
	tmpl string
	args []any
}

// Step appends one line to the live feed shown on the page. The template is
// English and translated at render time.
func (s *Status) Step(format string, args ...any) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.steps = append(s.steps, stepEntry{at: time.Now(), tmpl: format, args: args})
	if len(s.steps) > 40 {
		s.steps = s.steps[len(s.steps)-40:]
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
	snap.stepEntries = append(snap.stepEntries, s.steps...)
	s.mu.Unlock()

	// Sampling the USB unit forks systemctl and reads /proc: do it outside the
	// lock, and never let it delay the wireless state.
	snap.USB = USBStatus()
	snap.Localize("en")
	return snap
}

// Localize renders every human-readable field in lang, in place. Snapshot
// returns English by default; the HTTP handler calls this with the language the
// browser asks for. The code itself is English so the project can be shared,
// the translations live in i18n.go.
func (s *Snapshot) Localize(lang string) {
	if s == nil {
		return
	}
	s.Steps = s.Steps[:0]
	for _, e := range s.stepEntries {
		s.Steps = append(s.Steps, e.at.Format("15:04:05")+"  "+TF(lang, e.tmpl, e.args...))
	}
	s.Waiting = T(lang, "Waiting")
	if s.WiFiWant != "" {
		s.WiFiLabel = TF(lang, "Join Wi-Fi %s", s.WiFiWant)
	} else {
		s.WiFiLabel = T(lang, "Join Wi-Fi")
	}
	// Stages and the next-step rule look checks up by their English name, so
	// the names are translated last.
	s.Stages = stagesFor(lang, *s)
	s.Banner, s.BannerCls = bannerFor(lang, *s)
	s.PhaseLine = phaseLine(lang, *s)
	s.Next, s.NextKind = nextStep(lang, *s)
	for i := range s.Checks {
		c := &s.Checks[i]
		c.Name = T(lang, c.Name)
		c.Detail = c.detailIn(lang)
	}
	if s.Error != "" {
		s.Error = T(lang, s.Error)
	}
	svc, mode, sess, svcCls, modeCls, sessCls := usbText(lang, s.USB)
	s.USBSvc, s.USBMode, s.USBSess = svc, mode, sess
	s.USBSvcCls, s.USBModeCls, s.USBSessCls = svcCls, modeCls, sessCls
}

// detailIn renders a check detail in lang: a Setf check keeps its template, a
// plain Set check is translated by exact match (dynamic system text such as an
// error string stays as it is).
func (c Check) detailIn(lang string) string {
	if c.tmpl != "" {
		return TF(lang, c.tmpl, c.args...)
	}
	return T(lang, c.Detail)
}

// stagesFor maps the live phase and checks onto the workflow diagram. Labels
// are translated; every value shown comes from what was discovered at runtime.
func stagesFor(lang string, s Snapshot) []Stage {
	order := []string{"bluetooth", "profile", "sdp", "wpp", "wifi", "tcp", "session"}
	labels := []struct{ label, check string }{
		{"Bluetooth paired", "Bluetooth paired"},
		{"Android Auto profile", "Android Auto profile"},
		{"RFCOMM channel", "RFCOMM channel"},
		{"WPP credentials", "WPP Wi-Fi"},
		{"Car Wi-Fi", "Car Wi-Fi"},
		{"TCP + TLS", "Car TCP"},
		{"Android Auto session", "Android Auto session"},
	}
	cur := -1
	for i, ph := range order {
		if s.Phase == ph {
			cur = i
			break
		}
	}
	out := make([]Stage, 0, len(labels))
	for i, l := range labels {
		st := Stage{Label: T(lang, l.label)}
		for _, c := range s.Checks {
			if c.Name == l.check {
				st.Detail = c.detailIn(lang)
				break
			}
		}
		switch {
		case cur < 0 && !s.Running && s.Error != "":
			st.State = "failed"
		case i < cur:
			st.State = "done"
		case i == cur:
			if !s.Running && s.Error != "" {
				st.State = "failed"
			} else {
				st.State = "current"
			}
		default:
			st.State = "pending"
		}
		out = append(out, st)
	}
	return out
}

// nextStep turns the live state into one imperative instruction. The templates
// are English and go through T, so the code stays shareable.
func nextStep(lang string, s Snapshot) (string, string) {
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
		name = "the phone"
		if lang == "fr" {
			name = "the phone"
		}
	}
	// Wired mode: the launcher unit is the first thing that can be wrong, and
	// it is the failure that looks like "the car refuses the phone".
	if s.USB.Transport == "usb" && s.USB.ServiceState != "active" {
		return T(lang, `The USB service is stopped: press "Repair USB", then plug the cable again.`), "action"
	}
	// The head unit only learns that this phone does Android Auto from a wired
	// session (it ignores the AA Bluetooth pairing channel), so the first run
	// necessarily goes through the cable, once.
	if s.USB.Transport == "usb" && !s.USB.AASeen && s.Phase != "session" {
		return T(lang, "First run: plug the USB cable, let Android Auto show up for ~20 s, then unplug. Wireless is enough afterwards."), "action"
	}
	switch {
	case !s.Running && s.Error == "":
		return T(lang, "Start the daemon below."), "action"
	case !s.Running && strings.Contains(s.Error, "no paired car"):
		return TF(lang, "Pair %s with the car (Bluetooth menu on its screen), then press Restart.", name), "action"
	case !s.Running && (strings.Contains(s.Error, "car absent") || strings.Contains(s.Error, "unreachable")):
		return T(lang, "Turn the car on: the phone retries by itself as soon as it appears."), "wait"
	case !s.Running:
		return T(lang, "Start a new attempt."), "action"
	case s.Phase == "session":
		return T(lang, "You are connected. You can drive."), "ok"
	case s.Phase == "bluetooth" || s.Phase == "profile":
		if ok, seen := check("Bluetooth paired"); seen && !ok {
			return T(lang, "Pair the phone in the car's Bluetooth menu first."), "action"
		}
		return TF(lang, "On the car screen: Mobile devices → « %s » → Android Auto. If two identical entries exist, delete the oldest.", name), "action"
	case s.Phase == "wifi":
		return T(lang, "The phone is joining the car Wi-Fi: hands off."), "wait"
	default:
		return T(lang, "Hands off: the phone is talking to the car."), "wait"
	}
}

// Detail is a convenience for building check details.// Detail is a convenience for building check details.
func Detail(format string, args ...any) string {
	return fmt.Sprintf(format, args...)
}
