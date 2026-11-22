package wireless

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newRequest builds a request carrying an Accept-Language header.
func newRequest(accept string) *http.Request {
	r := httptest.NewRequest("GET", "/", nil)
	if accept != "" {
		r.Header.Set("Accept-Language", accept)
	}
	return r
}

func testSnapshot() Snapshot {
	return Snapshot{
		Phase:    "wifi",
		Running:  true,
		WiFiWant: "test-ssid",
		Error:    "handshake failed",
		Checks: []Check{
			{Name: "Bluetooth paired", OK: true, Detail: "TESTCAR 10:32:2C:50:5D:5E"},
			{Name: "Car Wi-Fi", OK: false, Detail: `current="home-wifi" want="test-ssid"`},
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
}

func TestRenderPage(t *testing.T) {
	snap := testSnapshot()
	snap.Localize("en")
	got := renderPage("en", snap)
	for _, want := range []string{
		"Cargadgetd Wireless",
		"Bluetooth paired",
		"TESTCAR",
		"Car Wi-Fi",
		`current=&#34;home-wifi&#34;`,
		"handshake failed",
		"Join Wi-Fi test-ssid",
		"Start",
		"Stop",
		"Restart",
		"id=4 len=0",
		`data-p="usb"`,
		"USB service: active",
		"accessory linked",
		"Repair USB",
		"waiting for Android Auto accessory start",
		`class="flow"`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("page is missing %q", want)
		}
	}
}

// TestRenderPageFrench checks the page is translated, and that the page script
// carries no language of its own: the labels come from the JSON.
func TestRenderPageFrench(t *testing.T) {
	snap := testSnapshot()
	snap.Localize("fr")
	got := renderPage("fr", snap)
	for _, want := range []string{
		"Cargadgetd sans fil",
		"Bluetooth appairé",
		"Rejoindre le Wi-Fi test-ssid",
		"Démarrer",
		"Arrêter",
		"Redémarrer",
		"Réparer l&#39;USB",
		"État",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("French page is missing %q", want)
		}
	}
	if !strings.Contains(got, "s.Banner") {
		t.Fatal("page script should read the translated banner from the JSON")
	}
	if strings.Contains(got, "'Arrêté'") {
		t.Fatal("page script must not embed a language of its own")
	}
}

func TestRenderPageEmpty(t *testing.T) {
	snap := Snapshot{}
	snap.Localize("en")
	got := renderPage("en", snap)
	if !strings.Contains(got, "Waiting") {
		t.Fatal("empty page should show the idle line")
	}
	if !strings.Contains(got, `id="wifiForm" method="post" action="/wifi" style="display:none"`) {
		t.Fatal("Wi-Fi form should be hidden without an SSID")
	}
}

func TestDetectLang(t *testing.T) {
	cases := map[string]string{
		"fr-CH,fr;q=0.9,en;q=0.8": "fr",
		"en-GB,en;q=0.9":          "en",
		"de-DE,de;q=0.9":          "en", // no German translation yet
		"":                        "en",
	}
	for accept, want := range cases {
		r := newRequest(accept)
		if got := DetectLang(r); got != want {
			t.Fatalf("DetectLang(%q) = %q, want %q", accept, got, want)
		}
	}
}
