package session

import "testing"

func TestHandshakeButtonsAcceptsEverything(t *testing.T) {
	got := handshakeButtons(nil)
	for _, h := range huInputs {
		if !containsInt(got, h.Code) {
			t.Fatalf("missing %d %s in %v", h.Code, h.Name, got)
		}
	}
	for _, c := range []int{5, 6, 85, 87, 88, 126, 127} {
		if !containsInt(got, c) {
			t.Fatalf("missing media/call %d in %v", c, got)
		}
	}
}

func TestHandshakeButtonsBindsOnlyTheAdvertisedKeys(t *testing.T) {
	// The Melia answers any unadvertised code with STATUS_KEYCODE_NOT_BOUND
	// (-18) and refuses the whole binding, so the set must be the SDR list.
	got := handshakeButtons([]int{7, 8, 3, 200, 3})
	want := []int{7, 8, 3, 200}
	if len(got) != len(want) {
		t.Fatalf("want %v, got %v", want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("want %v, got %v", want, got)
		}
	}
	if containsInt(got, 87) {
		t.Fatalf("bound a key the head unit never listed: %v", got)
	}
}

func TestHandshakeButtonsFallsBackToKnownWhenNoneAdvertised(t *testing.T) {
	got := handshakeButtons([]int{0, -1})
	for _, h := range huInputs {
		if !containsInt(got, h.Code) {
			t.Fatalf("missing %d %s in %v", h.Code, h.Name, got)
		}
	}
}

func TestHandshakeBodyIsPacked(t *testing.T) {
	body := handshakeBody([]int{85, 87, 65536})
	if len(body) < 2 || body[0] != 0x0a {
		t.Fatalf("want one packed field 1, got %x", body)
	}
	got := parsePackedVarints(body[2:])
	if len(got) != 3 || got[0] != 85 || got[1] != 87 || got[2] != 65536 {
		t.Fatalf("%v from %x", got, body)
	}
}

func TestHuInputName(t *testing.T) {
	n, ok := HuInputName(87)
	if !ok || n != "next" {
		t.Fatalf("87 → %q %v", n, ok)
	}
	n, ok = HuInputName(85)
	if !ok || n != "playpause" {
		t.Fatalf("85 → %q %v", n, ok)
	}
	n, ok = HuInputName(127)
	if !ok || n != "pause" {
		t.Fatalf("127 → %q %v", n, ok)
	}
	if _, ok := HuInputName(99); ok {
		t.Fatal("99 should be unknown")
	}
	if n, ok := HuInputName(82); !ok || n != "menu" {
		t.Fatalf("android menu %q %v", n, ok)
	}
	if n, ok := HuInputName(126); !ok || n != "play" {
		t.Fatalf("play %q %v", n, ok)
	}
}

func containsInt(s []int, v int) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
