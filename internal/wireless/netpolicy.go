package wireless

import (
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/cargadgetd/cargadgetd/internal/cfg"
)

// carIface remembers the interface the car's access point put us on, so the end
// of the session restores the right default route.
var carIface struct {
	sync.Mutex
	name string
}

// applyNetPolicy runs the setuid helper's -net-policy: root is needed to change
// routes, and the daemon runs as the user.
func applyNetPolicy(mode, iface string, logf func(string, ...any)) {
	if iface == "" {
		return
	}
	bin := cfg.Get().Gadget.Bin
	if bin == "" {
		return
	}
	out, err := exec.Command(bin, "-net-policy", mode, "-net-iface", iface).CombinedOutput()
	msg := strings.TrimSpace(string(out))
	if err != nil {
		logf("route: helper -net-policy %s failed: %v (%s)", mode, err, lastLine(msg))
		return
	}
	if msg != "" {
		logf("route: %s", lastLine(msg))
	}
}

// applyCarRoutes routes only the car's subnet over the access point, leaving the
// internet on cellular, and re-asserts it for half a minute: ConnMan re-installs
// the service's default route when it finishes its own bookkeeping, which is
// exactly the moment the internet would disappear again.
func applyCarRoutes(iface string, logf func(string, ...any)) {
	if iface == "" {
		return
	}
	carIface.Lock()
	carIface.name = iface
	carIface.Unlock()
	applyNetPolicy("car", iface, logf)
	go func() {
		for i := 0; i < 6; i++ {
			time.Sleep(5 * time.Second)
			applyNetPolicy("car", iface, logf)
		}
	}()
}

// restoreCarRoutes puts the access point's default route back when the session
// is over, so the phone behaves exactly as before we touched it.
func restoreCarRoutes(logf func(string, ...any)) {
	carIface.Lock()
	iface := carIface.name
	carIface.name = ""
	carIface.Unlock()
	if iface == "" {
		iface = "wlan0"
	}
	applyNetPolicy("restore", iface, logf)
}
