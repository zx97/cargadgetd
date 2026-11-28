package wireless

import (
	"sync"
	"time"
)

// absentFor is how long a head unit that did not answer the page is tried last.
// Long enough to stop a parked car from eating every cycle, short enough that
// starting it again is picked up without a restart.
const absentFor = 2 * time.Minute

var (
	absentMu   sync.Mutex
	absentTill = map[string]time.Time{}
)

// NoteAbsent records that a device did not answer, so the next cycles try the
// units that are actually there first. The VW in the car park and the lab unit
// on the bench are both paired to this phone: without this, every cycle began
// with the absent one and the present one got what was left of its window.
func NoteAbsent(addr string) {
	if addr == "" {
		return
	}
	absentMu.Lock()
	absentTill[addr] = time.Now().Add(absentFor)
	absentMu.Unlock()
}

// NotePresent clears the mark, for a unit that just answered.
func NotePresent(addr string) {
	absentMu.Lock()
	delete(absentTill, addr)
	absentMu.Unlock()
}

// absentNow reports whether addr recently failed to answer, clearing a mark
// that has expired. Ordering uses it as one sort key: see orderCandidates.
func absentNow(addr string) bool {
	absentMu.Lock()
	defer absentMu.Unlock()
	t, ok := absentTill[addr]
	if !ok {
		return false
	}
	if time.Now().After(t) {
		delete(absentTill, addr)
		return false
	}
	return true
}
