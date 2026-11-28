package wireless

import (
	"fmt"
	"time"
)

// Hero is what the page says first, in one line and one colour.
type Hero struct {
	Kind  string // ok | wait | ko
	Title string
	Hint  string
}

// Chip is one live fact, rendered as a big touch target.
type Chip struct {
	Kind   string // ok | wait | ko
	Text   string
	Action string // "" or the form action the chip posts to
}

// rawLive carries the facts the hero and the chips are built from; the texts are
// produced in Localize so the page script never holds a language.
type rawLive struct {
	bt       BTSnapshot
	ssid, ip string
}

// readLive samples Bluetooth and Wi-Fi. Both are cheap and bounded; the page
// polls, so the Bluetooth read is cached for a couple of seconds.
func readLive() rawLive {
	ssid, ip := LiveWiFi()
	return rawLive{bt: ReadBTCached(2 * time.Second), ssid: ssid, ip: ip}
}

// bridge is how a device class is named in the hero.
func (r rawLive) carName() (name string, connected bool) {
	for _, d := range r.bt.Devices {
		if d.LooksLikeHeadUnit() {
			if name == "" || d.Connected {
				name = d.Name
				connected = d.Connected
			}
		}
	}
	if name == "" {
		name = "the car"
	}
	return name, connected
}

// heroFor decides what the driver must read first. Order matters: with the
// adapter off, nothing else on the page is true.
func heroFor(lang string, phase string, running bool, r rawLive, next string) Hero {
	switch {
	case r.bt.Err != "":
		return Hero{"ko", T(lang, "Bluetooth unavailable"), T(lang, "BlueZ is not answering; reboot the phone if it persists.")}
	case !r.bt.Present:
		return Hero{"ko", T(lang, "No Bluetooth adapter"), T(lang, "The phone reports no Bluetooth controller.")}
	case !r.bt.Powered:
		return Hero{"ko", T(lang, "Bluetooth is off"), T(lang, "Turn Bluetooth on in the phone settings, then touch “Restart”.")}
	case running || phase == "session":
		return Hero{"ok", T(lang, "Android Auto connected"), T(lang, "Use the car's screen. Internet stays on the phone's data.")}
	}
	if name, connected := r.carName(); connected {
		return Hero{"wait", TF(lang, "%s is paired and connected", name), next}
	} else if name != "the car" {
		return Hero{"wait", TF(lang, "%s is paired, not connected", name), T(lang, "Switch the ignition on, or touch “Restart”.")}
	}
	return Hero{"ko", T(lang, "No car paired yet"), T(lang, "Pair the phone from the car's Bluetooth menu, then touch “Restart”.")}
}

// chipsFor renders the three live facts the driver can act on.
func chipsFor(lang string, phase string, running bool, r rawLive) []Chip {
	// Bluetooth
	bt := Chip{Kind: "ko", Text: T(lang, "Bluetooth: off"), Action: "/bt-debug"}
	switch {
	case r.bt.Err != "":
		bt = Chip{Kind: "ko", Text: T(lang, "Bluetooth: unavailable"), Action: "/bt-debug"}
	case !r.bt.Present:
		bt = Chip{Kind: "ko", Text: T(lang, "Bluetooth: no adapter"), Action: "/bt-debug"}
	case r.bt.Powered:
		n := len(r.bt.Devices)
		bt = Chip{Kind: "ok", Text: TF(lang, "Bluetooth: on (%d device(s))", n), Action: "/bt-debug"}
	}
	// Wi-Fi
	wifi := Chip{Kind: "wait", Text: T(lang, "Wi-Fi: not connected")}
	if r.ssid != "" {
		wifi = Chip{Kind: "ok", Text: TF(lang, "Wi-Fi: %s", r.ssid)}
		if r.ip != "" {
			wifi.Text = TF(lang, "Wi-Fi: %s (%s)", r.ssid, r.ip)
		}
	}
	// Session
	sess := Chip{Kind: "wait", Text: T(lang, "Session: idle")}
	if running || phase == "session" {
		sess = Chip{Kind: "ok", Text: T(lang, "Session: active")}
	}
	return []Chip{bt, wifi, sess}
}

// heroChips fills the page's first screen. Called from Localize so both the
// server-rendered page and the polled JSON carry the same, translated answer.
func (s *Snapshot) heroChips(lang string) {
	h := heroFor(lang, s.Phase, s.Running, s.live, s.Next)
	s.Hero = h
	s.Chips = chipsFor(lang, s.Phase, s.Running, s.live)
}

var _ = fmt.Sprintf
