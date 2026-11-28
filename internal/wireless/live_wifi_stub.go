//go:build !linux

package wireless

func LiveWiFi() (ssid, ip string) { return "", "" }
