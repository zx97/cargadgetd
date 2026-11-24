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

func defaultGateway(string) string { return "" }
