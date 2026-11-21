//go:build linux

package wireless

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// dialRFCOMM opens an RFCOMM stream to addr on channel. The kernel and BlueZ
// handle the ACL link and bonding; the caller receives a byte stream to the
// head unit's AA RFCOMM service.
func dialRFCOMM(addr string, channel uint8) (net.Conn, error) {
	bd, err := parseBDAddr(addr)
	if err != nil {
		return nil, err
	}
	fd, err := unix.Socket(unix.AF_BLUETOOTH, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, unix.BTPROTO_RFCOMM)
	if err != nil {
		return nil, fmt.Errorf("rfcomm socket: %w", err)
	}
	sa := &unix.SockaddrRFCOMM{Addr: bd, Channel: channel}
	start := time.Now()
	if err := unix.Connect(fd, sa); err != nil {
		unix.Close(fd)
		return nil, fmt.Errorf("rfcomm connect %s channel %d after %s: %w", addr, channel, sinceText(start), err)
	}
	f := os.NewFile(uintptr(fd), "rfcomm")
	if f == nil {
		unix.Close(fd)
		return nil, fmt.Errorf("rfcomm: os.NewFile failed")
	}
	// os.NewFile switches the socket to non-blocking and registers it with the
	// netpoller, so the deadlines set by handshake work.
	return &fileConn{File: f}, nil
}

func parseBDAddr(s string) ([6]uint8, error) {
	var bd [6]uint8
	parts := strings.Split(strings.TrimSpace(s), ":")
	if len(parts) != 6 {
		return bd, fmt.Errorf("bad Bluetooth address %q", s)
	}
	for i, p := range parts {
		v, err := strconv.ParseUint(p, 16, 8)
		if err != nil {
			return bd, fmt.Errorf("bad Bluetooth address %q: %w", s, err)
		}
		bd[5-i] = uint8(v) // bdaddr_t is little-endian
	}
	return bd, nil
}

type fileConn struct{ *os.File }

func (c *fileConn) LocalAddr() net.Addr                { return rfcommAddr{} }
func (c *fileConn) RemoteAddr() net.Addr               { return rfcommAddr{} }
func (c *fileConn) SetDeadline(t time.Time) error      { return c.File.SetDeadline(t) }
func (c *fileConn) SetReadDeadline(t time.Time) error  { return c.File.SetReadDeadline(t) }
func (c *fileConn) SetWriteDeadline(t time.Time) error { return c.File.SetWriteDeadline(t) }

type rfcommAddr struct{}

func (rfcommAddr) Network() string { return "rfcomm" }
func (rfcommAddr) String() string  { return "rfcomm" }
