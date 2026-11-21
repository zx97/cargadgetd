package wireless

import (
	"strings"
	"testing"
)

func TestRenderPage(t *testing.T) {
	snap := Snapshot{
		Phase:    "wifi",
		Running:  true,
		WiFiWant: "multivan",
		Error:    "handshake failed",
		Checks: []Check{
			{Name: "Bluetooth appairé", OK: true, Detail: "MULTIVAN 10:32:2C:50:5D:5E"},
			{Name: "Wi-Fi voiture", OK: false, Detail: `actuel="soho2016" attendu="multivan"`},
		},
		WPP: []string{"id=4 len=0"},
	}
	got := renderPage(snap)
	for _, want := range []string{
		"Cargadgetd Wireless",
		"Bluetooth appairé",
		"MULTIVAN",
		"Wi-Fi voiture",
		`actuel=&#34;soho2016&#34;`,
		"handshake failed",
		"Rejoindre le Wi-Fi multivan",
		"Démarrer",
		"Arrêter",
		"id=4 len=0",
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
	if strings.Contains(got, "Rejoindre le Wi-Fi") {
		t.Fatal("no Wi-Fi button without an SSID")
	}
}
