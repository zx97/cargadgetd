//go:build linux

package wireless

// LiveWiFi returns the interface the phone is associated with and its IPv4,
// read now: the page must never show a network the phone has left.
func LiveWiFi() (ssid, ip string) { return wifiSSID("wlan0"), wifiIPv4("wlan0") }
