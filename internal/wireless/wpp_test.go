package wireless

import (
	"bytes"
	"errors"
	"fmt"
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

func TestParseStartEndpoint(t *testing.T) {
	// WifiStartRequest: ip_address=1, port=2, start_request_reason=3.
	p := pbAppendString(nil, 1, "192.168.49.1")
	p = pbAppendVarint(p, 2, 5288)
	p = pbAppendVarint(p, 3, 0)
	ip, port := parseStartEndpoint(p)
	if ip != "192.168.49.1" || port != 5288 {
		t.Fatalf("%s:%d", ip, port)
	}
}

func TestParseInfoResponse(t *testing.T) {
	// ssid=1, key=2, bssid=3, security_mode=4, access_point_type=5.
	p := pbAppendString(nil, 1, "test-ssid")
	p = pbAppendString(p, 2, "secret")
	p = pbAppendString(p, 3, "AA:BB:CC:DD:EE:FF")
	p = pbAppendVarint(p, 4, 8) // WPA2_PERSONAL
	p = pbAppendVarint(p, 5, 1) // DYNAMIC
	ssid, key, bssid, sec := parseInfoResponse(p)
	if ssid != "test-ssid" || key != "secret" || bssid != "AA:BB:CC:DD:EE:FF" || sec != 8 {
		t.Fatalf("%q %q %q %d", ssid, key, bssid, sec)
	}
}

// A head unit may send an empty BSSID; the field is required, so it is present
// but empty and must not shift the other fields.
func TestParseInfoResponseEmptyBssid(t *testing.T) {
	p := pbAppendString(nil, 1, "test-ssid")
	p = pbAppendString(p, 2, "secret")
	p = pbAppendString(p, 3, "")
	p = pbAppendVarint(p, 4, 8)
	ssid, key, bssid, _ := parseInfoResponse(p)
	if ssid != "test-ssid" || key != "secret" || bssid != "" {
		t.Fatalf("%q %q %q", ssid, key, bssid)
	}
}

func TestParseSetupInfo(t *testing.T) {
	var ep []byte
	ep = pbAppendString(ep, 1, "10.0.0.1")
	ep = pbAppendVarint(ep, 2, 5288)
	var ap []byte
	ap = pbAppendString(ap, 2, "test-ssid")
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
	if ssid != "test-ssid" || key != "secret" || bssid != "AA:BB:CC:DD:EE:FF" || sec != 8 {
		t.Fatalf("ap %q %q %q %d", ssid, key, bssid, sec)
	}
}

// WifiVersionRequest carries car_info=5 and wpp_info=6 (the WPP-over-TCP
// endpoint), not an SSID: credentials only arrive in WifiInfoResponse.
func TestParseVersionSSID(t *testing.T) {
	p := pbAppendVarint(nil, 1, 1)
	p = pbAppendVarint(p, 2, 0)
	if got := parseVersionSSID(p); got != "" {
		t.Fatalf("got %q", got)
	}
}

func TestBuildVersionResponse(t *testing.T) {
	// WifiVersionResponse: major=1, minor=2, status=4.
	resp := buildVersionResponse(1, 7)
	f := pbParse(resp)
	if v, _ := pbLastVarint(f, 1); v != 1 {
		t.Fatalf("major=%d", v)
	}
	if v, _ := pbLastVarint(f, 2); v != 7 {
		t.Fatalf("minor=%d", v)
	}
	if v, _ := pbLastVarint(f, 4); v != wppStatusSuccess {
		t.Fatalf("status=%d", v)
	}
}

func TestBuildStartResponse(t *testing.T) {
	// Measured on a working commercial session (payload-level HCI capture):
	// WifiStartResponse carries field 3 (status) alone, no ip_address, no port.
	// The wire frame is 00 02 00 07 18 00.
	resp := buildStartResponse(wppStatusSuccess)
	if got, want := fmt.Sprintf("%x", resp), "1800"; got != want {
		t.Fatalf("payload = %s, want %s", got, want)
	}
	f := pbParse(resp)
	if len(f) != 1 || f[0].Num != 3 || f[0].Var != wppStatusSuccess {
		t.Fatalf("unexpected fields: %+v", f)
	}
	var buf bytes.Buffer
	if err := writeWPP(&buf, wppStartResponse, resp); err != nil {
		t.Fatal(err)
	}
	if got, want := fmt.Sprintf("%x", buf.Bytes()), "000200071800"; got != want {
		t.Fatalf("frame = %s, want %s", got, want)
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
	st.Set("Wi-Fi", true, "test-ssid")
	st.SetWiFiWant("test-ssid")
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
	if snap.Error != "boom" || snap.WiFiWant != "test-ssid" || len(snap.WPP) != 1 {
		t.Fatalf("snapshot=%+v", snap)
	}

	st.Reset()
	if len(st.Snapshot().Checks) != 0 || st.Snapshot().WiFiWant != "" {
		t.Fatal("reset did not clear")
	}

	// Static checks survive Reset.
	st.SetStatic("Certificat MD", false, "expiré")
	st.Reset()
	snap = st.Snapshot()
	if len(snap.Checks) != 1 || snap.Checks[0].Name != "Certificat MD" || snap.Checks[0].OK {
		t.Fatalf("static check: %+v", snap.Checks)
	}
}

func TestControllerJoinWiFiUnknown(t *testing.T) {
	ctl := NewController(Options{}, NewStatus(), nil)
	if err := ctl.JoinWiFi(); err == nil {
		t.Fatal("expected an error without an SSID")
	}
}
