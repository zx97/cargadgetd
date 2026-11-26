package wireless

import (
	"sort"
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

// demoteAbsent moves recently silent devices to the end, keeping the order
// inside each group.
func demoteAbsent(ds []*Device) {
	now := time.Now()
	absentMu.Lock()
	isAbsent := func(d *Device) bool {
		t, ok := absentTill[d.Address]
		if ok && now.After(t) {
			delete(absentTill, d.Address)
			return false
		}
		return ok
	}
	present := make([]*Device, 0, len(ds))
	var absent []*Device
	for _, d := range ds {
		if isAbsent(d) {
			absent = append(absent, d)
		} else {
			present = append(present, d)
		}
	}
	absentMu.Unlock()
	sort.SliceStable(absent, func(i, j int) bool { return false })
	copy(ds, append(present, absent...))
}
