package cfg

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// Config is the process-wide cargadgetd settings. Empty strings are only
// used for optional/disabled fields (pcm dump, ffs diag, unhandled GAL log).
type Config struct {
	Daemon Daemon
	Video  Video
	Audio  Audio
	USB    USB
	Gadget Gadget
	Apps   Apps
	Music  Music
	Shell  Shell
	GPS    GPS
}

const (
	// TransportUSB is the AOA / FunctionFS phone path.
	TransportUSB = "usb"
	// TransportWireless leaves the gadget down. The MD does not open FunctionFS.
	TransportWireless = "wireless"
)

// NormalizeTransport returns wireless only for that exact mode. Anything else,
// including a typo, stays usb so a bad key cannot drop the cable path.
func NormalizeTransport(v string) string {
	if strings.EqualFold(strings.TrimSpace(v), TransportWireless) {
		return TransportWireless
	}
	return TransportUSB
}

type Daemon struct {
	// Transport is usb or wireless. Unknown values are usb.
	Transport string
	// Listen is tcp://, unix://, or ffs:///. Device USB launchers pass -listen.
	Listen string
	// Control is the JSON control plane.
	Control string
	// Certs is reversegearhead.so (GAL MD PEMs). "embedded" still maps to the default plugin.
	Certs string
	// Peer is the head unit to dial on the wireless path: a Bluetooth address
	// (exact), a device name (substring), or "auto" to let the daemon pick from
	// the paired devices. Empty means auto.
	//
	// Two contracts, and the explicit one is the default: with an address here
	// nothing is guessed, so a paired phone or PC can never take the cycle. The
	// ordering heuristics in internal/wireless only run for "auto".
	Peer string
}

type Video struct {
	// Encoder is "droidmedia" (Sailfish HAL) or "software" (x264).
	Encoder string
	// Size is "auto" to follow the HU, or WxH like "800x480".
	Size string
	// ConfigIndex selects HU video_configs[]; -1 means pick from Size/auto.
	ConfigIndex int
	// FrameMs is the paint + encode interval. 33 ≈ 30fps.
	FrameMs int
	// Live enables the live RGBA encoder. false skips the encode loop.
	Live bool
	// BitrateKbps is the CBR target. 0 follows the frame-size budget (about
	// 4.8 Mbps at 30fps). Some head units reboot or drop the session under a
	// heavy stream, so the rate is a user setting: 1500 kbps keeps text and map
	// lines crisp and cuts the keyframe bursts that stress a car decoder.
	BitrateKbps int
}

type Audio struct {
	Enabled    bool
	ForcePrime bool
	// TickMs is the PCM period in ms. 0 on USB means 2048 frames (~42.7ms @ 48kHz).
	TickMs int
	// Channels is 1 or 2; 0 means use the HU SDR.
	Channels int
	// ConfigIndex selects audio_configs[]; -1 means auto (prefer 48kHz stereo).
	ConfigIndex int
	// Type is auto|media|system|speech.
	Type string
}

type USB struct {
	MediaGapMs int
	// PCMDump is a path prefix for s16le captures; empty disables.
	PCMDump string
	// FFSDiag is a TSV path for FunctionFS write traces; empty disables.
	FFSDiag string
}

type Gadget struct {
	// Path is the configfs gadget directory.
	Path string
	// Bin is gadgetctl / cargadgetd-gadget.
	Bin string
}

type Apps struct {
	// Strict makes UNHANDLED protocol messages fatal.
	Strict bool
	// UnhandledLog is a file for HU→MD events we do not fully parse/route
	// (UNHANDLED, sensor, input). Empty disables.
	UnhandledLog string
}

type Music struct {
	Loopback bool
	Sink     string
}

// Shell selects libcargadgetd_shell_<id>.so. Empty means basic.
type Shell struct {
	ID string
	// DropFrames is how many encode ticks may pass while the shell draw is
	// still inside the plugin before the frame says it is not responding.
	// 0 leaves the last picture up.
	DropFrames int
}

// GPS is the session location client. Backend is geoclue or mock.
type GPS struct {
	Backend string
}

var (
	mu      sync.RWMutex
	current *Config
	loaded  string
)

// Default is the compiled-in baseline. The user YAML is written from this.
func Default() *Config {
	return &Config{
		Daemon: Daemon{
			Transport: TransportUSB,
			Listen:    "tcp://127.0.0.1:5277",
			Control:   "tcp://127.0.0.1:9277",
			Certs:     "/usr/lib/cargadgetd/plugins/reversegearhead.so",
		},
		Video: Video{
			Encoder:     "droidmedia",
			Size:        "auto",
			ConfigIndex: -1,
			FrameMs:     33,
			Live:        true,
			BitrateKbps: 0,
		},
		Audio: Audio{
			Enabled:     true,
			ForcePrime:  false,
			TickMs:      0,
			Channels:    0,
			ConfigIndex: -1,
			Type:        "auto",
		},
		USB: USB{
			MediaGapMs: 0,
		},
		Gadget: Gadget{
			Path: "/sys/kernel/config/usb_gadget/aa",
			Bin:  "/usr/share/cargadgetd/bin/cargadgetd-gadget",
		},
		Apps: Apps{
			Strict: false,
		},
		Shell: Shell{ID: "basic", DropFrames: 20},
		GPS:   GPS{Backend: "geoclue"},
		Music: Music{
			Loopback: true,
			Sink:     "sink.null",
		},
	}
}

