package wireless

import "testing"

func TestParseDefaultDevsFindsTheCellularDefault(t *testing.T) {
	out := `default via 192.168.43.1 dev wlan0 proto dhcp src 192.168.43.2 metric 100
default via 10.0.0.1 dev wlan0 proto dhcp src 10.0.1.18 metric 600
default dev rmnet_data0 scope link metric 700`
	devs := parseDefaultDevs(out)
	if len(devs) != 3 {
		t.Fatalf("got %v, want 3 defaults", devs)
	}
	if devs[2] != "rmnet_data0" {
		t.Fatalf("cellular default not seen: %v", devs)
	}
	// The car's default is not a replacement for itself.
	other := ""
	for _, d := range devs {
		if d != "wlan0" {
			other = d
		}
	}
	if other != "rmnet_data0" {
		t.Fatalf("other default %q, want rmnet_data0", other)
	}
}

func TestParseDefaultDevsEmpty(t *testing.T) {
	if devs := parseDefaultDevs(""); len(devs) != 0 {
		t.Fatalf("got %v for empty input", devs)
	}
}
