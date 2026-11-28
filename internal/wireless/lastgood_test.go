package wireless

import (
	"os"
	"testing"
)

// The learned memory is a plain file, written whole and read back.
func TestLastGoodRoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	forgetLastGood()
	if got := LastGood(); got != "" {
		t.Fatalf("empty home returned %q", got)
	}
	NoteGood("AA:BB:CC:DD:EE:FF")
	if got := LastGood(); got != "AA:BB:CC:DD:EE:FF" {
		t.Fatalf("LastGood = %q", got)
	}
	// A fresh process reads the file: drop the in-memory copy, keep the file.
	lastGoodMu.Lock()
	lastGoodAddr, lastGoodRead = "", false
	lastGoodMu.Unlock()
	if got := LastGood(); got != "AA:BB:CC:DD:EE:FF" {
		t.Fatalf("file read back as %q", got)
	}
	if _, err := os.Stat(lastGoodPath()); err != nil {
		t.Fatalf("file: %v", err)
	}
	forgetLastGood()
}

// The unit that carried the last session is dialled first, even when it was
// just marked silent and the other unit was not: that is the 11.7s the bench
// spent paging a parked unit.
func TestOrderCandidatesPrefersTheLastGoodUnit(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	resetAbsent(t)
	forgetLastGood()

	mib3 := &Device{Address: "10:32:2C:50:5D:5E", Name: "head-unit-B", Paired: true, Class: classMIB3}
	melia := &Device{Address: "41:00:00:00:9E:F0", Name: "head-unit-A", Paired: true, Class: classMelia}
	NoteGood(mib3.Address)
	NoteAbsent(mib3.Address) // it just failed to page, and the mark is fresh
	orderCandidates([]*Device{melia, mib3})

	got := []*Device{melia, mib3}
	orderCandidates(got)
	if got[0] != mib3 {
		t.Fatalf("first candidate is %q, want the last good unit", got[0].Name)
	}
	forgetLastGood()
}

// Learning must never beat the class rule: a computer is still last.
func TestLastGoodCannotPromoteAComputer(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	resetAbsent(t)
	forgetLastGood()

	pc := &Device{Address: "C8:94:02:16:28:74", Name: "laptop", Paired: true, Class: classLaptop}
	car := &Device{Address: "41:00:00:00:9E:F0", Name: "head-unit-A", Paired: true, Class: classMelia}
	NoteGood(pc.Address)

	got := []*Device{pc, car}
	orderCandidates(got)
	if got[0] != car {
		t.Fatalf("first candidate is %q, want the car", got[0].Name)
	}
	forgetLastGood()
}
