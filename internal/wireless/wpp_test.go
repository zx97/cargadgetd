package wireless

import (
	"bytes"
	"errors"
	"testing"
)

func TestWPPFrameRoundTrip(t *testing.T) {
	var b bytes.Buffer
	payload := []byte{0x08, 0x01, 0x10, 0x02}
	if err := writeWPP(&b, wppInfoResponse, payload); err != nil {
		t.Fatal(err)
	}
	pkt, err := readWPP(&b)
	if err != nil {
		t.Fatal(err)
	}
	if pkt.ID != wppInfoResponse {
		t.Fatalf("id=%d", pkt.ID)
	}
	if !bytes.Equal(pkt.Payload, payload) {
		t.Fatalf("payload=%x", pkt.Payload)
	}
}

func TestParseStartEndpointLayoutA(t *testing.T) {
	p := pbAppendString(nil, 2, "192.168.49.1")
	p = pbAppendVarint(p, 3, 5277)
	ip, port := parseStartEndpoint(p)
	if ip != "192.168.49.1" || port != 5277 {
		t.Fatalf("%s:%d", ip, port)
	}
}

func TestParseStartEndpointLayoutB(t *testing.T) {
	p := pbAppendString(nil, 1, "10.0.0.1")
	p = pbAppendVarint(p, 2, 5288)
	ip, port := parseStartEndpoint(p)
	if ip != "10.0.0.1" || port != 5288 {
		t.Fatalf("%s:%d", ip, port)
	}
}

func TestParseInfoResponseLayoutA(t *testing.T) {
	p := pbAppendString(nil, 2, "multivan")
	p = pbAppendString(p, 3, "AA:BB:CC:DD:EE:FF")
	p = pbAppendString(p, 4, "secret")
	p = pbAppendVarint(p, 5, 8)
	ssid, key, bssid, sec := parseInfoResponse(p)
	if ssid != "multivan" || key != "secret" || bssid != "AA:BB:CC:DD:EE:FF" || sec != 8 {
		t.Fatalf("%q %q %q %d", ssid, key, bssid, sec)
	}
}

func TestParseInfoResponseLayoutB(t *testing.T) {
	p := pbAppendString(nil, 1, "multivan")
	p = pbAppendString(p, 2, "AA:BB:CC:DD:EE:FF")
	p = pbAppendString(p, 3, "secret")
	p = pbAppendVarint(p, 4, 12)
	ssid, key, bssid, sec := parseInfoResponse(p)
	if ssid != "multivan" || key != "secret" || bssid != "AA:BB:CC:DD:EE:FF" || sec != 12 {
		t.Fatalf("%q %q %q %d", ssid, key, bssid, sec)
	}
}

func TestParseSetupInfo(t *testing.T) {
	var ep []byte
	ep = pbAppendString(ep, 1, "10.0.0.1")
	ep = pbAppendVarint(ep, 2, 5288)
	var ap []byte
	ap = pbAppendString(ap, 2, "multivan")
	ap = pbAppendString(ap, 3, "AA:BB:CC:DD:EE:FF")
	ap = pbAppendString(ap, 4, "secret")
	ap = pbAppendVarint(ap, 5, 8)
	var p []byte
	p = pbAppendBytes(p, 4, ep)
	p = pbAppendBytes(p, 5, ap)

	ip, port, ssid, key, bssid, sec := parseSetupInfo(p)
	if ip != "10.0.0.1" || port != 5288 {
		t.Fatalf("endpoint %s:%d", ip, port)
	}
	if ssid != "multivan" || key != "secret" || bssid != "AA:BB:CC:DD:EE:FF" || sec != 8 {
		t.Fatalf("ap %q %q %q %d", ssid, key, bssid, sec)
	}
}

func TestParseVersionSSID(t *testing.T) {
	var group []byte
	group = pbAppendString(group, 2, "multivan")
	var p []byte
	p = pbAppendVarint(p, 2, 1)
	p = pbAppendBytes(p, 7, group)
	if got := parseVersionSSID(p); got != "multivan" {
		t.Fatalf("got %q", got)
	}
}

func TestBuildVersionResponse(t *testing.T) {
	resp := buildVersionResponse(1, 7)
	f := pbParse(resp)
	if v, _ := pbLastVarint(f, 2); v != 1 {
		t.Fatalf("major=%d", v)
	}
	if v, _ := pbLastVarint(f, 3); v != 7 {
		t.Fatalf("minor=%d", v)
	}
	if v, _ := pbLastVarint(f, 5); v != wppStatusSuccess {
		t.Fatalf("status=%d", v)
	}
}

func TestRFCOMMChannelFromSDP(t *testing.T) {
	// ProtocolDescriptorList fragment: UUID16 RFCOMM (0x0003), uint8 channel.
	resp := []byte{0x35, 0x0b, 0x19, 0x00, 0x03, 0x08, 0x0b}
	ch, ok := rfcommChannelFromSDP(resp)
	if !ok || ch != 11 {
		t.Fatalf("ch=%d ok=%v", ch, ok)
	}
	if _, ok := rfcommChannelFromSDP([]byte{0x35, 0x03, 0x19, 0x00, 0x03}); ok {
		t.Fatal("matched without a channel")
	}
}

func TestStatusSnapshot(t *testing.T) {
	st := NewStatus()
	st.Phase("wifi")
	st.Set("Wi-Fi", false, "soho2016")
	st.Set("Wi-Fi", true, "multivan")
	st.SetWiFiWant("multivan")
	st.WPP("id=1")
	st.Error(errors.New("boom"))

	snap := st.Snapshot()
	if snap.Phase != "wifi" {
		t.Fatalf("phase=%q", snap.Phase)
	}
	if snap.Running {
		t.Fatal("running before the controller started")
	}
	if len(snap.Checks) != 1 || snap.Checks[0].Name != "Wi-Fi" || !snap.Checks[0].OK {
		t.Fatalf("checks=%+v", snap.Checks)
	}
	if snap.Error != "boom" || snap.WiFiWant != "multivan" || len(snap.WPP) != 1 {
		t.Fatalf("snapshot=%+v", snap)
	}

	st.Reset()
	if len(st.Snapshot().Checks) != 0 || st.Snapshot().WiFiWant != "" {
		t.Fatal("reset did not clear")
	}
}

func TestControllerJoinWiFiUnknown(t *testing.T) {
	ctl := NewController(Options{}, NewStatus(), nil)
	if err := ctl.JoinWiFi(); err == nil {
		t.Fatal("expected an error without an SSID")
	}
}
