package wireless

import (
	"context"
	"io"
	"net"
	"testing"
	"time"
)

// blockingConn never answers a read and cannot carry deadlines, like a raw
// RFCOMM socket opened from a file descriptor. Before the reader goroutine,
// such a socket wedged the whole connection attempt.
type blockingConn struct {
	closed chan struct{}
}

func newBlockingConn() *blockingConn { return &blockingConn{closed: make(chan struct{})} }

func (c *blockingConn) Read([]byte) (int, error) {
	<-c.closed
	return 0, io.EOF
}

func (c *blockingConn) Write(p []byte) (int, error) { return len(p), nil }

func (c *blockingConn) Close() error {
	select {
	case <-c.closed:
	default:
		close(c.closed)
	}
	return nil
}

func (c *blockingConn) LocalAddr() net.Addr  { return rfcommAddr{} }
func (c *blockingConn) RemoteAddr() net.Addr { return rfcommAddr{} }

func (c *blockingConn) SetDeadline(time.Time) error      { return nil }
func (c *blockingConn) SetReadDeadline(time.Time) error  { return nil }
func (c *blockingConn) SetWriteDeadline(time.Time) error { return nil }

func TestHandshakeTimesOutOnSilentConnection(t *testing.T) {
	rc := newBlockingConn()
	opt := Options{
		HandshakeTimeout: 300 * time.Millisecond,
		Logf:             func(string, ...any) {},
	}
	st := NewStatus()

	start := time.Now()
	_, err := handshake(context.Background(), rc, opt, st, "")
	if err == nil {
		t.Fatal("handshake should fail when the head unit never answers")
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("handshake took %s: the timeout did not bound a blocked read", d)
	}
}

func TestHandshakeHonoursContextCancel(t *testing.T) {
	rc := newBlockingConn()
	opt := Options{
		HandshakeTimeout: time.Minute,
		Logf:             func(string, ...any) {},
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	if _, err := handshake(ctx, rc, opt, NewStatus(), ""); err == nil {
		t.Fatal("handshake should return the context error")
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("handshake took %s to notice the cancellation", d)
	}
}
