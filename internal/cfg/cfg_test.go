package cfg

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultMatchesDocumentedZeros(t *testing.T) {
	c := Default()
	if c.Daemon.Transport != TransportUSB || c.Daemon.Listen != "tcp://127.0.0.1:5277" || c.Daemon.Control != "tcp://127.0.0.1:9277" || c.Daemon.Certs != "/usr/lib/cargadgetd/plugins/reversegearhead.so" {
		t.Fatalf("daemon %+v", c.Daemon)
	}
	if c.Video.Encoder != "droidmedia" || c.Video.Size != "auto" || c.Video.ConfigIndex != -1 || c.Video.FrameMs != 33 || !c.Video.Live {
		t.Fatalf("video %+v", c.Video)
	}
	if !c.Audio.Enabled || c.Audio.ForcePrime || c.Audio.TickMs != 0 || c.Audio.Channels != 0 || c.Audio.ConfigIndex != -1 || c.Audio.Type != "auto" {
		t.Fatalf("audio %+v", c.Audio)
	}
	if c.USB.MediaGapMs != 0 || c.USB.PCMDump != "" || c.USB.FFSDiag != "" {
		t.Fatalf("usb %+v", c.USB)
	}
	if c.Gadget.Path != "/sys/kernel/config/usb_gadget/aa" || c.Gadget.Bin != "/usr/share/cargadgetd/bin/cargadgetd-gadget" {
		t.Fatalf("gadget %+v", c.Gadget)
	}
	if c.Apps.Strict || c.Apps.UnhandledLog != "" {
		t.Fatalf("apps %+v", c.Apps)
	}
	if c.Shell.ID != "basic" || c.Shell.DropFrames != 20 {
		t.Fatalf("shell %+v", c.Shell)
	}
	if c.GPS.Backend != "geoclue" {
		t.Fatalf("gps %+v", c.GPS)
	}
	if !c.Music.Loopback || c.Music.Sink != "sink.null" {
		t.Fatalf("music %+v", c.Music)
	}
}

func TestMergeKeepsOmittedAndHonorsZeros(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "overlay.yaml")
	body := []byte(`
audio:
  enabled: false
  tick_ms: 0
  config_index: 0
video:
  frame_ms: 0
  live: false
usb:
  media_gap_ms: 0
shell:
  drop_frames: 0
`)
	if err := os.WriteFile(p, body, 0o644); err != nil {
		t.Fatal(err)
	}
	c := Default()
	if err := applyFile(c, p); err != nil {
		t.Fatal(err)
	}
	if c.Audio.Enabled {
		t.Fatal("enabled: false must apply")
	}
	if c.Audio.TickMs != 0 {
		t.Fatalf("tick_ms=0: %d", c.Audio.TickMs)
	}
	if c.Audio.ConfigIndex != 0 {
		t.Fatalf("config_index=0: %d", c.Audio.ConfigIndex)
	}
	if c.Audio.Type != "auto" {
		t.Fatalf("omitted type: %q", c.Audio.Type)
	}
	if c.Video.FrameMs != 0 {
		t.Fatalf("frame_ms=0: %d", c.Video.FrameMs)
	}
	if c.Video.Live {
		t.Fatal("live: false must apply")
	}
	if c.Video.Size != "auto" {
		t.Fatalf("omitted size: %q", c.Video.Size)
	}
	if c.Shell.DropFrames != 0 || c.Shell.ID != "basic" {
		t.Fatalf("shell %+v", c.Shell)
	}
}

func TestLoadExplicit(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "only.yaml")
	if err := os.WriteFile(p, []byte("video:\n  encoder: software\n  size: \"800x480\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Load(p); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		mu.Lock()
		current = nil
		loaded = ""
		mu.Unlock()
	})
	c := Get()
	if c.Video.Encoder != "software" || c.Video.Size != "800x480" || c.Video.FrameMs != 33 {
		t.Fatalf("%+v", c.Video)
	}
	if c.Shell.DropFrames != 20 {
		t.Fatalf("omitted drop_frames: %d", c.Shell.DropFrames)
	}
	if LoadedFrom() != p {
		t.Fatalf("loaded %q", LoadedFrom())
	}
}

