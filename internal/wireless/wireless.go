package wireless

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"time"
)

// Options configures the Wireless Android Auto bootstrap.
type Options struct {
	// Name matches the paired head unit name (case-insensitive substring).
	Name string
	// Address matches the paired head unit Bluetooth address.
	Address string
	// Channel is the head unit's AA RFCOMM channel. Default 8.
	Channel uint8
	// Interface is the Wi-Fi interface. Default wlan0.
	Interface string
	// SSID, when set, is required in addition to the credentials from the
	// head unit before the bootstrap proceeds.
	SSID string
	// WiFiTimeout bounds the wait for the car's AP association. Default 60s.
	WiFiTimeout time.Duration
	// HandshakeTimeout bounds the RFCOMM WPP exchange. Default 60s.
	HandshakeTimeout time.Duration
	// Passive waits for the head unit to open our AA RFCOMM profile and never
	// pages or probes. It is the handover case: the car learns the phone over
	// USB, drops USB, and connects to the phone itself; our own pages and
	// probes in that window are refused and only add noise.
	Passive bool
	// Logf receives progress lines.
	Logf func(string, ...any)
	// Status receives the live checks rendered by the status web page.
	Status *Status
}

func (o *Options) logf(format string, args ...any) {
	if o.Logf != nil {
		o.Logf(format, args...)
	}
}

