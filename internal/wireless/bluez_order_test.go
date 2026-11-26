package wireless

import "testing"

func TestOrderCandidatesPrefersTheConnectedUnit(t *testing.T) {
	parked := &Device{Address: "10:32:2C:50:5D:5E", Name: "MULTIVAN", Paired: true, Connected: false, UUIDs: []string{aaUUIDText}}
	lab := &Device{Address: "41:00:00:00:9E:F0", Name: "CAR BT_603F", Paired: true, Connected: true, UUIDs: []string{aaUUIDText}}
	phone := &Device{Address: "C8:94:02:16:28:74", Name: "manux", Paired: true, Connected: false}

	got := []*Device{parked, phone, lab}
	orderCandidates(got)
	if got[0] != lab {
		t.Fatalf("first candidate is %q (connected=%v), want the connected lab unit", got[0].Name, got[0].Connected)
	}
}

func TestOrderCandidatesPrefersAAUUIDAmongEquals(t *testing.T) {
	aa := &Device{Address: "11:11:11:11:11:11", Name: "car", Paired: true, UUIDs: []string{aaUUIDText}}
	plain := &Device{Address: "00:00:00:00:00:01", Name: "phone", Paired: true}

	got := []*Device{plain, aa}
	orderCandidates(got)
	if got[0] != aa {
		t.Fatalf("first candidate is %q, want the one advertising the AA UUID", got[0].Name)
	}
}
