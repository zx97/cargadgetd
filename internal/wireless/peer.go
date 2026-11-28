package wireless

import "strings"

// PeerChoice is the Bluetooth target for one wireless cycle, with the reason
// it was chosen. The reason is logged verbatim: when a session refuses to
// start, "which device did we dial, and why that one" is the first question.
type PeerChoice struct {
	// Name and Addr are the selectors handed to FindDevices. Addr wins when
	// both are set (an address is exact, a name is a substring).
	Name, Addr string
	// Why says where the choice came from, for the log.
	Why string
	// Exact is true when the choice names one device, false when the daemon
	// still has to pick from several (discovery).
	Exact bool
}

// ResolvePeer decides which head unit the wireless cycle targets.
//
// Two contracts, in this order:
//
//   - an explicit selector wins: -wireless-addr (the bench handle), then
//     -wireless-name, then daemon.peer. An address is exact; a name is a
//     substring match, which is how units with a random address are handled.
//   - "auto" (or an empty daemon.peer) hands the decision to the candidate
//     ordering in FindDevices: audio/video class first, then connected, then
//     the Android Auto UUID, with units that just failed to page demoted.
//
// The explicit path is what makes repeated sessions deterministic: with three
// paired devices (a PC, a headset and the car) nothing is left to rank.
func ResolvePeer(flagName, flagAddr, cfgPeer string) PeerChoice {
	if a := strings.TrimSpace(flagAddr); a != "" {
		return PeerChoice{Name: strings.TrimSpace(flagName), Addr: a, Why: "-wireless-addr", Exact: true}
	}
	if p := strings.TrimSpace(cfgPeer); p != "" && !isAutoPeer(p) {
		if isAddress(p) {
			return PeerChoice{Addr: p, Why: "daemon.peer address", Exact: true}
		}
		return PeerChoice{Name: p, Why: "daemon.peer name", Exact: true}
	}
	if n := strings.TrimSpace(flagName); n != "" {
		return PeerChoice{Name: n, Why: "-wireless-name", Exact: true}
	}
	if p := strings.TrimSpace(cfgPeer); isAutoPeer(p) {
		return PeerChoice{Why: "daemon.peer auto (discovery)", Exact: false}
	}
	return PeerChoice{Why: "no selector (discovery)", Exact: false}
}

// isAutoPeer reports whether a daemon.peer value asks for discovery rather than
// naming a device. Empty is handled by the callers so the log can tell "nobody
// configured a peer" apart from "the user asked for auto".
func isAutoPeer(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "auto", "any", "discovery":
		return true
	}
	return false
}
