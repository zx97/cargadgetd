package wireless

import (
	"fmt"
	"strings"
	"sync"
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

// The agent has to outlive the join.
//
// ConnMan hands the passphrase of an *unsaved* service (a head unit's AP, which
// Sailfish cannot store) through the registered agent, and it keeps that secret
// only while the agent exists. Unregistering at the end of the join made the
// station deauthenticate about 200 ms later — measured on the bench:
//
//	01:47:48.675 wifi: join #1 "carplay_wifi_8687" accepted by ConnMan in 30.481s
//	01:47:48.881 link: LOST wlan0 ssid="" ip="192.168.43.2"
//	01:47:49.890 link: wlan0 moved to ssid="carplay_wifi_8687"
//
// On the previous run the same deauthentication landed mid-session and the head
// unit tore the projection down: the picture, the audio and both MediaAck
// streams stopped 24 s after the session opened, with the TCP connection left
// half-open. The agent therefore stays registered until the phone leaves the
// head unit's access point (ReleaseAgent).
var (
	agentMu   sync.Mutex
	agentConn *dbus.Conn
	agentReg  bool
)

// keepAgent exports the passphrase agent and registers it with ConnMan, once.
func keepAgent(psk string, logf func(string, ...any)) error {
	agentMu.Lock()
	defer agentMu.Unlock()
	if agentConn == nil {
		conn, err := dbus.SystemBus()
		if err != nil {
			return fmt.Errorf("connman: system bus: %w", err)
		}
		agentConn = conn
	}
	agent := &connmanAgent{psk: psk, logf: logf}
	if err := agentConn.Export(agent, connmanAgentPath, connmanAgentIface); err != nil {
		return fmt.Errorf("connman: export agent: %w", err)
	}
	if !agentReg {
		mgr := agentConn.Object(connmanService, "/")
		if err := mgr.Call("net.connman.Manager.RegisterAgent", 0, connmanAgentPath).Err; err != nil {
			return fmt.Errorf("connman: register agent: %w", err)
		}
		agentReg = true
		if logf != nil {
			logf("connman: agent registered (kept for the whole session)")
		}
	}
	return nil
}

// ReleaseAgent unregisters the passphrase agent. Call it only once the phone is
// off the head unit's access point: while the service lives, ConnMan needs the
// agent to hold its secret.
func ReleaseAgent() {
	agentMu.Lock()
	defer agentMu.Unlock()
	if agentConn == nil || !agentReg {
		return
	}
	_ = agentConn.Object(connmanService, "/").
		Call("net.connman.Manager.UnregisterAgent", 0, connmanAgentPath).Err
	agentReg = false
	_ = agentConn.Export(nil, connmanAgentPath, connmanAgentIface)
}

// JoinWithAgent asks ConnMan to connect ssid, answering its passphrase prompt.
// It returns once ConnMan reports the service ready (DHCP done) or fails.
func JoinWithAgent(ssid, psk string, logf func(string, ...any)) error {
	conn, err := dbus.SystemBus()
	if err != nil {
		return fmt.Errorf("connman: system bus: %w", err)
	}
	if err := keepAgent(psk, logf); err != nil {
		return err
	}

	// The AP must be in a scan result before it can be connected: this is the
	// step that used to fail with "no saved wifi service".
	svc, err := waitConnmanService(conn, ssid, 20*time.Second, logf)
	if err != nil {
		return err
	}
	// Already up (wpa_supplicant autostart re-associates a network this phone
	// used before): a second Connect would re-apply the service and deauth the
	// station in the middle of the session.
	if st := connmanServiceState(conn, svc); st == "ready" || st == "online" {
		if logf != nil {
			logf("connman: %q already %s, no connect issued", ssid, st)
		}
		return nil
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
	scanAt := time.Time{} // first loop scans immediately

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
		if scanAt.IsZero() || time.Since(scanAt) > 2*time.Second {
			connmanScan()
			scanAt = time.Now()
		}
		time.Sleep(250 * time.Millisecond)
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
