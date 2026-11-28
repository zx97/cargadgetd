package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"github.com/cargadgetd/cargadgetd/internal/aaextract"
	"image"
	"image/color"
	"image/draw"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cargadgetd/cargadgetd/internal/cfg"
	"github.com/cargadgetd/cargadgetd/internal/control"
	"github.com/cargadgetd/cargadgetd/internal/ffs"
	"github.com/cargadgetd/cargadgetd/internal/livevid"
	"github.com/cargadgetd/cargadgetd/internal/loopback"
	"github.com/cargadgetd/cargadgetd/internal/rgh"
	"github.com/cargadgetd/cargadgetd/internal/session"
	"github.com/cargadgetd/cargadgetd/internal/shell"
	aassl "github.com/cargadgetd/cargadgetd/internal/ssl"
	"github.com/cargadgetd/cargadgetd/internal/usbcable"
	"github.com/cargadgetd/cargadgetd/internal/usbgadget"
	"github.com/cargadgetd/cargadgetd/internal/wireless"
)

// Set by release builds via -ldflags.
var (
	version = "dev"
	commit  = "unknown"
)

func main() {
	listenFlag := flag.String("listen", "", "MD listen (tcp://, unix://, or ffs:///; default: daemon.listen)")
	controlFlag := flag.String("control", "", "JSON control listen (default: daemon.control)")
	certDirFlag := flag.String("certs", "", "reversegearhead.so path (default: daemon.certs)")
	configPath := flag.String("config", "", "YAML config file (default: search path after compiled defaults)")
	autoAV := flag.Bool("auto-av", true, "open AV channels and stream livevid after SDR")
	warmVideo := flag.Bool("warm-video", true, "create the hardware encoder once at start-up, before any car connects")
	wirelessFlag := flag.Bool("wireless", false, "connect to a paired head unit over Bluetooth/Wi-Fi instead of USB")
	dialFlag := flag.String("dial", "", "dial a head unit's AAP endpoint directly (tcp://host:port), no Bluetooth pairing needed")
	wirelessName := flag.String("wireless-name", "", "paired head unit Bluetooth name (default: the paired device advertising the Android Auto UUID)")
	wirelessAddr := flag.String("wireless-addr", "", "paired head unit Bluetooth address")
	wirelessChannel := flag.Int("wireless-channel", 0, "head unit AA RFCOMM channel (0 = discover over SDP)")
	wirelessSSID := flag.String("wireless-ssid", "", "require this Wi-Fi SSID (default: the one from the head unit)")
	wirelessIface := flag.String("wireless-iface", "wlan0", "Wi-Fi interface")
	wirelessWeb := flag.Int("wireless-web", 9288, "wireless status page port (127.0.0.1)")
	wirelessPassive := flag.Bool("wireless-passive", false, "wait for the head unit to connect to our AA profile; never page or probe")
	logFile := flag.String("log-file", "", "log to this file, reopening it if it is deleted or rotated")
	showVersion := flag.Bool("version", false, "print version and exit")
	certInfo := flag.Bool("cert-info", false, "print the certificate probe and exit")
	certsFetch := flag.Bool("certs-fetch", false, "download the Android Auto APK, extract the real MD certificate pair, and exit")
	flag.Parse()
	// Millisecond timestamps: the wireless log is read to compare phase
	// durations, and whole seconds hide the interesting gaps.
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)

	// Own the log file when asked: a shell redirect would keep writing into a
	// deleted inode, so a removed log would silently swallow the session.
	if *logFile != "" {
		lw := &reopenWriter{path: *logFile}
		log.SetOutput(lw)
		defer lw.Close()
		log.Printf("log file: %s", *logFile)
	}

	if *showVersion {
		log.Printf("cargadgetd %s (%s)", version, commit)
		return
	}
	if err := cfg.Load(*configPath); err != nil {
		log.Fatalf("config: %v", err)
	}
	conf0 := cfg.Get()
	listen := effectiveListen(pick(*listenFlag, conf0.Daemon.Listen), conf0.Daemon.Listen, conf0.Daemon.Transport)
	controlAddr := pick(*controlFlag, conf0.Daemon.Control)
	certDir := pick(*certDirFlag, conf0.Daemon.Certs)
	log.Printf("cargadgetd %s (%s) config=%s transport=%s listen=%s", version, commit, cfg.LoadedFrom(), conf0.Daemon.Transport, listen)

	// Prove the vendor encoder before a car is attached (see warm.go).
	if *warmVideo && conf0.Video.Live {
		w, h := 1280, 720
		if s := conf0.Video.Size; s != "" && s != "auto" {
			var ww, hh int
			if _, err := fmt.Sscanf(s, "%dx%d", &ww, &hh); err == nil && ww > 0 && hh > 0 {
				w, h = ww, hh
			}
		}
		go warmEncoder(w, h, 30, conf0.Video.BitrateKbps, conf0.Video.Encoder)
	}

	// The real Google pair, extracted from the Android Auto APK, wins when it
	// is present: it is the certificate the head units actually trust, and it
	// can be refreshed when Google rotates it. The -certs plugin stays as the
	// offline fallback and for a bench that pins a known-good pair.
	mat, err := aaextract.Open(rgh.ConfigDefault)
	if err != nil {
		mat, err = rgh.Open(certDir)
		if err != nil {
			log.Fatalf("certs: %v", err)
		}
		log.Printf("no extracted Android Auto pair (%v); using %s", err, mat.Path)
	}
	log.Printf("using MD certs from %s (%d + %d bytes) expires_in=%ds",
		mat.Path, len(mat.Cert), len(mat.Key), mat.ExpiresIn)
	if *certsFetch {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		dir, err := aaextract.InstallDownload(ctx, rgh.ConfigDefault)
		if err != nil {
			log.Fatalf("certs-fetch: %v", err)
		}
		mat, err := aaextract.Open(rgh.ConfigDefault)
		if err != nil {
			log.Fatalf("certs-fetch: installed in %s but will not load: %v", dir, err)
		}
		log.Printf("certs-fetch: %s (expires_in=%ds)\n%s", mat.Path, mat.ExpiresIn, mat.Probe)
		return
	}
	// One daemon at a time: two encoders (wireless projecting + USB on a
	// charger) crash Codec2 on this phone. Claimed after the commands that
	// only read the certificate and exit.
	claimSingleInstance()

	if *certInfo {
		if mat.Probe != "" {
			fmt.Print(mat.Probe)
			if !strings.HasSuffix(mat.Probe, "\n") {
				fmt.Println()
			}
		}
		return
	}

	ctl := control.New()
	var mdMu = struct {
		md *session.MD
	}{}

	ctl.Handle("status", func(req control.Request) control.Response {
		ok := mdMu.md != nil
		b, _ := json.Marshal(map[string]any{
			"session":    ok,
			"certs":      mat.Path,
			"expires_in": mat.ExpiresIn,
		})
		return control.Response{OK: true, Data: b}
	})
	ctl.Handle("open_av", func(req control.Request) control.Response {
		if mdMu.md == nil {
			return control.Response{OK: false, Error: "no session"}
		}
		if err := mdMu.md.OpenVideo(); err != nil {
			return control.Response{OK: false, Error: err.Error()}
		}
		if err := mdMu.md.OpenAudio(); err != nil {
			return control.Response{OK: false, Error: err.Error()}
		}
		if err := mdMu.md.OpenInput(); err != nil {
			return control.Response{OK: false, Error: err.Error()}
		}
		if mdMu.md.SensorCh != 0 {
			if err := mdMu.md.OpenSensor(); err != nil {
				return control.Response{OK: false, Error: err.Error()}
			}
		}
		return control.Response{OK: true}
	})

	cn, ca := parseAddr(controlAddr)
	if err := ctl.Listen(cn, ca); err != nil {
		log.Fatalf("control: %v", err)
	}
	log.Printf("control on %s %s", cn, ca)

	// The phone's Bluetooth identity: announced on the Android Auto Bluetooth
	// channel so the head unit ties its USB (and Wi-Fi) sessions to the same
	// Bluetooth bond — that link is what makes wireless AA available.
	btName, btAddr, usbName := wireless.LocalNames()
	log.Printf("wireless: car sees bluetooth name=%q addr=%s, usb name=%q", btName, btAddr, usbName)

	serve := func(rw io.ReadWriteCloser, peer string, usbBulk bool, onReady func()) {
		defer rw.Close()
		log.Printf("HU connected from %s", peer)
		if usbBulk {
			on, src, raw := usbcable.Probe()
			log.Printf("handshake: vbus_online=%v src=%s raw=%q", on, src, raw)
		}
		ssl, err := aassl.NewServerPEM(mat.Cert, mat.Key)
		if err != nil {
			log.Printf("ssl: %v", err)
			return
		}
		defer ssl.Close()
		md := session.NewMD(rw, ssl, log.Default())
		md.SetUSBBulk(usbBulk)
		if !usbBulk {
			// The official phone pings once a second on wireless links and only
			// answers the head unit's on USB; reproduce that, and log wlan0 for
			// the duration of the session so a dead link is distinguishable
			// from a mute head unit.
			md.EnablePings(time.Second)
			linkCtx, linkStop := context.WithCancel(context.Background())
			defer linkStop()
			go wireless.WatchLink(linkCtx, *wirelessIface, log.Printf, func() {
				md.Abort(fmt.Errorf("%w (the phone left the head unit's access point)", session.ErrHeadUnitSilent))
			})
		}
		md.SetLocalBluetooth(btAddr, btName)
		// Same vendor/model split as the USB descriptors, so the head unit
		// shows "<vendor> <model>" for the Bluetooth entry, the USB entry and
		// the AA entry alike.
		vendor := wireless.Vendor()
		md.SetDeviceInfo(wireless.ModelFromAlias(btName, vendor), vendor)
		if usbBulk {
			// Stock Android Auto also connects the head unit's Bluetooth link
			// during the session (its car-Bluetooth service). That active link
			// is what lets the HU merge its USB device entry with the Bluetooth
			// bond. Give AOA a couple of seconds to settle first.
			go func() {
				time.Sleep(2 * time.Second)
				wireless.ConnectHeadUnit("", 3, log.Printf)
			}()
		}
		mdMu.md = md
		defer func() { mdMu.md = nil }()
		runSession(md, *autoAV, usbBulk, onReady)
	}

	// -dial: the bench path when the head unit offers its access point by hand
	// (SSID and passphrase on its screen) instead of through the Bluetooth
	// handshake. The unit is the AAP server; we are the passive TLS side, the
	// same role we play after a WPP exchange.
	if *dialFlag != "" {
		addr := strings.TrimPrefix(*dialFlag, "tcp://")
		log.Printf("dial: connecting to %s (no Bluetooth)", addr)
		conn, err := net.DialTimeout("tcp", addr, 10*time.Second)
		if err != nil {
			log.Fatalf("dial %s: %v", addr, err)
		}
		log.Printf("dial: connected to %s", addr)
		serve(conn, "dial:"+addr, false, nil)
		log.Printf("dial: session over")
		return
	}

	if *wirelessFlag {
		// Which head unit to dial, and why. An explicit address (flag or
		// daemon.peer) is exact; "auto" leaves the choice to the candidate
		// ordering, which is only as good as its heuristics.
		peer := wireless.ResolvePeer(*wirelessName, *wirelessAddr, cfg.Get().Daemon.Peer)
		log.Printf("wireless: peer from %s name=%q addr=%q channel=%d iface=%s",
			peer.Why, peer.Name, peer.Addr, *wirelessChannel, *wirelessIface)
		st := wireless.NewStatus()
		st.SetNames(btName, usbName)
		// The "Record Bluetooth" button: the monitor channel needs root, so the
		// setuid helper does the recording while the page stays responsive.
		st.SetCapture(func() error {
			bin := cfg.Get().Gadget.Bin
			if bin == "" {
				return fmt.Errorf("no helper configured (gadget.bin)")
			}
			out := fmt.Sprintf("/home/defaultuser/hci-%s.btsnoop", time.Now().Format("20060102-150405"))
			cmd := exec.Command(bin, "-bt-capture", out, "-bt-capture-seconds", "180")
			cmd.Env = os.Environ()
			if err := cmd.Start(); err != nil {
				return fmt.Errorf("capture: %w", err)
			}
			log.Printf("bt capture: recording 180s to %s (helper pid %d)", out, cmd.Process.Pid)
			go func() {
				if err := cmd.Wait(); err != nil {
					log.Printf("bt capture: %v", err)
				}
			}()
			return nil
		})
		if mat.ExpiresIn < 0 {
			st.SetStatic("MD certificate", false, fmt.Sprintf("expired (expires_in=%ds)", mat.ExpiresIn))
		} else {
			st.SetStatic("MD certificate", true, fmt.Sprintf("valid (%ds)", mat.ExpiresIn))
		}
		ctl := wireless.NewController(wireless.Options{
			Name:      peer.Name,
			Address:   peer.Addr,
			Channel:   uint8(*wirelessChannel),
			Interface: *wirelessIface,
			SSID:      *wirelessSSID,
			Passive:   *wirelessPassive,
			Logf:      log.Printf,
		}, st, func(conn net.Conn) {
			// "Session Android Auto" only turns green when the head unit has
			// really started it (SDR received), not when TCP merely connected.
			serve(conn, "wireless", false, func() {
				st.Set("Android Auto session", true, "open (SDR received)")
			})
		})
		go ctl.Run(context.Background())
		addr := fmt.Sprintf("127.0.0.1:%d", *wirelessWeb)
		restartFn := func() {
			go func() {
				time.Sleep(500 * time.Millisecond)
				if err := wireless.RestartProcess(); err != nil {
					log.Printf("wireless restart: %v", err)
				}
			}()
		}
		go func() {
			log.Printf("wireless status: http://%s/", addr)
			if err := http.ListenAndServe(addr, st.Handler(ctl.Start, ctl.Stop, ctl.JoinWiFi, restartFn)); err != nil {
				log.Printf("wireless status http: %v", err)
			}
		}()
		select {}
	}

	if strings.HasPrefix(listen, "ffs://") {
		dir := strings.TrimPrefix(listen, "ffs://")
		if dir == "" {
			dir = "/dev/usb-ffs/aa"
		}
		log.Printf("MD listening on FunctionFS %s (ep1 write / ep2 read)", dir)
		unplug := make(chan struct{})
		go func() {
			usbcable.WaitDisconnect(nil)
			close(unplug)
		}()
		exitUnplug := func() {
			_, src, raw := usbcable.Probe()
			log.Printf("USB cable disconnected — exiting (src=%s raw=%q)", src, raw)
			stopGadgetAgent()
		}
		// Sessions die on bus events (DISABLE/SUSPEND) or HU exit. Reopen the
		// bulk endpoints while VBUS is still present. Cable unplug exits the
		// process so gadgetctl can restore usb-moded for the next prompt.
		for {
			select {
			case <-unplug:
				exitUnplug()
				return
			default:
			}
			eps, err := ffs.OpenAccessory(dir)
			if err != nil {
				log.Printf("ffs open: %v", err)
				select {
				case <-unplug:
					exitUnplug()
					return
				case <-time.After(2 * time.Second):
				}
				continue
			}
			stop := make(chan struct{})
			go func() {
				select {
				case ferr := <-eps.Fatal():
					log.Printf("ffs fatal: %v — closing endpoints", ferr)
					_ = eps.Close()
				case <-unplug:
					_ = eps.Close()
				case <-stop:
				}
			}()
			serve(eps, "usb-ffs:"+dir, true, nil)
			close(stop)
			wasActive := eps.Active()
			_ = eps.Close()
			on, src, raw := usbcable.Probe()
			log.Printf("session done: hu_bytes=%v vbus_online=%v src=%s raw=%q", wasActive, on, src, raw)
			select {
			case <-unplug:
				exitUnplug()
				return
			default:
			}
			// A single VBUS=0 sample is not unplug — AOA mode-switch and
			// Subaru leaving AA (HU home) both dip online while the cable
			// is still in. Exiting here tore the gadget down so the next
			// Android Auto tap did nothing. WaitDisconnect's 3s debounce
			// is the unplug signal (the <-unplug case above).
			if !on {
				log.Printf("VBUS off after session (hu_bytes=%v) — not unplug yet, reopening (src=%s raw=%q)", wasActive, src, raw)
			}
			// The host does not drop VBUS when Desktop Head Unit exits, so
			// WaitDisconnect never fires and 18d1:2d00 stays enumerated.
			// A session that already spoke to the HU is over: restore the
			// ask gadget and let cargadgetd-aoa wait for the next START.
			// Reopening FunctionFS on the same accessory left DHU pinging
			// a dead TLS session.
			if wasActive {
				log.Printf("session ended after HU traffic — restoring USB gadget (src=%s raw=%q)", src, raw)
				stopGadgetAgent()
				return
			}
			log.Printf("session ended — reopening FunctionFS for next HU connect")
			time.Sleep(time.Second)
		}
	}

	network, addr := parseAddr(listen)
	ln, err := net.Listen(network, addr)
	if err != nil {
		log.Fatalf("listen: %v", err)
	}
	log.Printf("MD listening on %s %s", network, addr)

	for {
		conn, err := ln.Accept()
		if err != nil {
			log.Printf("accept: %v", err)
			continue
		}
		go serve(conn, conn.RemoteAddr().String(), false, nil)
	}
}

