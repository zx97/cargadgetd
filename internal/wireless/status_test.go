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
			want: "Démarrer",
			kind: "action",
		},
		{
			name: "car off",
			snap: Snapshot{Error: "voiture absente : Bluetooth injoignable (contact allumé ?)"},
			want: "Allume le contact",
			kind: "wait",
		},
		{
			name: "waiting for the car",
			snap: Snapshot{
				Running:       true,
				Phase:         "profile",
				BluetoothName: "Manolla",
				Checks:        []Check{{Name: "Bluetooth appairé", OK: true}},
			},
			want: "« Manolla »",
			kind: "action",
		},
		{
			name: "not paired yet",
			snap: Snapshot{
				Running: true,
				Phase:   "bluetooth",
				Checks:  []Check{{Name: "Bluetooth appairé", OK: false}},
			},
			want: "Appaire",
			kind: "action",
		},
		{
			name: "connected",
			snap: Snapshot{Running: true, Phase: "session"},
			want: "Tu peux rouler",
			kind: "ok",
		},
		{
			name: "usb launcher stopped",
			snap: Snapshot{USB: USBSnapshot{Transport: "usb", ServiceState: "inactive"}},
			want: "Réparer l'USB",
			kind: "action",
		},
		{
			name: "car not paired",
			snap: Snapshot{
				BluetoothName: "Manolla",
				Error:         "aucune voiture appairée : appaire le téléphone depuis le menu Bluetooth de la voiture",
			},
			want: "Appaire Manolla",
			kind: "action",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, kind := nextStep(tt.snap)
			if !strings.Contains(got, tt.want) {
				t.Fatalf("nextStep() = %q, want it to mention %q", got, tt.want)
			}
			if kind != tt.kind {
				t.Fatalf("nextStep() kind = %q, want %q", kind, tt.kind)
			}
		})
	}
}
