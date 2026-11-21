package wireless

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"
)

// Controller runs the wireless bootstrap on demand and exposes start/stop.
// The status page keeps working while the connection attempts run.
type Controller struct {
	Opt    Options
	Status *Status
	Serve  func(net.Conn)

	start chan struct{}
	mu    sync.Mutex
	// running is true while Dial or Serve is active.
	running bool
	// stopped is set by Stop, so a natural session end reconnects but a user
	// Stop does not.
	stopped bool
	cancel  context.CancelFunc
	conn    net.Conn
}

// NewController builds a controller and wires the status into Options.
func NewController(opt Options, st *Status, serve func(net.Conn)) *Controller {
	if st == nil {
		st = NewStatus()
	}
	opt.Status = st
	return &Controller{
		Opt:    opt,
		Status: st,
		Serve:  serve,
		start:  make(chan struct{}, 1),
	}
}

// Run starts once, then reconnects after a natural end until Stop or ctx.
func (c *Controller) Run(ctx context.Context) {
	c.Start()
	for {
		select {
		case <-ctx.Done():
			c.Stop()
			return
		case <-c.start:
		}
		if ctx.Err() != nil {
			return
		}
		c.Status.Reset()
		c.Status.Running(true)

		runCtx, cancel := context.WithCancel(ctx)
		c.mu.Lock()
		c.cancel = cancel
		c.running = true
		c.mu.Unlock()

		conn, err := Dial(runCtx, c.Opt)
		if err != nil {
			c.Status.Error(err)
			c.Status.Set("Connexion", false, err.Error())
			c.finish()
		} else {
			c.mu.Lock()
			c.conn = conn
			c.mu.Unlock()
			if c.Serve != nil {
				c.Serve(conn)
			} else {
				_ = conn.Close()
			}
			c.finish()
		}

		c.mu.Lock()
		stopped := c.stopped
		c.mu.Unlock()
		if stopped {
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(3 * time.Second):
		}
		c.Start()
	}
}

func (c *Controller) finish() {
	c.mu.Lock()
	cancel := c.cancel
	c.cancel = nil
	c.conn = nil
	c.running = false
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	c.Status.Running(false)
	c.Status.Set("Session AA", false, "terminée")
}

// Start requests a connection attempt. It is a no-op while already running.
func (c *Controller) Start() {
	c.mu.Lock()
	c.stopped = false
	running := c.running
	c.mu.Unlock()
	if running {
		return
	}
	select {
	case c.start <- struct{}{}:
	default:
	}
}

// Stop cancels the current attempt/session and prevents auto-reconnect.
func (c *Controller) Stop() {
	c.mu.Lock()
	c.stopped = true
	cancel := c.cancel
	conn := c.conn
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if conn != nil {
		_ = conn.Close()
	}
}

// JoinWiFi asks ConnMan to join the SSID received from the head unit.
func (c *Controller) JoinWiFi() error {
	ssid := c.Status.WiFiWant()
	if ssid == "" {
		return fmt.Errorf("SSID inconnu : le handshake WPP n'a pas encore livré le nom du réseau")
	}
	return connmanConnect(ssid)
}