// sessionReadyTimeout bounds the wait for the head unit's first frame after the
// transport is up. Wireless head units can accept the TCP connection and stay
// silent; a bound turns that into a retry instead of a stuck phone.
const sessionReadyTimeout = 25 * time.Second

func runSession(md *session.MD, autoAV, usbPace bool, onReady func()) {
	errCh := make(chan error, 1)
	go func() { errCh <- md.Run() }()

	// Stay on this read until the host speaks or the endpoint errors.
	// Recycling the session on a timer closed ep1 while the FunctionFS
	// read was still blocked, so the version request landed on a dead
	// write ("file already closed") and the head unit never got a UI.
	select {
	case <-md.WaitReady():
		vw, vh := md.VideoSize()
		ox, oy, cw, ch := md.VideoPad()
		inW, inH := md.TouchSize()
		if onReady != nil {
			onReady()
		}
		log.Printf("session ready (SDR received) video=%d audio=%d input=%d sensor=%d size=%dx%d ui=%dx%d pad=%d,%d touch=%dx%d",
			md.VideoCh, md.AudioCh, md.InputCh, md.SensorCh, vw, vh, cw, ch, ox, oy, inW, inH)
		if !autoAV {
			log.Printf("session: %v", <-errCh)
			return
		}
		if err := md.OpenVideo(); err != nil {
			log.Printf("open video: %v", err)
		}
		if err := md.OpenInput(); err != nil {
			log.Printf("open input: %v", err)
		}
		if md.SensorCh != 0 {
			if err := md.OpenSensor(); err != nil {
				log.Printf("open sensor: %v", err)
			}
		}
		// Everything else the unit listed. Some head units hold their streams
		// back until every channel is open; the Melia lists nine services and
		// we speak five, so this covers the microphone, the extra audio sinks
		// and the phone status channel it also announced.
		// Off by default since 2026-11-26: the sessions that showed our picture
		// on the Melia (25/11 01:29 and 01:34) opened five channels (video,
		// input, sensor, Bluetooth, one audio); every session since the passive
		// opening of the microphone and the extra audio sinks showed the unit's
		// own wallpaper instead, with the same stream it had acked before.
		// Keep it behind a switch to re-test that A/B on the bench.
		if os.Getenv("CARGADGETD_OPEN_ALL_CHANNELS") == "1" {
			md.OpenEveryListedChannel()
		}
		select {
		case <-md.WaitVideoStarted():
			log.Printf("video started")
		case <-time.After(8 * time.Second):
			log.Printf("video start timeout — allowing video stream without focus")
			md.AllowVideo()
		case err := <-errCh:
			log.Printf("session ended before video start: %v", err)
			return
		}
		conf := cfg.Get()
		wantAudio := conf.Audio.Enabled
		if !wantAudio {
			log.Printf("audio disabled (audio.enabled=false)")
		}

		framePeriod := shell.DefaultFramePeriod
		if ms := conf.Video.FrameMs; ms > 0 {
			if d := time.Duration(ms) * time.Millisecond; d >= shell.MinFramePeriod {
				framePeriod = d
			}
		}
		sampleRate, pcmChannels, audioCfgIdx := md.AudioFormat()
		pcmChunkMs := 40
		if !usbPace {
			pcmChunkMs = int(framePeriod / time.Millisecond)
			if pcmChunkMs < 20 {
				pcmChunkMs = 20
			}
			pcmChannels = 1
		}
		tickSet := conf.Audio.TickMs > 0
		if tickSet {
			pcmChunkMs = conf.Audio.TickMs
			if pcmChunkMs < 10 {
				pcmChunkMs = 10
			}
		}
		if n := conf.Audio.Channels; n == 1 || n == 2 {
			pcmChannels = n
		}
		// USB: 2048 stereo frames @ 48kHz ≈ 42.67ms (Pixel Bi≈8235) unless audio.tick_ms set.
		chunkSamples := sampleRate * pcmChunkMs / 1000
		audioTick := time.Duration(pcmChunkMs) * time.Millisecond
		if usbPace && !tickSet {
			chunkSamples = 2048
			audioTick = time.Second * time.Duration(chunkSamples) / time.Duration(sampleRate)
			pcmChunkMs = int(audioTick / time.Millisecond)
			if pcmChunkMs < 1 {
				pcmChunkMs = 1
			}
		}

		// The vendor encoder is created before any shell plugin is dlopen'ed.
		// On the VW the process died inside Codec2 while the plugins were still
		// coming up (2026-11-26 09:19): the create is the fragile call, so it
		// happens first, on its own, exactly like the start-up warm-up that
		// always succeeds. The painter is attached below, once the shell exists.
		live, err := livevid.New(livevid.Options{
			Width: vw, Height: vh, FPS: int(time.Second / framePeriod), Bitrate: 400,
			TargetBps: conf.Video.BitrateKbps * 1000,
			Encoder:   conf.Video.Encoder,
		})
		if err != nil {
			log.Printf("livevid: %v", err)
			return
		}
		defer live.Close()

		ui := shell.New(cw, ch, sampleRate, pcmChannels)
		ui.SetFramePeriod(framePeriod)
		ui.SetDropFrames(conf.Shell.DropFrames)
		if err := ui.OpenShell(shell.PluginDir, conf.Shell.ID); err != nil {
			log.Printf("shell: %v", err)
		}
		pluginDirs := []string{shell.PluginDir}
		if ids := shell.Load(ui, pluginDirs); len(ids) > 0 {
			log.Printf("shell: loading %s", strings.Join(ids, ", "))
			go func() {
				// Start the apps only once their plugins have finished loading.
				// StartApps used to be called straight after Load, while this
				// goroutine was still waiting: startOne skips an app whose
				// runner is not there yet, and nothing starts it again, so the
				// shell came up with "No apps" — a home screen with no tiles
				// and no way to reach them, which is exactly what the bench
				// unit displayed while the video itself ran fine.
				if ui.WaitLoad(2 * time.Minute) {
					log.Printf("shell apps ready: %s", strings.Join(ui.AppIDs(), ", "))
				} else {
					log.Printf("shell: apps not ready after 2m, starting what loaded")
				}
				ui.StartApps()
			}()
		} else {
			log.Printf("shell: no libcargadgetd_app_<id>.so in %v", pluginDirs)
		}
		ui.StartGPS(conf.GPS.Backend)
		ui.WatchNotifications()
		defer ui.Close()
		// Restore Pulse before plugin stop. A fault in a plugin's stop
		// must not leave the phone player on sink.null.
		phone := loopback.Start(ui.Mixer().PlayBlocking)
		defer phone.Stop()
		defer ui.Mixer().Unblock()

		var pcmMu sync.Mutex
		var audioTS uint64
		audioTSInit := false
		var audioOpened atomic.Bool
		var audioOpenMu sync.Mutex
		var videoTS atomic.Uint64
		mixBuf := make([]int16, chunkSamples*pcmChannels)
		usbDump := newPCMDumper("usb")
		defer usbDump.close()
		sendMix := func(chunks int, baseTS uint64) {
			if !wantAudio || !audioOpened.Load() || !md.AudioFocusGranted() {
				return
			}
			pcmMu.Lock()
			defer pcmMu.Unlock()
			if !audioTSInit {
				audioTS = baseTS
				audioTSInit = true
				log.Printf("audio clock sync: audioTS=%d (video ts)", audioTS)
			}
			for i := 0; i < chunks; i++ {
				if usbPace && md.AudioInFlight() >= int(session.MaxUSBAudioInFlight) {
					return
				}
				audioTS += uint64(audioTick / time.Microsecond)
				ui.Mixer().Mix(mixBuf)
				usbDump.write(mixBuf, pcmChannels, sampleRate)
				if err := md.SendPCM(audioTS, shell.PCM16LE(mixBuf)); err != nil {
					if errors.Is(err, session.ErrNoAudioCredit) {
						return
					}
					log.Printf("send pcm: %v", err)
					return
				}
			}
		}
		ensureAudio := func() {
			if !wantAudio {
				return
			}
			audioOpenMu.Lock()
			defer audioOpenMu.Unlock()
			if audioOpened.Load() {
				return
			}
			if usbPace {
				_ = md.WaitVideoAcked(2 * time.Second)
			}
			if err := md.OpenAudio(); err != nil {
				log.Printf("audio: open failed: %v", err)
				return
			}
			audioOpened.Store(true)
			pcmMu.Lock()
			audioTSInit = false
			pcmMu.Unlock()
			log.Printf("audio: opened (silence until an app plays)")
		}
		// OpenAudio/USB must not run on the plugin play() callback stack
		// (.so Go runtime → host cgGoPlay): that nested cgo path wedged FFS
		// (ep2 ENOMEM) when tapping Hello.
		audioKick := make(chan struct{}, 1)
		ui.Mixer().SetOnPlay(func() {
			select {
			case audioKick <- struct{}{}:
			default:
			}
		})
		go func() {
			for {
				select {
				case <-md.Done():
					return
				case <-audioKick:
					ensureAudio()
				}
			}
		}()
		go func() {
			var touchN uint64
			for ev := range md.Events {
				if ev.Type != "touch" {
					continue
				}
				t, ok := session.ParseTouch(ev.Payload)
				if ok {
					act, ok := shell.MapTouch(t.Action)
					if !ok {
						touchN++
						if touchN <= 8 {
							log.Printf("touch unmapped action=%d xy=%d,%d body=%x", t.Action, t.X, t.Y, ev.Payload)
						}
					} else {
						x, y := session.MapTouch(int(t.X), int(t.Y), ox, oy, cw, ch, vw, vh, inW, inH)
						touchN++
						if touchN <= 8 || touchN%50 == 0 {
							log.Printf("touch #%d aa=%d,%d action=%d → ui=%d,%d act=%d (in=%dx%d codec=%dx%d)",
								touchN, t.X, t.Y, t.Action, x, y, act, inW, inH, vw, vh)
						}
						ui.Touch(uint32(x), uint32(y), act)
					}
				}
				for _, b := range session.ParseButtons(ev.Payload) {
					name, ok := session.HuInputName(b.ScanCode)
					if !ok {
						continue
					}
					pressed := 0
					if b.Pressed {
						pressed = 1
					}
					log.Printf("huinput %s pressed=%d scan=%d", name, pressed, b.ScanCode)
					ui.HuInput(name, pressed)
				}
				for _, k := range session.ParseKnobs(ev.Payload) {
					name, ok := session.KnobEvent(k.Code, k.Delta)
					if !ok {
						continue
					}
					log.Printf("huinput %s pressed=1 scan=%d delta=%d", name, k.Code, k.Delta)
					ui.HuInput(name, 1)
				}
			}
		}()
		if wantAudio {
			urbBytes := chunkSamples * 2 * pcmChannels
			if usbPace {
				log.Printf("audio mix: %s ch=%d %dHz %dch config_index=%d, tick=%v (%d samples, ~%dB PCM/URB); open after first video frame",
					md.AudioKind(), md.AudioCh, sampleRate, pcmChannels, audioCfgIdx, audioTick.Round(time.Microsecond), chunkSamples, urbBytes)
			} else {
				log.Printf("audio mix: %dHz %dch, 1×%dms after each video (~%dB PCM/URB); open after first video frame",
					sampleRate, pcmChannels, pcmChunkMs, urbBytes)
			}
		}
		fps := int(time.Second / framePeriod)
		if fps < 1 {
			fps = 10
		}

		content := image.NewRGBA(image.Rect(0, 0, cw, ch))
		letterbox := color.RGBA{A: 0xff}
		paint := func(dst *image.RGBA, now time.Time) {
			ui.Draw(content, now)
			if ox == 0 && oy == 0 && cw == vw && ch == vh {
				draw.Draw(dst, dst.Bounds(), content, image.Point{}, draw.Src)
				return
			}
			draw.Draw(dst, dst.Bounds(), &image.Uniform{C: letterbox}, image.Point{}, draw.Src)
			draw.Draw(dst, image.Rect(ox, oy, ox+cw, oy+ch), content, image.Point{}, draw.Src)
		}
		if !conf.Video.Live {
			log.Printf("live video disabled (video.live=false)")
			log.Printf("session: %v", <-errCh)
			return
		}
		// JP2026 Codec2 create segfaults if a plugin dlopen is still in
		// progress (music / reversegearhead). Handshake is already done;
		// the first frame waits until every .so has finished init.
		if !ui.WaitLoad(2 * time.Minute) {
			log.Printf("shell: plugin load timed out; opening encoder anyway")
		}
		live.SetPaint(paint)
		log.Printf("video: CBR target %.1f Mbps (video.bitrate_kbps=%d)", float64(live.Bitrate())/1e6, conf.Video.BitrateKbps)
		log.Printf("live video: %dx%d ui=%dx%d pad=%d,%d @ %dfps encoder=%s cbr=%d (encode every frame)", vw, vh, cw, ch, ox, oy, fps, live.EncoderName(), live.Bitrate())

		var ts uint64
		videoStarted := false
		codecSent := false
		streaming := true
		nextFrame := time.Now()
		var bypassN, skipN uint64
		var lastIDR time.Time
		// keyframeInterval keeps an IDR flowing even when nothing goes wrong: a
		// stream whose only keyframe is the first one never recovers a decoder
		// that lost its reference, and the head unit then stalls for good.
		const keyframeInterval = 2 * time.Second
		var pcmCoolUntil atomic.Int64 // unix nano; 0 = not cooling
		var pcmCoolN atomic.Uint64
		onAudioAckStall := func(reason string) {
			md.AbandonAudioCredit(reason)
			n := pcmCoolN.Add(1)
			pcmCoolUntil.Store(time.Now().Add(2 * time.Second).UnixNano())
			if n <= 5 || n%25 == 0 {
				log.Printf("audio ack slow — cool-and-retry (n=%d)", n)
			}
		}
		// USB: independent PCM ticker (Pixel-like). TCP: PCM glued to video loop below.
		if usbPace && wantAudio {
			go func() {
				tick := time.NewTicker(audioTick)
				defer tick.Stop()
				for {
					select {
					case <-md.Done():
						return
					case <-tick.C:
						if !audioOpened.Load() || !md.AudioFocusGranted() {
							continue
						}
						if until := pcmCoolUntil.Load(); until != 0 && time.Now().UnixNano() < until {
							continue
						}
						sendMix(1, videoTS.Load())
						if md.AudioInFlight() >= int(session.MaxUSBAudioInFlight) {
							if err := md.WaitAudioCredit(audioTick + 20*time.Millisecond); err != nil {
								onAudioAckStall("audio ticker")
							}
						}
					}
				}
			}()
		}
		for {
			select {
			case err := <-errCh:
				log.Printf("session: %v", err)
				return
			case <-md.Done():
				// The session is over but Run() may still be blocked in a
				// transport read. Stop pushing frames into a dead endpoint:
				// that wrote "send queue full" for minutes and kept the
				// service from restarting the session.
				select {
				case err := <-errCh:
					log.Printf("session: %v", err)
				default:
					log.Printf("session: ended (watchdog or peer), stopping the video loop")
				}
				return
			default:
			}
			if usbPace && !md.VideoStreaming() {
				if streaming {
					streaming = false
					log.Printf("video paused (MediaStop/focus loss) — waiting for StartInd")
				}
				time.Sleep(50 * time.Millisecond)
				nextFrame = time.Now()
				continue
			}
			if !streaming {
				streaming = true
				live.ForceIDR()
				nextFrame = time.Now()
				log.Printf("video resume — IDR after HU StartInd/focus")
			}
			now := time.Now()
			if now.Before(nextFrame) {
				time.Sleep(nextFrame.Sub(now))
				now = time.Now()
			} else if lag := now.Sub(nextFrame); lag >= framePeriod {
				skip := int(lag / framePeriod)
				ts += uint64(skip) * uint64(framePeriod/time.Microsecond)
				skipN += uint64(skip)
				nextFrame = now
				if skipN <= 5 || skipN%50 == 0 {
					log.Printf("video catch-up: skipped %d ticks (total_skip=%d)", skip, skipN)
				}
			}
			ts += uint64(framePeriod / time.Microsecond)
			videoTS.Store(ts)
			if usbPace {
				// One try per tick. If the previous AU is still unacked, drop
				// this frame and encode a current one on the next credit —
				// do not spin until MediaAck (that froze gears/Link on Subaru).
				wait := framePeriod
				if time.Now().After(nextFrame) {
					wait = 0
				}
				err := md.ReserveVideoCredit(wait)
				if err != nil {
					if errors.Is(err, session.ErrNoVideoCredit) {
						// Ack RTT is ~80ms. Past VideoAckGiveUp the ack is not
						// coming: drop that credit and send a new keyframe.
						// Do not retransmit the unacked AU.
						if md.GiveUpVideoAck(session.VideoAckGiveUp) {
							live.NotePipeSlow()
							// A keyframe costs an encoder reopen, which is what
							// killed Codec2 when it happened per dropped frame.
							// One per two seconds is enough to resynchronise.
							if time.Since(lastIDR) > 2*time.Second {
								lastIDR = time.Now()
								live.ForceIDR()
							}
						}
						bypassN++
						if bypassN <= 5 || bypassN%50 == 0 {
							log.Printf("video drop: waiting MediaAck (dropped=%d last_ack_age=%v)",
								bypassN, md.LastVideoAckAge().Round(time.Millisecond))
						}
						nextFrame = nextFrame.Add(framePeriod)
						continue
					}
					log.Printf("video credit: %v", err)
					if err.Error() == "session done" {
						return
					}
					nextFrame = nextFrame.Add(framePeriod)
					continue
				}
			}
			if time.Since(lastIDR) >= keyframeInterval {
				lastIDR = time.Now()
				live.RequestIDR()
			}
			au, err := live.Encode(now)
			if err != nil {
				if usbPace {
					md.ReleaseVideoCredit()
				}
				log.Printf("live encode: %v", err)
				nextFrame = nextFrame.Add(framePeriod)
				continue
			}
			// The parameter sets go out as CODEC_CONFIG before the first frame:
			// a head unit that reads only that message never learns the picture
			// size and acks our frames without ever showing them.
			if !codecSent {
				if cc := live.CodecConfig(); len(cc) > 0 {
					if err := md.SendVideoCodecConfig(cc); err != nil {
						log.Printf("video CODEC_CONFIG: %v", err)
					} else {
						codecSent = true
						log.Printf("video CODEC_CONFIG sent (%dB)", len(cc))
					}
				}
			}
			t0 := time.Now()
			if usbPace {
				err = md.SendH264Reserved(ts, au)
			} else {
				_, err = md.SendH264Credit(ts, au)
			}
			if err != nil {
				if errors.Is(err, session.ErrNoVideoCredit) {
					bypassN++
					if bypassN <= 5 || bypassN%50 == 0 {
						log.Printf("video hold: skip frame (waiting MediaAck, total_hold=%d)", bypassN)
					}
				} else {
					log.Printf("send h264: %v (au=%dB wait=%v)", err, len(au), time.Since(t0).Round(time.Millisecond))
				}
				nextFrame = nextFrame.Add(framePeriod)
				continue
			}
			if !videoStarted {
				videoStarted = true
				log.Printf("first video AU on wire")
				// Home grid never calls play(). Harman fills ~4s of video then
				// stops MediaAck if the audio channel (the presentation clock)
				// was never opened. Silence ticks keep the sink fed.
				if wantAudio {
					select {
					case audioKick <- struct{}{}:
					default:
					}
				}
			}
			if d := time.Since(t0); d > 100*time.Millisecond {
				log.Printf("send h264 slow: au=%dB wait=%v", len(au), d.Round(time.Millisecond))
				if d > 150*time.Millisecond {
					live.NotePipeSlow()
				}
			}
			// Steady CBR frames are ~20KB. Log IDRs and scene changes above that.
			if len(au) > 48*1024 {
				log.Printf("large video AU %dB", len(au))
			}
			if audioOpened.Load() && md.AudioFocusGranted() && !usbPace {
				sendMix(1, ts)
			}
			nextFrame = nextFrame.Add(framePeriod)
		}
	case err := <-errCh:
		log.Printf("session ended early (handshake/SDR): %v", err)
		return
	case <-time.After(sessionReadyTimeout):
		// Wireless: the enquiry reaches "session Android Auto" and the head unit
		// can stay completely mute on the TCP connection. Without this bound
		// runSession blocked for ever, the phone stayed on the car's access point
		// (no route) and only a Wi-Fi restart got it back.
		log.Printf("session: the head unit sent nothing in %s, dropping the connection", sessionReadyTimeout)
		return
	}
}