// ExpandPath trims p and expands a leading ~/.
func ExpandPath(p string) string {
	p = strings.TrimSpace(p)
	if p != "~" && !strings.HasPrefix(p, "~/") {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return p
	}
	if p == "~" {
		return home
	}
	return filepath.Join(home, p[2:])
}

// Get returns the loaded config, or Default if Load has not run.
func Get() *Config {
	mu.RLock()
	defer mu.RUnlock()
	if current == nil {
		return Default()
	}
	c := *current
	return &c
}

// Ready is true after Load or SetForTest.
func Ready() bool {
	mu.RLock()
	defer mu.RUnlock()
	return current != nil
}

// LoadIfNeeded calls Load("") only when the process has no config yet.
func LoadIfNeeded() error {
	if Ready() {
		return nil
	}
	return Load("")
}

// LoadedFrom is a comma-separated list of files merged by Load, or "defaults".
func LoadedFrom() string {
	mu.RLock()
	defer mu.RUnlock()
	if loaded == "" {
		return "defaults"
	}
	return loaded
}

// Load merges compiled defaults with config files. explicit, if set, is the
// only file read (after defaults) and does not create the user config.
// Otherwise, if ~/.config/cargadgetd/config.yaml is missing, it is written
// from Default(). Then files are applied in order:
//
//	/usr/share/cargadgetd/config.yaml
//	/etc/cargadgetd/config.yaml
//	$XDG_CONFIG_HOME/cargadgetd/config.yaml  (or ~/.config/cargadgetd/config.yaml)
func Load(explicit string) error {
	c := Default()
	if explicit != "" {
		if err := applyFile(c, explicit); err != nil {
			return err
		}
		set(c, explicit)
		return nil
	}
	wrote, writeErr := ensureUserConfig()
	var files []string
	for _, p := range searchPaths() {
		err := applyFile(c, p)
		if err == nil {
			files = append(files, p)
			continue
		}
		if os.IsNotExist(err) {
			continue
		}
		return fmt.Errorf("%s: %w", p, err)
	}
	from := "defaults"
	if len(files) > 0 {
		from = fmt.Sprintf("defaults + %s", joinComma(files))
	}
	if wrote != "" {
		from = fmt.Sprintf("wrote %s; %s", wrote, from)
	} else if writeErr != nil {
		from = fmt.Sprintf("%s (user config not created: %v)", from, writeErr)
	}
	set(c, from)
	return nil
}

// UserPath is ~/.config/cargadgetd/config.yaml (or $XDG_CONFIG_HOME/...).
func UserPath() string {
	dir := userDir()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "config.yaml")
}

func userDir() string {
	xdg := os.Getenv("XDG_CONFIG_HOME")
	if xdg == "" {
		home, _ := os.UserHomeDir()
		if home == "" {
			return ""
		}
		xdg = filepath.Join(home, ".config")
	}
	return filepath.Join(xdg, "cargadgetd")
}

func searchPaths() []string {
	out := []string{
		"/usr/share/cargadgetd/config.yaml",
		"/etc/cargadgetd/config.yaml",
	}
	if p := UserPath(); p != "" {
		out = append(out, p)
	}
	return uniq(out)
}

// ensureUserConfig writes Default() as YAML when the user file is missing.
// It never overwrites an existing file.
func ensureUserConfig() (wrote string, err error) {
	p := UserPath()
	if p == "" {
		return "", nil
	}
	if _, err := os.Stat(p); err == nil {
		return "", nil
	} else if !os.IsNotExist(err) {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return "", err
	}
	b, err := encodeYAML(Default())
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(p, b, 0o644); err != nil {
		return "", err
	}
	return p, nil
}

func uniq(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, p := range in {
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	return out
}

func joinComma(s []string) string { return strings.Join(s, ", ") }

func set(c *Config, from string) {
	mu.Lock()
	current = c
	loaded = from
	mu.Unlock()
}

// SetForTest replaces the process config until t ends. It does not read files.
func SetForTest(t testing.TB, mutate func(*Config)) {
	t.Helper()
	mu.Lock()
	prev := current
	prevFrom := loaded
	c := Default()
	if mutate != nil {
		mutate(c)
	}
	current = c
	loaded = "test"
	mu.Unlock()
	t.Cleanup(func() {
		mu.Lock()
		current = prev
		loaded = prevFrom
		mu.Unlock()
	})
}
