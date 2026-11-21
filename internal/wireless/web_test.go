package wireless

import (
	"strings"
	"testing"
)

func TestRenderPage(t *testing.T) {
	snap := Snapshot{
		Phase:    "wifi",
		Running:  true,
		WiFiWant: "test-ssid",
		Error:    "handshake failed",
		Checks: []Check{
			{Name: "Bluetooth appairé", OK: true, Detail: "TESTCAR 10:32:2C:50:5D:5E"},
			{Name: "Wi-Fi voiture", OK: false, Detail: `actuel="home-wifi" attendu="test-ssid"`},
		},
		WPP: []string{"id=4 len=0"},
		USB: USBSnapshot{
			Transport:       "usb",
			ServiceState:    "active",
			Mode:            "ask",
			VendorID:        "0x0e8d",
			AccessoryLinked: true,
			Log:             []string{"waiting for Android Auto accessory start"},
		},
	}
	got := renderPage(snap)
	for _, want := range []string{
		"Cargadgetd Wireless",
		"Bluetooth appairé",
		"TESTCAR",
		"Wi-Fi voiture",
		`actuel=&#34;home-wifi&#34;`,
		"handshake failed",
		"Rejoindre le Wi-Fi test-ssid",
		"Démarrer",
		"Arrêter",
		"Redémarrer",
		"id=4 len=0",
		"Câble USB",
		"Service USB : active",
		"accessoire lié",
		"Réparer l'USB",
		"waiting for Android Auto accessory start",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("page is missing %q", want)
		}
	}
}

func TestRenderPageEmpty(t *testing.T) {
	got := renderPage(Snapshot{})
	if !strings.Contains(got, "En attente") {
		t.Fatal("empty page should show the idle line")
	}
	if !strings.Contains(got, `id="wifiForm" method="post" action="/wifi" style="display:none"`) {
		t.Fatal("Wi-Fi form should be hidden without an SSID")
	}
}