// bounceUDC detaches and reattaches the gadget from the USB controller so
// the host sees a genuine disconnect. Without it the HU never learns the AA
// session died (FunctionFS reopen is invisible on the bus) and keeps pinging
// the dead TLS session while every fresh accept chokes on those pings.
// gadgetctl chowns <gadget>/UDC to us at setup.
func bounceUDC() {
	udcPath := usbgadget.UDCPath(cfg.Get().Gadget.Path)
	cur, err := os.ReadFile(udcPath)
	if err != nil {
		log.Printf("udc bounce skipped: %v", err)
		return
	}
	name := strings.TrimSpace(string(cur))
	if name == "" {
		// Already unbound (e.g. cable pulled) — rebind to the first UDC.
		ents, _ := os.ReadDir("/sys/class/udc")
		if len(ents) == 0 {
			log.Printf("udc bounce skipped: gadget unbound and no UDC present")
			return
		}
		name = ents[0].Name()
	} else {
		if err := os.WriteFile(udcPath, []byte("\n"), 0644); err != nil {
			log.Printf("udc unbind failed: %v", err)
			return
		}
		time.Sleep(500 * time.Millisecond) // let the host register the disconnect
	}
	if err := os.WriteFile(udcPath, []byte(name+"\n"), 0644); err != nil {
		log.Printf("udc rebind failed: %v", err)
		return
	}
	log.Printf("udc bounced (%s) — host will re-enumerate", name)
}

