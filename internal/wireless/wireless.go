package wireless

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
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

// Dial performs the Wireless Android Auto bootstrap against a paired head unit
// and returns the TCP connection to it. The caller runs the normal MD session
// on the connection: the phone is still the TLS server.
func Dial(ctx context.Context, opt Options) (net.Conn, error) {
	if opt.Interface == "" {
		opt.Interface = "wlan0"
	}
	if opt.WiFiTimeout <= 0 {
		opt.WiFiTimeout = 60 * time.Second
	}
	if opt.HandshakeTimeout <= 0 {
		opt.HandshakeTimeout = 60 * time.Second
	}
	st := opt.Status
	st.Phase("bluetooth")

	dev, err := FindDevice(opt.Name, opt.Address)
	if err != nil {
		st.Error(err)
		st.Set("Bluetooth appairé", false, err.Error())
		return nil, err
	}
	st.Set("Bluetooth appairé", true, fmt.Sprintf("%s %s", dev.Name, dev.Address))
	opt.logf("bluetooth: %s addr=%s paired=%v bonded=%v trusted=%v connected=%v services_resolved=%v adapter=%s uuids=%v",
		dev.Name, dev.Address, dev.Paired, dev.Bonded, dev.Trusted, dev.Connected, dev.ServicesResolved, dev.Adapter, dev.UUIDs)
	dev.EnsureAdapter(opt.logf)
	logWiFi(opt.Interface, opt.logf)
	if services, err := connmanWiFiServices(); err != nil {
		opt.logf("wifi: connman: %v", err)
	} else {
		for _, s := range services {
			opt.logf("wifi: connman %q state=%s", s.Name, s.State)
		}
	}
	if err := dev.Connect(); err != nil {
		opt.logf("bluetooth: connect %s: %v", dev.Address, err)
		st.Set("Lien Bluetooth", false, err.Error())
	} else {
		opt.logf("bluetooth: connected %s", dev.Address)
		st.Set("Lien Bluetooth", true, "connecté")
	}
	if dev.WaitConnected(10 * time.Second) {
		opt.logf("bluetooth: link up")
		st.Set("Lien Bluetooth", true, "lien monté")
	} else {
		opt.logf("bluetooth: link still down")
		st.Set("Lien Bluetooth", false, "lien non monté")
	}

	// Guess the car's AP from its Bluetooth name and the Wi-Fi networks already
	// saved on the phone. The authoritative SSID still comes from the WPP
	// handshake; this only lets the phone switch early.
	guess := ""
	if opt.SSID == "" {
		guess = savedSSIDFor(dev.Name)
	}
	if guess != "" {
		st.Set("Wi-Fi auto", true, fmt.Sprintf("réseau enregistré %q (Bluetooth %q)", guess, dev.Name))
		st.SetWiFiWant(guess)
		opt.logf("wifi: auto-detected saved network %q from Bluetooth name %q", guess, dev.Name)
		if err := connmanConnect(guess); err != nil {
			st.Set("Wi-Fi join", false, err.Error())
			opt.logf("wifi: early join %q: %v", guess, err)
		} else {
			st.Set("Wi-Fi join", true, "demandé à ConnMan")
			opt.logf("wifi: asked ConnMan to join %q early", guess)
		}
	} else {
		st.Set("Wi-Fi auto", false, "aucun réseau enregistré ne correspond au nom Bluetooth")
	}

	channel := opt.Channel
	if channel == 0 {
		st.Phase("sdp")
		for attempt := 1; attempt <= 5; attempt++ {
			ch, err := sdpRFCOMMChannel(dev.Address, opt.logf)
			if err == nil {
				channel = ch
				st.Set("SDP canal AA", true, fmt.Sprintf("canal %d", ch))
				opt.logf("sdp: Android Auto RFCOMM channel %d", ch)
				break
			}
			st.Set("SDP canal AA", false, err.Error())
			opt.logf("sdp: attempt %d: %v", attempt, err)
			time.Sleep(2 * time.Second)
		}
		if channel == 0 {
			channel = 8
			st.Set("SDP canal AA", false, "canal non trouvé, essai canal 8")
			opt.logf("sdp: no channel found; falling back to RFCOMM 8")
		}
	} else {
		st.Set("SDP canal AA", true, fmt.Sprintf("canal forcé %d", channel))
	}

	rc, err := dialRFCOMM(dev.Address, channel)
	if err != nil {
		st.Error(err)
		st.Set("RFCOMM", false, err.Error())
		return nil, err
	}
	defer rc.Close()
	st.Set("RFCOMM", true, fmt.Sprintf("canal %d", channel))
	st.Phase("wpp")
	opt.logf("rfcomm: connected to %s channel %d", dev.Address, channel)
	// The legacy handshake starts with the phone asking for the AP info.
	if err := writeWPP(rc, wppInfoRequest, nil); err != nil {
		return nil, err
	}
	opt.logf("wpp tx: id=%d (info request)", wppInfoRequest)
	idleSince := time.Now()

	var (
		endpoint         string
		ssid, key, bssid string
		security         uint64
	)
	deadline := time.Now().Add(opt.HandshakeTimeout)
	for {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("wpp: handshake timeout (endpoint=%q ssid=%q)", endpoint, ssid)
		}
		_ = rc.SetReadDeadline(time.Now().Add(5 * time.Second))
		pkt, err := readWPP(rc)
		if err != nil {
			if errors.Is(err, os.ErrDeadlineExceeded) {
				if time.Since(idleSince) > 10*time.Second {
					_ = writeWPP(rc, wppInfoRequest, nil)
					opt.logf("wpp tx: id=%d (info request, retry)", wppInfoRequest)
					idleSince = time.Now()
				}
				continue
			}
			return nil, fmt.Errorf("wpp read: %w", err)
		}
		idleSince = time.Now()
		st.WPP(describeWPP(pkt))
		opt.logf("wpp rx: %s", describeWPP(pkt))
		switch pkt.ID {
		case wppVersionRequest:
			f := pbParse(pkt.Payload)
			major, _ := pbLastVarint(f, 2)
			minor, _ := pbLastVarint(f, 3)
			st.Set("WPP version", true, fmt.Sprintf("%d.%d", major, minor))
			resp := buildVersionResponse(major, minor)
			if err := writeWPP(rc, wppVersionResponse, resp); err != nil {
				return nil, err
			}
			opt.logf("wpp tx: id=%d ver=%d.%d hex=%x", wppVersionResponse, major, minor, resp)
			if g := parseVersionSSID(pkt.Payload); g != "" {
				if ssid == "" {
					ssid = g
				}
				st.SetWiFiWant(g)
				st.Set("WPP Wi-Fi", true, fmt.Sprintf("ssid=%q (version)", g))
			}
			// Legacy head units answer this with WifiInfoResponse.
			if err := writeWPP(rc, wppInfoRequest, nil); err == nil {
				opt.logf("wpp tx: id=%d (info request)", wppInfoRequest)
			}
		case wppSetupInfo:
			ip, port, s, k, b, sec := parseSetupInfo(pkt.Payload)
			if ip != "" {
				endpoint = net.JoinHostPort(ip, fmt.Sprint(port))
			}
			if s != "" {
				ssid, key, bssid, security = s, k, b, sec
				st.SetWiFiWant(ssid)
				st.Set("WPP Wi-Fi", true, fmt.Sprintf("ssid=%q", ssid))
			}
			opt.logf("wpp setup: endpoint=%q ssid=%q key_len=%d bssid=%q security=%d",
				endpoint, ssid, len(key), bssid, security)
		case wppInfoResponse:
			s, k, b, sec := parseInfoResponse(pkt.Payload)
			if s != "" {
				ssid, key, bssid, security = s, k, b, sec
				st.SetWiFiWant(ssid)
				st.Set("WPP Wi-Fi", true, fmt.Sprintf("ssid=%q", ssid))
			}
			opt.logf("wpp info: ssid=%q key_len=%d bssid=%q security=%d",
				ssid, len(key), bssid, security)
		case wppStartRequest:
			ip, port := parseStartEndpoint(pkt.Payload)
			if ip != "" {
				endpoint = net.JoinHostPort(ip, fmt.Sprint(port))
				st.Set("WPP endpoint", true, endpoint)
			}
			opt.logf("wpp start request: endpoint=%q", endpoint)
		case wppStartResponse:
			ip, port := parseStartEndpoint(pkt.Payload)
			if ip != "" {
				endpoint = net.JoinHostPort(ip, fmt.Sprint(port))
				st.Set("WPP endpoint", true, endpoint)
			}
			opt.logf("wpp start response: endpoint=%q", endpoint)
		case wppPingRequest:
			f := pbParse(pkt.Payload)
			ts, _ := pbLastVarint(f, 2)
			if err := writeWPP(rc, wppPingResponse, buildPingResponse(ts)); err != nil {
				return nil, err
			}
		}
		if endpoint != "" && ssid != "" {
			break
		}
	}

	st.Set("WPP handshake", true, endpoint)
	st.Phase("wifi")
	want := ssid
	if want == "" {
		want = guess
	}
	if opt.SSID != "" {
		want = opt.SSID
	}
	if err := waitWifi(ctx, opt.Interface, want, opt.WiFiTimeout, opt.logf, st); err != nil {
		st.Set("Wi-Fi voiture", false, err.Error())
		return nil, err
	}
	if err := writeWPP(rc, wppConnectStatus, buildConnectStatus(wppStatusSuccess)); err != nil {
		return nil, err
	}
	opt.logf("wpp tx: id=%d status=SUCCESS", wppConnectStatus)
	_ = rc.SetReadDeadline(time.Time{})

	st.Phase("tcp")
	conn, err := net.DialTimeout("tcp", endpoint, 10*time.Second)
	if err != nil {
		st.Set("TCP voiture", false, err.Error())
		return nil, fmt.Errorf("tcp dial %s: %w", endpoint, err)
	}
	st.Set("TCP voiture", true, endpoint)
	st.Set("Session AA", true, "TCP connecté")
	st.Phase("session")
	opt.logf("tcp: connected to %s", endpoint)
	return conn, nil
}
