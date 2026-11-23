package wireless

import (
	"fmt"
	"strings"
	"time"

	"github.com/godbus/dbus/v5"
)

// ConnMan agent
//
// A head unit's access point appears at the first wireless attempt, with a new
// SSID almost every session (carplay_wifi_34A5, B2AB, DC02, 5059, 0D60 on the
// lab unit) and a short window: its Wi-Fi mode times out if no phone joins.
// ConnMan will not connect an access point it has not saved ("no saved wifi
// service"), and provisioning files are unavailable on Sailfish (/var/lib/connman
// does not exist, connmand runs without its wifi plugin).
//
// The mechanism ConnMan itself uses when a user types a passphrase is an agent:
// it calls RequestInput("Passphrase") on a registered net.connman.Agent. This
// agent answers with the key the WPP handshake delivered, so ConnMan does the
// association, the DHCP lease, the route and its own UI bookkeeping — the work
// wpa_supplicant bypassed.

const (
	connmanAgentPath  = dbus.ObjectPath("/cargadgetd/connman/agent")
	connmanAgentIface = "net.connman.Agent"
)

// connmanAgent answers the passphrase request for the head unit's AP.
type connmanAgent struct {
	psk  string
	logf func(string, ...any)
}

// RequestInput is where ConnMan asks for the passphrase. Only the keys it asks
// for are returned; anything else upsets it.
func (a *connmanAgent) RequestInput(service dbus.ObjectPath, fields map[string]dbus.Variant) (map[string]dbus.Variant, *dbus.Error) {
	out := make(map[string]dbus.Variant, len(fields))
	for k := range fields {
		switch strings.ToLower(k) {
		case "passphrase", "password", "psk":
			out[k] = dbus.MakeVariant(a.psk)
			if a.logf != nil {
				a.logf("connman: agent supplied %s for %s", k, service)
			}
		}
	}
	return out, nil
}

// ReportError tells us ConnMan refused the key or failed the association.
func (a *connmanAgent) ReportError(service dbus.ObjectPath, err string) *dbus.Error {
	if a.logf != nil {
		a.logf("connman: agent error for %s: %s", service, err)
	}
	return nil
}

// ReportPeerError and the peer-authorization calls are part of the interface;
// a head unit's AP never uses them.
func (a *connmanAgent) ReportPeerError(service dbus.ObjectPath, peer string, err string) *dbus.Error {
	return nil
}

func (a *connmanAgent) RequestPeerAuthorization(service dbus.ObjectPath, peer string) *dbus.Error {
	return nil
}

func (a *connmanAgent) Release() *dbus.Error { return nil }
func (a *connmanAgent) Cancel() *dbus.Error  { return nil }

// ConnManStateFunc is called on every state change of a service, so the caller
// can log "association" then "ready" instead of silence.
type ConnManStateFunc func(state string)

// JoinWithAgent asks ConnMan to connect ssid, answering its passphrase prompt.
// It returns once ConnMan reports the service ready (DHCP done) or fails.
func JoinWithAgent(ssid, psk string, logf func(string, ...any)) error {
	conn, err := dbus.SystemBus()
	if err != nil {
		return fmt.Errorf("connman: system bus: %w", err)
	}
	agent := &connmanAgent{psk: psk, logf: logf}
	if err := conn.Export(agent, connmanAgentPath, connmanAgentIface); err != nil {
		return fmt.Errorf("connman: export agent: %w", err)
	}
	defer conn.Export(nil, connmanAgentPath, connmanAgentIface)

	mgr := conn.Object(connmanService, "/")
	if err := mgr.Call("net.connman.Manager.RegisterAgent", 0, connmanAgentPath).Err; err != nil {
		return fmt.Errorf("connman: register agent: %w", err)
	}
	defer mgr.Call("net.connman.Manager.UnregisterAgent", 0, connmanAgentPath)

	// The AP must be in a scan result before it can be connected: this is the
	// step that used to fail with "no saved wifi service".
	svc, err := waitConnmanService(conn, ssid, 20*time.Second, logf)
	if err != nil {
		return err
	}

	connectErr := make(chan error, 1)
	go func() {
		connectErr <- conn.Object(connmanService, svc).Call("net.connman.Service.Connect", 0).Err
	}()

	deadline := time.Now().Add(30 * time.Second)
	for {
		state := connmanServiceState(conn, svc)
		if state == "ready" || state == "online" {
			if logf != nil {
				logf("connman: %q %s", ssid, state)
			}
			return nil
		}
		if state == "failure" {
			return fmt.Errorf("connman: %q failed to connect", ssid)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("connman: %q not ready after 30s (state=%q)", ssid, state)
		}
		select {
		case cerr := <-connectErr:
			if cerr != nil {
				return fmt.Errorf("connman: connect %q: %w", ssid, cerr)
			}
			connectErr = nil // consumed once; keep polling the state
		default:
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// waitConnmanService scans until ConnMan lists a wifi service named ssid.
func waitConnmanService(conn *dbus.Conn, ssid string, wait time.Duration, logf func(string, ...any)) (dbus.ObjectPath, error) {
	deadline := time.Now().Add(wait)
	scanAt := time.Now()
	for {
		services, err := connmanWiFiServices()
		if err == nil {
			for _, s := range services {
				if strings.EqualFold(s.Name, ssid) {
					if logf != nil {
						logf("connman: %q found (state=%s)", ssid, s.State)
					}
					return s.Path, nil
				}
			}
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("connman: %q never appeared in a scan", ssid)
		}
		if time.Since(scanAt) > 2*time.Second {
			connmanScan()
			scanAt = time.Now()
		}
		time.Sleep(300 * time.Millisecond)
	}
}

// connmanServiceState reads Service.State.
func connmanServiceState(conn *dbus.Conn, path dbus.ObjectPath) string {
	var v dbus.Variant
	if err := conn.Object(connmanService, path).
		Call("org.freedesktop.DBus.Properties.Get", 0, "net.connman.Service", "State").Store(&v); err != nil {
		return ""
	}
	s, _ := v.Value().(string)
	return s
}