func stopGadgetAgent() {
	bin := cfg.Get().Gadget.Bin
	cmd := exec.Command(bin, "-stop")
	out, err := cmd.CombinedOutput()
	if err != nil {
		log.Printf("gadget -stop: %v (%s)", err, bytesTrim(out))
		return
	}
	if len(out) > 0 {
		log.Printf("gadget -stop: %s", bytesTrim(out))
	}
}

func bytesTrim(b []byte) string {
	return strings.TrimSpace(string(b))
}

// effectiveListen drops an ffs:// address when transport is wireless, even if
// the USB launcher passed -listen ffs://. The TCP or unix address in the
// config is used instead, then the compiled default.
func effectiveListen(chosen, configured, transport string) string {
	if transport != cfg.TransportWireless || !strings.HasPrefix(chosen, "ffs://") {
		return chosen
	}
	if configured != "" && !strings.HasPrefix(configured, "ffs://") {
		return configured
	}
	return "tcp://127.0.0.1:5277"
}

func pick(ss ...string) string {
	for _, s := range ss {
		if t := strings.TrimSpace(s); t != "" {
			return t
		}
	}
	return ""
}

func parseAddr(s string) (network, addr string) {
	if len(s) > 7 && s[:7] == "unix://" {
		return "unix", s[7:]
	}
	if len(s) > 6 && s[:6] == "tcp://" {
		return "tcp", s[6:]
	}
	if filepath.IsAbs(s) || s[0] == '.' {
		_ = os.Remove(s)
		return "unix", s
	}
	return "tcp", s
}

