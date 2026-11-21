//go:build !linux

package wireless

import (
	"fmt"
	"net"
)

func dialRFCOMM(string, uint8) (net.Conn, error) {
	return nil, fmt.Errorf("wireless: Bluetooth RFCOMM is only implemented on Linux")
}
