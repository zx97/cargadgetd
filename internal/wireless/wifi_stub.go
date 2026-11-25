//go:build !linux

package wireless

import (
	"context"
	"fmt"
	"time"
)

func waitWifi(context.Context, string, string, time.Duration, func(string, ...any), *Status) error {
	return fmt.Errorf("wireless: Wi-Fi checks are only implemented on Linux")
}

func logWiFi(string, func(string, ...any)) {}

func WatchLink(context.Context, string, func(string, ...any), func()) {}

func AssociateWiFi(string, string, string, func(string, ...any)) error {
	return fmt.Errorf("wireless: Wi-Fi association is only implemented on Linux")
}

func defaultGateway(string) string { return "" }