func pcmLeftHz(pcm []int16, ch, rate int) (hz float64, peak, zc int) {
	if ch < 1 || len(pcm) < ch*2 {
		return 0, 0, 0
	}
	frames := len(pcm) / ch
	prev := pcm[0]
	for i := 0; i < frames; i++ {
		cur := pcm[i*ch]
		a := int(cur)
		if a < 0 {
			a = -a
		}
		if a > peak {
			peak = a
		}
		if (prev < 0 && cur >= 0) || (prev >= 0 && cur < 0) {
			zc++
		}
		prev = cur
	}
	hz = float64(zc) / 2 * float64(rate) / float64(frames)
	return
}

type pcmDumper struct {
	mu      sync.Mutex
	f       *os.File
	left    int
	n       int
	started bool
	tag     string
}

func newPCMDumper(tag string) *pcmDumper {
	p := strings.TrimSpace(cfg.Get().USB.PCMDump)
	if p == "" {
		return nil
	}
	f, err := os.Create(p + "-" + tag + ".s16le")
	if err != nil {
		log.Printf("pcm dump %s: %v", tag, err)
		return nil
	}
	log.Printf("pcm dump %s -> %s", tag, f.Name())
	return &pcmDumper{f: f, left: 48000 * 2 * 2 * 2, tag: tag}
}

func (d *pcmDumper) write(pcm []int16, ch, rate int) {
	if d == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.f == nil {
		return
	}
	hz, peak, zc := pcmLeftHz(pcm, ch, rate)
	d.n++
	if d.n <= 3 || d.n%25 == 0 {
		log.Printf("pcm dump %s n=%d L~%.0fHz zc=%d peak=%d", d.tag, d.n, hz, zc, peak)
	}
	if !d.started {
		if peak < 50 {
			return
		}
		d.started = true
	}
	b := shell.PCM16LE(pcm)
	if len(b) > d.left {
		b = b[:d.left]
	}
	n, _ := d.f.Write(b)
	d.left -= n
	if d.left <= 0 {
		log.Printf("pcm dump %s done", d.tag)
		_ = d.f.Close()
		d.f = nil
	}
}

func (d *pcmDumper) close() {
	if d == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.f != nil {
		_ = d.f.Close()
		d.f = nil
	}
}