func TestGPSBackendIgnoresOldNeonKey(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "old.yaml")
	if err := os.WriteFile(p, []byte("neon:\n  gps: mock\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := Default()
	want := c.GPS.Backend
	if err := applyFile(c, p); err != nil {
		t.Fatal(err)
	}
	if c.GPS.Backend != want {
		t.Fatalf("backend %q", c.GPS.Backend)
	}
	if err := os.WriteFile(p, []byte("gps:\n  backend: mock\nneon:\n  gps: geoclue\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c = Default()
	if err := applyFile(c, p); err != nil {
		t.Fatal(err)
	}
	if c.GPS.Backend != "mock" {
		t.Fatalf("backend %q", c.GPS.Backend)
	}
}

func TestGetWithoutLoadIsDefault(t *testing.T) {
	mu.Lock()
	prev, prevFrom := current, loaded
	current = nil
	loaded = ""
	mu.Unlock()
	t.Cleanup(func() {
		mu.Lock()
		current = prev
		loaded = prevFrom
		mu.Unlock()
	})
	c := Get()
	if c.Video.FrameMs != 33 || !c.Audio.Enabled {
		t.Fatalf("%+v", c)
	}
	if LoadedFrom() != "defaults" {
		t.Fatalf("%q", LoadedFrom())
	}
}

func TestLoadIfNeededSkipsWhenReady(t *testing.T) {
	SetForTest(t, func(c *Config) { c.Shell.ID = "night" })
	if !Ready() {
		t.Fatal("SetForTest must mark Ready")
	}
	if err := LoadIfNeeded(); err != nil {
		t.Fatal(err)
	}
	if Get().Shell.ID != "night" {
		t.Fatal("LoadIfNeeded must not clobber an already-loaded config")
	}
}

func TestEncodeYAMLRoundTrip(t *testing.T) {
	b, err := encodeYAML(Default())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(b), "# cargadgetd.") {
		t.Fatalf("header: %q", b[:40])
	}
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	c := Default()
	if err := applyFile(c, p); err != nil {
		t.Fatal(err)
	}
	want := Default()
	if *c != *want {
		t.Fatalf("round trip\n got %+v\nwant %+v", c, want)
	}
}

func TestLegacyKeysIgnored(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "old.yaml")
	body := []byte("apps:\n  shells_dir: /tmp/shells\nmusic:\n  player: amber\n  loopback: false\n")
	if err := os.WriteFile(p, body, 0o644); err != nil {
		t.Fatal(err)
	}
	c := Default()
	if err := applyFile(c, p); err != nil {
		t.Fatal(err)
	}
	if c.Music.Loopback || c.Music.Sink != "sink.null" || c.Apps.Strict {
		t.Fatalf("%+v", c)
	}
}

func TestLoadWritesUserDefaultAndDoesNotClobber(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Cleanup(func() {
		mu.Lock()
		current = nil
		loaded = ""
		mu.Unlock()
	})
	if err := Load(""); err != nil {
		t.Fatal(err)
	}
	p := UserPath()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	parsed := Default()
	if err := applyFile(parsed, p); err != nil {
		t.Fatal(err)
	}
	want := Default()
	if *parsed != *want {
		t.Fatalf("wrote file diverged\n got %+v\nwant %+v\n%s", parsed, want, b)
	}
	if Get().Video.FrameMs != 33 || !Get().Audio.Enabled {
		t.Fatalf("%+v", Get())
	}
	if !strings.Contains(LoadedFrom(), "wrote "+p) {
		t.Fatalf("LoadedFrom %q", LoadedFrom())
	}
	custom := []byte("video:\n  frame_ms: 50\n")
	if err := os.WriteFile(p, custom, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Load(""); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(p)
	if string(got) != string(custom) {
		t.Fatal("existing user config was overwritten")
	}
	if Get().Video.FrameMs != 50 {
		t.Fatalf("overlay: frame_ms=%d", Get().Video.FrameMs)
	}
}

func TestSearchPathsOmitsUIYaml(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/tmp/cargadgetd-xdg")
	for _, p := range searchPaths() {
		if strings.HasSuffix(p, "ui.yaml") {
			t.Fatalf("ui.yaml must not be a daemon search path: %v", searchPaths())
		}
	}
}

func TestTransportUnknownIsUSB(t *testing.T) {
	if NormalizeTransport("") != TransportUSB || NormalizeTransport("nope") != TransportUSB || NormalizeTransport("USB") != TransportUSB {
		t.Fatal("unknown transport must stay usb")
	}
	if NormalizeTransport(" wireless ") != TransportWireless || NormalizeTransport("WIRELESS") != TransportWireless {
		t.Fatal("wireless")
	}
	dir := t.TempDir()
	p := filepath.Join(dir, "t.yaml")
	if err := os.WriteFile(p, []byte("daemon:\n  transport: \"wireless\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := Default()
	if err := applyFile(c, p); err != nil {
		t.Fatal(err)
	}
	if c.Daemon.Transport != TransportWireless || c.Daemon.Listen != "tcp://127.0.0.1:5277" {
		t.Fatalf("%+v", c.Daemon)
	}
	if err := os.WriteFile(p, []byte("daemon:\n  transport: cable\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c = Default()
	if err := applyFile(c, p); err != nil {
		t.Fatal(err)
	}
	if c.Daemon.Transport != TransportUSB {
		t.Fatalf("typo transport %q", c.Daemon.Transport)
	}
}

func TestExpandPath(t *testing.T) {
	if ExpandPath("  /tmp/x  ") != "/tmp/x" {
		t.Fatal("trim")
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no home")
	}
	if got := ExpandPath("~/.local/cargadgetd/apps/shells"); got != filepath.Join(home, ".local/cargadgetd/apps/shells") {
		t.Fatalf("tilde: %q", got)
	}
}

// daemon.peer selects the head unit: an address is exact, "auto" keeps the
// discovery heuristics. It must survive a load and a save, or a settings menu
// that writes it would silently lose the choice.
func TestDaemonPeerRoundTrip(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "peer.yaml")
	if err := os.WriteFile(p, []byte("daemon:\n  peer: \" AA:BB:CC:DD:EE:FF \"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := Default()
	if err := applyFile(c, p); err != nil {
		t.Fatal(err)
	}
	if c.Daemon.Peer != "AA:BB:CC:DD:EE:FF" {
		t.Fatalf("peer = %q, want the trimmed address", c.Daemon.Peer)
	}
	b, err := encodeYAML(c)
	if err != nil {
		t.Fatal(err)
	}
	c2 := Default()
	if err := applyFile(c2, writeTemp(t, dir, "back.yaml", b)); err != nil {
		t.Fatal(err)
	}
	if c2.Daemon.Peer != "AA:BB:CC:DD:EE:FF" {
		t.Fatalf("peer lost in the round trip: %q\n%s", c2.Daemon.Peer, b)
	}
}

// writeTemp drops b in dir under name and returns the path.
func writeTemp(t *testing.T, dir, name string, b []byte) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// 1500 kbps is the only rate ever run end to end on the bench units; the
// frame-size budget (~4.8 Mbps) is the rate that rebooted a car decoder, so it
// must be an explicit choice. 0 in the file still means it.
func TestDefaultBitrateIsTheValidatedOne(t *testing.T) {
	if got := Default().Video.BitrateKbps; got != 1500 {
		t.Fatalf("default bitrate = %d, want 1500", got)
	}
	dir := t.TempDir()
	explicitZero := writeTemp(t, dir, "zero.yaml", []byte("video:\n  bitrate_kbps: 0\n"))
	c := Default()
	if err := applyFile(c, explicitZero); err != nil {
		t.Fatal(err)
	}
	if c.Video.BitrateKbps != 0 {
		t.Fatalf("explicit bitrate_kbps: 0 became %d", c.Video.BitrateKbps)
	}
	other := writeTemp(t, dir, "high.yaml", []byte("video:\n  bitrate_kbps: 3000\n"))
	c = Default()
	if err := applyFile(c, other); err != nil {
		t.Fatal(err)
	}
	if c.Video.BitrateKbps != 3000 {
		t.Fatalf("explicit bitrate_kbps: 3000 became %d", c.Video.BitrateKbps)
	}
}
