package wireless

import (
	"strings"
	"testing"
)

func TestNextStepTellsWhatToDo(t *testing.T) {
	tests := []struct {
		name   string
		snap   Snapshot
		want   string
		kind   string
		absent string
	}{
		{
			name: "stopped",
			snap: Snapshot{},
			want: "Start the daemon",
			kind: "action",
		},
		{
			name: "car off",
			snap: Snapshot{Error: "car absent: Bluetooth unreachable (ignition on?)"},
			want: "Turn the car on",
			kind: "wait",
		},
		{
			name: "waiting for the car",
			snap: Snapshot{
				Running:       true,
				Phase:         "profile",
				BluetoothName: "Manolla",
				Checks:        []Check{{Name: "Bluetooth paired", OK: true}},
			},
			want: "« Manolla »",
			kind: "action",
		},
		{
			name: "not paired yet",
			snap: Snapshot{
				Running: true,
				Phase:   "bluetooth",
				Checks:  []Check{{Name: "Bluetooth paired", OK: false}},
			},
			want: "Pair the phone",
			kind: "action",
		},
		{
			name: "connected",
			snap: Snapshot{Running: true, Phase: "session"},
			want: "You can drive",
			kind: "ok",
		},
		{
			name: "usb launcher stopped",
			snap: Snapshot{USB: USBSnapshot{Transport: "usb", ServiceState: "inactive"}},
			want: "Repair USB",
			kind: "action",
		},
		{
			name: "car not paired",
			snap: Snapshot{
				BluetoothName: "Manolla",
				Error:         "no paired car: pair the phone from the car's Bluetooth menu",
			},
			want: "Pair Manolla",
			kind: "action",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, kind := nextStep("en", tt.snap)
			if !strings.Contains(got, tt.want) {
				t.Fatalf("nextStep() = %q, want it to mention %q", got, tt.want)
			}
			if kind != tt.kind {
				t.Fatalf("nextStep() kind = %q, want %q", kind, tt.kind)
			}
		})
	}
}

// TestNextStepFrench checks that the same rules render in French, including a
// detail that went through Setf.
func TestNextStepFrench(t *testing.T) {
	snap := Snapshot{Running: true, Phase: "session"}
	got, kind := nextStep("fr", snap)
	if !strings.Contains(got, "Tu peux rouler") {
		t.Fatalf("nextStep(fr) = %q", got)
	}
	if kind != "ok" {
		t.Fatalf("nextStep(fr) kind = %q", kind)
	}
}

// TestLocalizeTranslatesDetails makes sure a Setf detail follows the language
// and a static detail falls back to its English form.
func TestLocalizeTranslatesDetails(t *testing.T) {
	st := NewStatus()
	st.Set("Bluetooth link", true, "link up")
	st.Setf("WPP version", true, "%d.%d", 1, 6)
	for _, lang := range []string{"en", "fr"} {
		snap := st.Snapshot()
		snap.Localize(lang)
		for _, c := range snap.Checks {
			switch c.Name {
			case T(lang, "Bluetooth link"):
				want := T(lang, "link up")
				if c.Detail != want {
					t.Fatalf("%s: detail = %q, want %q", lang, c.Detail, want)
				}
			case T(lang, "WPP version"):
				want := TF(lang, "%d.%d", 1, 6)
				if c.Detail != want {
					t.Fatalf("%s: detail = %q, want %q", lang, c.Detail, want)
				}
			}
		}
	}
}

// TestStepsFollowLanguage checks the feed is formatted at render time.
func TestStepsFollowLanguage(t *testing.T) {
	st := NewStatus()
	st.Step("Car found: %s (%s)", "MULTIVAN", "AA:BB")
	st.Step("WPP version %d.%d", 1, 6)
	snap := st.Snapshot()
	for _, lang := range []string{"en", "fr"} {
		snap.Localize(lang)
		joined := strings.Join(snap.Steps, "\n")
		if !strings.Contains(joined, TF(lang, "Car found: %s (%s)", "MULTIVAN", "AA:BB")) {
			t.Fatalf("%s: feed = %q", lang, joined)
		}
	}
}