// Dial performs the Wireless Android Auto bootstrap. The primary path is the
// RFCOMM server profile: the head unit sees the AA UUID in the phone's SDP
// record and connects to us. Older head units expect the phone to connect;
// after a short wait the client path is tried as a fallback.
func Dial(ctx context.Context, opt Options) (conn net.Conn, err error) {
	start := time.Now()
	if opt.Interface == "" {
		opt.Interface = "wlan0"
	}
	if opt.WiFiTimeout <= 0 {
		opt.WiFiTimeout = 60 * time.Second
	}
	if opt.HandshakeTimeout <= 0 {
		opt.HandshakeTimeout = 60 * time.Second
	}
	opt.logf("dial: begin name=%q addr=%q channel=%d iface=%s ssid=%q wifi_timeout=%s wpp_timeout=%s",
		opt.Name, opt.Address, opt.Channel, opt.Interface, opt.SSID,
		opt.WiFiTimeout, opt.HandshakeTimeout)
	defer func() {
		if err != nil {
			opt.logf("dial: FAILED after %s: %v", sinceText(start), err)
			return
		}
		if conn == nil {
			opt.logf("dial: ended after %s without a connection", sinceText(start))
			return
		}
		opt.logf("dial: connected after %s", sinceText(start))
	}()

	st := opt.Status
	st.Phase("bluetooth")

	t := time.Now()
	devices, err := FindDevices(opt.Name, opt.Address)
	if err != nil {
		opt.logf("bluetooth: device lookup failed in %s: %v", sinceText(t), err)
		// The most common cause by far: the phone is not paired with the car any
		// more (a re-pair attempt, a car-side delete). Say it in one sentence
		// instead of dumping the device list on the page; the list stays in the
		// log for diagnosis.
		human := err
		if strings.Contains(err.Error(), "no paired Bluetooth device") {
			human = fmt.Errorf("no paired car: pair the phone from the car's Bluetooth menu, then press Restart")
		}
		st.Error(human)
		st.Set("Bluetooth paired", false, human.Error())
		return nil, human
	}
	dev := devices[0]
	opt.logf("bluetooth: %d paired candidate(s) in %s", len(devices), sinceText(t))
	st.Setf("Bluetooth paired", true, "%s %s", dev.Name, dev.Address)
	for i, c := range devices {
		opt.logf("bluetooth: candidate %d/%d %s addr=%s trusted=%v connected=%v adapter=%s uuids=%v",
			i+1, len(devices), c.Name, c.Address, c.Trusted, c.Connected, c.Adapter, c.UUIDs)
	}
	t = time.Now()
	dev.EnsureAdapter(opt.logf)
	opt.logf("bluetooth: adapter setup took %s", sinceText(t))
	st.Step("Bluetooth ready (adapter %s)", dev.Adapter)
	logWiFi(opt.Interface, opt.logf)
	if services, err := connmanWiFiServices(); err != nil {
		opt.logf("wifi: connman: %v", err)
	} else {
		opt.logf("wifi: connman reports %d saved stations", len(services))
		for _, s := range services {
			opt.logf("wifi: connman %q state=%s", s.Name, s.State)
		}
	}

	// Guess the car's AP from its Bluetooth name and the Wi-Fi networks already
	// saved on the phone. The authoritative SSID still comes from the WPP
	// handshake; this only lets the phone switch early.
	guess := ""
	if opt.SSID == "" {
		guess = savedSSIDFor(dev.Name)
	}
	if guess != "" {
		st.Setf("Saved network", true, "saved network %q (Bluetooth %q)", guess, dev.Name)
		st.SetWiFiWant(guess)
		opt.logf("wifi: auto-detected saved network %q from Bluetooth name %q", guess, dev.Name)
		t = time.Now()
		if err := connmanConnect(guess); err != nil {
			st.Set("Wi-Fi join", false, err.Error())
			opt.logf("wifi: early join %q failed after %s: %v", guess, sinceText(t), err)
		} else {
			st.Set("Wi-Fi join", true, "requested from ConnMan")
			opt.logf("wifi: early join %q accepted by ConnMan in %s", guess, sinceText(t))
		}
	} else {
		st.Set("Saved network", false, "no saved network matches the Bluetooth name")
	}

	// Primary path: the head unit connects to the phone's AA RFCOMM profile.
	var conns <-chan *os.File
	if c, err := ensureAAProfile(opt.logf); err != nil {
		st.Set("Android Auto profile", false, err.Error())
		opt.logf("profile: %v", err)
	} else {
		conns = c
		st.Set("Android Auto profile", true, "registered, waiting for the car")
		st.Phase("profile")
		waitFor := 30 * time.Second
		if opt.Passive {
			waitFor = 5 * time.Minute
		}
		opt.logf("profile: waiting up to %s for the head unit to open the AA RFCOMM channel (passive=%v)", waitFor, opt.Passive)
		st.Step("Android Auto profile published, waiting for the car (max %s)", waitFor)
		t = time.Now()
		select {
		case f := <-conns:
			rc := &fileConn{File: f}
			st.Set("Incoming RFCOMM", true, "car connected")
			st.Set("Bluetooth link", true, "car connected")
			st.Phase("wpp")
			opt.logf("rfcomm: incoming connection from the head unit after %s", sinceText(t))
			st.Step("The car connects to us")
			return handshake(ctx, rc, opt, st, guess)
		case <-time.After(waitFor):
			if opt.Passive {
				st.Set("Android Auto profile", false, "no incoming connection (passive mode)")
				return nil, fmt.Errorf("passive: no incoming connection after %s", sinceText(t))
			}
			st.Set("Android Auto profile", false, "no incoming connection, trying as client")
			opt.logf("profile: no incoming connection after %s, falling back to client", sinceText(t))
			st.Step("The car does not come: we call it")
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	// The head unit may connect to our profile at any time during the client
	// path. Check between the blocking steps so its connection is used at once
	// instead of waiting for the next attempt.
	acceptIncoming := func(where string) (net.Conn, bool) {
		if conns == nil {
			return nil, false
		}
		select {
		case f := <-conns:
			opt.logf("rfcomm: head unit connected while %s; using its channel", where)
			st.Set("Incoming RFCOMM", true, "car connected")
			st.Set("Bluetooth link", true, "car connected")
			st.Phase("wpp")
			return &fileConn{File: f}, true
		default:
			return nil, false
		}
	}

	// Fallback: some head units expect the phone to connect to them.
	if rc, ok := acceptIncoming("before the Bluetooth link"); ok {
		return handshake(ctx, rc, opt, st, guess)
	}

	// Try every candidate in turn: a head unit that is switched off must not
	// stop the daemon from finding one that is on. A pinned name or address
	// leaves a single candidate, so the behaviour is unchanged there.
	var lastErr error
	for i, cand := range devices {
		if i > 0 {
			opt.logf("bluetooth: next candidate %d/%d: %s (%s)", i+1, len(devices), cand.Name, cand.Address)
		}
		// No per-candidate line on the page: the driver only needs to know
		// which head unit answered. The candidates stay in the log.
		conn, err := dialCandidate(ctx, cand, opt, st, guess, acceptIncoming)
		if err == nil {
			st.Step("%s answered (%s)", cand.Name, cand.Address)
			return conn, nil
		}
		lastErr = err
		if ctx.Err() != nil {
			return nil, err
		}
		opt.logf("bluetooth: %s did not answer or refused: %v", cand.Name, err)
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("%w: no paired head unit answered (%d tried)", errCarAbsent, len(devices))
	} else if errors.Is(lastErr, errCarAbsent) {
		lastErr = fmt.Errorf("%w (%d paired devices tried)", lastErr, len(devices))
	}
	return nil, lastErr
}

// dialCandidate runs the client half of the bootstrap against one head unit:
// bring the ACL link up, find the AA RFCOMM channel, and hand the connection to
// the WPP handshake. It returns the first error, so Dial can move to the next
// candidate.
func dialCandidate(ctx context.Context, cand *Device, opt Options, st *Status, guess string,
	acceptIncoming func(string) (net.Conn, bool)) (net.Conn, error) {
	t := time.Now()
	linkErr := cand.Connect()
	if linkErr != nil {
		opt.logf("bluetooth: Device1.Connect failed after %s: %v", sinceText(t), linkErr)
	} else {
		opt.logf("bluetooth: Device1.Connect returned OK in %s", sinceText(t))
	}
	if cand.WaitConnected(10*time.Second, opt.logf) {
		opt.logf("bluetooth: link up")
		st.Set("Bluetooth link", true, "link up")
	} else {
		// Nothing else can work without the ACL link, and the next candidate
		// deserves the time: a head unit that is switched off must not cost a
		// full SDP and probe cycle.
		opt.logf("bluetooth: link still down after 10s, skipping %s", cand.Name)
		human := humanBluetoothError(linkErr)
		if human == nil {
			human = fmt.Errorf("%s: Bluetooth link never came up", cand.Name)
		}
		st.Set("Bluetooth link", false, human.Error())
		return nil, human
	}

	channel := opt.Channel
	if channel == 0 {
		st.Phase("sdp")
		for attempt := 1; attempt <= 3; attempt++ {
			if rc, ok := acceptIncoming("during the SDP lookup"); ok {
				return handshake(ctx, rc, opt, st, guess)
			}
			t = time.Now()
			ch, err := sdpRFCOMMChannel(cand.Address, opt.logf)
			if err == nil {
				channel = ch
				st.Setf("RFCOMM channel", true, "channel %d", ch)
				opt.logf("sdp: Android Auto RFCOMM channel %d (attempt %d took %s)", ch, attempt, sinceText(t))
				st.Step("RFCOMM channel %d found over SDP", ch)
				break
			}
			st.Set("RFCOMM channel", false, err.Error())
			opt.logf("sdp: attempt %d failed after %s: %v", attempt, sinceText(t), err)
			time.Sleep(3 * time.Second)
		}
		if channel == 0 {
			// SDP was refused (usually the baseband link is not fully up yet).
			// Do not guess: ask the head unit itself which channel speaks WPP.
			st.Set("RFCOMM channel", false, "SDP unavailable, probing channels")
			ch, perr := probeAAChannel(ctx, cand.Address, opt.logf)
			if perr != nil {
				st.Set("RFCOMM channel", false, perr.Error())
				return nil, perr
			}
			channel = ch
			st.Setf("RFCOMM channel", true, "channel %d (WPP probe)", ch)
			st.Step("RFCOMM channel %d found by probing", ch)
		}
	} else {
		st.Setf("RFCOMM channel", true, "forced channel %d", channel)
	}

	if rc, ok := acceptIncoming("before dialling the car channel"); ok {
		return handshake(ctx, rc, opt, st, guess)
	}
	t = time.Now()
	rc, err := dialRFCOMM(cand.Address, channel, opt.logf)
	if err != nil {
		human := humanBluetoothError(err)
		opt.logf("rfcomm: connect %s channel %d failed after %s: %v", cand.Address, channel, sinceText(t), err)
		st.Error(human)
		st.Set("RFCOMM", false, human.Error())
		return nil, human
	}
	st.Setf("RFCOMM", true, "channel %d", channel)
	st.Phase("wpp")
	if fc, ok := rc.(*fileConn); ok {
		opt.logf("rfcomm: connected to %s channel %d in %s (fd=%d)", cand.Address, channel, sinceText(t), fc.Fd())
	} else {
		opt.logf("rfcomm: connected to %s channel %d in %s", cand.Address, channel, sinceText(t))
	}
	return handshake(ctx, rc, opt, st, guess)
}

// errCarAbsent marks "nothing answered", the normal case at home: the page
// shows "waiting for the car" instead of an error.
var errCarAbsent = errors.New("car absent")

// humanBluetoothError turns raw BlueZ/L2CAP failures into one sentence the
// driver can act on. The raw error stays in the log.
func humanBluetoothError(err error) error {
	if err == nil {
		return nil
	}
	s := err.Error()
	switch {
	case strings.Contains(s, "host is down"),
		strings.Contains(s, "page-timeout"),
		strings.Contains(s, "page timeout"),
		strings.Contains(s, "connection timed out"):
		return fmt.Errorf("%w: Bluetooth unreachable (ignition on?)", errCarAbsent)
	case strings.Contains(s, "device or resource busy"):
		return fmt.Errorf("RFCOMM channel busy with a previous session: retrying next cycle")
	case strings.Contains(s, "connection refused"):
		return fmt.Errorf("no Android Auto service on this channel: retrying next cycle")
	}
	return err
}

// handshake runs the WPP exchange on rc, joins the car's AP, and returns the
// TCP connection where the phone is the TLS server.
func handshake(ctx context.Context, rc net.Conn, opt Options, st *Status, guess string) (conn net.Conn, err error) {
	start := time.Now()
	var (
		rx, tx int
		lastRx time.Time
	)
	defer func() {
		rc.Close()
		age := "never"
		if !lastRx.IsZero() {
			age = sinceText(lastRx)
		}
		if err != nil {
			st.Set("WPP", false, err.Error())
			opt.logf("wpp: end after %s, rx=%d tx=%d packets, last_rx_age=%s: %v",
				sinceText(start), rx, tx, age, err)
			return
		}
		opt.logf("wpp: end after %s, rx=%d tx=%d packets, last_rx_age=%s",
			sinceText(start), rx, tx, age)
	}()

	send := func(id uint16, payload []byte, what string) error {
		t := time.Now()
		err := writeWPP(rc, id, payload)
		if err != nil {
			opt.logf("wpp tx: id=%d %s len=%d FAILED after %s: %v", id, what, len(payload), sinceText(t), err)
			return err
		}
		tx++
		opt.logf("wpp tx: id=%d %s len=%d in %s hex=%s", id, what, len(payload), sinceText(t), hexPreview(payload))
		return nil
	}

	// The legacy handshake starts with the phone asking for the AP info.
	if err := send(wppInfoRequest, nil, "info request"); err != nil {
		return nil, err
	}
	idleSince := time.Now()

	var (
		endpoint         string
		ssid, key, bssid string
		security         uint64
		retries          int
	)
	// A raw RFCOMM socket opened with os.NewFile may stay a blocking file whose
	// SetReadDeadline does nothing; a reader goroutine plus explicit timers keeps
	// the handshake bounded either way.
	stream := newWPPStream(rc, opt.logf)
	deadline := time.Now().Add(opt.HandshakeTimeout)
waitLoop:
	for {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("wpp: handshake timeout after %s (endpoint=%q ssid=%q rx=%d)",
				sinceText(start), endpoint, ssid, rx)
		}
		t := time.Now()
		wait := 5 * time.Second
		if rem := time.Until(deadline); rem < wait {
			wait = rem
		}
		select {
		case pkt, ok := <-stream.pkts:
			if !ok {
				return nil, fmt.Errorf("wpp read after %s (rx=%d): %w", sinceText(t), rx, <-stream.err)
			}
			rx++
			lastRx = time.Now()
			idleSince = lastRx
			st.WPP(describeWPP(pkt))
			opt.logf("wpp rx: id=%d len=%d in %s hex=%s", pkt.ID, len(pkt.Payload), sinceText(t), hexPreview(pkt.Payload))
			opt.logf("wpp rx: %s", describeWPP(pkt))
			switch pkt.ID {
			case wppVersionRequest:
				f := pbParse(pkt.Payload)
				major, _ := pbLastVarint(f, 1)
				minor, _ := pbLastVarint(f, 2)
				st.Setf("WPP version", true, "%d.%d", major, minor)
				st.Step("WPP version %d.%d", major, minor)
				resp := buildVersionResponse(major, minor)
				if err := send(wppVersionResponse, resp, fmt.Sprintf("version response %d.%d", major, minor)); err != nil {
					return nil, err
				}
				if g := parseVersionSSID(pkt.Payload); g != "" {
					if ssid == "" {
						ssid = g
					}
					st.SetWiFiWant(g)
					st.Setf("WPP Wi-Fi", true, "ssid=%q (version)", g)
				}
				// Legacy head units answer this with WifiInfoResponse.
				if err := send(wppInfoRequest, nil, "info request"); err != nil {
					return nil, err
				}
			case wppSetupInfo:
				ip, port, s, k, b, sec := parseSetupInfo(pkt.Payload)
				if ip != "" {
					endpoint = net.JoinHostPort(ip, fmt.Sprint(port))
					st.Set("Endpoint", true, endpoint)
					st.Step("Car endpoint: %s", endpoint)
				}
				if s != "" {
					ssid, key, bssid, security = s, k, b, sec
					st.SetWiFiWant(ssid)
					st.Setf("WPP Wi-Fi", true, "ssid=%q", ssid)
					st.Step("Car Wi-Fi: %q (security %d)", ssid, security)
				}
				opt.logf("wpp setup: endpoint=%q ssid=%q key_len=%d bssid=%q security=%d",
					endpoint, ssid, len(key), bssid, security)
			case wppInfoResponse:
				s, k, b, sec := parseInfoResponse(pkt.Payload)
				if s != "" {
					ssid, key, bssid, security = s, k, b, sec
					st.SetWiFiWant(ssid)
					st.Setf("WPP Wi-Fi", true, "ssid=%q", ssid)
					st.Step("Car Wi-Fi: %q (security %d)", ssid, security)
				}
				opt.logf("wpp info: ssid=%q key_len=%d bssid=%q security=%d",
					ssid, len(key), bssid, security)
			case wppStartRequest:
				ip, port := parseStartEndpoint(pkt.Payload)
				if ip != "" {
					endpoint = net.JoinHostPort(ip, fmt.Sprint(port))
					st.Set("Endpoint", true, endpoint)
					st.Step("Car endpoint: %s", endpoint)
				}
				opt.logf("wpp start request: endpoint=%q", endpoint)
			case wppStartResponse:
				ip, port := parseStartEndpoint(pkt.Payload)
				if ip != "" {
					endpoint = net.JoinHostPort(ip, fmt.Sprint(port))
					st.Set("Endpoint", true, endpoint)
					st.Step("Car endpoint: %s", endpoint)
				}
				opt.logf("wpp start response: endpoint=%q", endpoint)
			case wppPingRequest:
				f := pbParse(pkt.Payload)
				ts, _ := pbLastVarint(f, 1)
				if err := send(wppPingResponse, buildPingResponse(ts), "ping response"); err != nil {
					return nil, err
				}
			default:
				opt.logf("wpp: unhandled packet id=%d len=%d (hex=%s)", pkt.ID, len(pkt.Payload), hexPreview(pkt.Payload))
			}
			if endpoint != "" && ssid != "" {
				opt.logf("wpp: endpoint and SSID known after %s, leaving the RFCOMM loop", sinceText(start))
				break waitLoop
			}
		case <-time.After(wait):
			if time.Since(idleSince) > 10*time.Second {
				// Stock Android Auto retries the Wi-Fi info request twice, then
				// closes the socket and restarts WPP. More retries hide the real
				// problem: a car that never answers is usually on a different
				// RFCOMM channel than the one we are talking to.
				if retries >= 2 {
					return nil, fmt.Errorf("wpp: no WifiInfoResponse after %d retries (wrong RFCOMM service on this channel?)", retries)
				}
				if err := send(wppInfoRequest, nil, "info request, retry"); err != nil {
					return nil, err
				}
				retries++
				idleSince = time.Now()
			}
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	st.Set("WPP handshake", true, endpoint)
	st.Phase("wifi")
	want := ssid
	if want == "" {
		want = guess
	}
	wantSrc := "wpp"
	if ssid == "" && guess != "" {
		wantSrc = "bluetooth name"
	}
	if opt.SSID != "" {
		want = opt.SSID
		wantSrc = "flag"
	}
	opt.logf("wifi: target ssid=%q (from %s), endpoint=%q, waiting up to %s", want, wantSrc, endpoint, opt.WiFiTimeout)
	if err := waitWifi(ctx, opt.Interface, want, opt.WiFiTimeout, opt.logf, st); err != nil {
		st.Set("Car Wi-Fi", false, err.Error())
		return nil, err
	}
	if err := send(wppConnectStatus, buildConnectStatus(wppStatusSuccess), "connect status SUCCESS"); err != nil {
		return nil, err
	}

	st.Phase("tcp")
	t := time.Now()
	conn, err = net.DialTimeout("tcp", endpoint, 10*time.Second)
	if err != nil {
		st.Set("Car TCP", false, err.Error())
		return nil, fmt.Errorf("tcp dial %s after %s: %w", endpoint, sinceText(t), err)
	}
	st.Set("Car TCP", true, endpoint)
	st.Set("Android Auto session", true, "TCP connected")
	st.Phase("session")
	opt.logf("tcp: connected to %s in %s", endpoint, sinceText(t))
	st.Step("TCP connected to %s", endpoint)
	return conn, nil
}
