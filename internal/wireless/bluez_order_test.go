package wireless

import (
	"testing"
	"time"
)

// Real class-of-device values from the bench: the Melia and the MIB3 are car
// kits, the laptop is a computer, the headset is audio/video like a car.
const (
	classMelia   = 0x240408
	classMIB3    = 0x740420
	classLaptop  = 0x7c0104
	classHeadset = 0x240418
)

func resetAbsent(t *testing.T) {
	t.Helper()
	absentMu.Lock()
	absentTill = map[string]time.Time{}
	absentMu.Unlock()
	t.Cleanup(func() {
		absentMu.Lock()
		absentTill = map[string]time.Time{}
		absentMu.Unlock()
	})
}

func TestOrderCandidatesPrefersTheConnectedUnit(t *testing.T) {
	resetAbsent(t)
	parked := &Device{Address: "10:32:2C:50:5D:5E", Name: "MULTIVAN", Paired: true, Class: classMIB3}
	lab := &Device{Address: "41:00:00:00:9E:F0", Name: "CAR BT_603F", Paired: true, Connected: true, Class: classMelia}
	pc := &Device{Address: "C8:94:02:16:28:74", Name: "manux", Paired: true, Class: classLaptop}

	got := []*Device{parked, pc, lab}
	orderCandidates(got)
	if got[0] != lab {
		t.Fatalf("first candidate is %q (connected=%v), want the connected lab unit", got[0].Name, got[0].Connected)
	}
}

func TestOrderCandidatesPrefersAAUUIDAmongEquals(t *testing.T) {
	resetAbsent(t)
	aa := &Device{Address: "11:11:11:11:11:11", Name: "car", Paired: true, Class: classMelia, UUIDs: []string{aaUUIDText}}
	plain := &Device{Address: "00:00:00:00:00:01", Name: "other", Paired: true, Class: classMelia}

	got := []*Device{plain, aa}
	orderCandidates(got)
	if got[0] != aa {
		t.Fatalf("first candidate is %q, want the one advertising the AA UUID", got[0].Name)
	}
}

// The regression measured on the bench: both head units had just failed to
// page, so they were marked absent, and the previously silent-demoted ordering
// put the *laptop* at candidate 1/4 — while the laptop is the one device that
// can never be the car. The class rule must dominate the absence flag.
//
// It also carries a trap found the same day: the laptop answers with the
// Android Auto UUID, because it once ran a head-unit emulator, while the VW
// does not advertise it at all. Class is the only signal that separates them.
func TestOrderCandidatesNeverPutsAComputerFirst(t *testing.T) {
	resetAbsent(t)
	melia := &Device{Address: "41:00:00:00:9E:F0", Name: "CAR BT_603F", Paired: true, Class: classMelia, UUIDs: []string{aaUUIDText}}
	mib3 := &Device{Address: "10:32:2C:50:5D:5E", Name: "MULTIVAN", Paired: true, Class: classMIB3}
	pc := &Device{Address: "C8:94:02:16:28:74", Name: "manux", Paired: true, Connected: true, Class: classLaptop, UUIDs: []string{aaUUIDText}}

	NoteAbsent(melia.Address)
	NoteAbsent(mib3.Address)

	got := []*Device{pc, melia, mib3}
	orderCandidates(got)
	if got[0] == pc {
		t.Fatalf("a connected laptop was placed first (%v)", names(got))
	}
	if !got[0].LooksLikeHeadUnit() || !got[1].LooksLikeHeadUnit() {
		t.Fatalf("the two head units must come first, got %v", names(got))
	}
	if got[2] != pc {
		t.Fatalf("the laptop must be last, got %v", names(got))
	}
}

// An absent head unit still goes after a present one, inside the same class.
func TestOrderCandidatesDemotesAnAbsentHeadUnit(t *testing.T) {
	resetAbsent(t)
	parked := &Device{Address: "10:32:2C:50:5D:5E", Name: "MULTIVAN", Paired: true, Class: classMIB3}
	lab := &Device{Address: "41:00:00:00:9E:F0", Name: "CAR BT_603F", Paired: true, Class: classMelia}
	NoteAbsent(parked.Address)

	got := []*Device{parked, lab}
	orderCandidates(got)
	if got[0] != lab {
		t.Fatalf("first candidate is %q, want the unit that is there", got[0].Name)
	}
}

func names(ds []*Device) []string {
	out := make([]string, len(ds))
	for i, d := range ds {
		out[i] = d.Name
	}
	return out
}
