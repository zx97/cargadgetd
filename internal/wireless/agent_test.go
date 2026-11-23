package wireless

import "testing"

// A head unit with a legacy PIN needs the code it displays sent back. BlueZ
// asks through RequestPinCode (four digits) or RequestPasskey (SSP); both must
// answer with the configured PIN, otherwise Sailfish's six-digit dialog decides
// the pairing and the bond is unusable.
func TestPinAgentAnswers(t *testing.T) {
	a := &pinAgent{pin: "0000"}

	if got, err := a.RequestPinCode("/org/bluez/hci1/dev_41_00_00_00_9E_F0"); err != nil || got != "0000" {
		t.Fatalf("RequestPinCode = %q, %v", got, err)
	}
	if got, err := a.RequestPasskey("/org/bluez/hci1/dev_41_00_00_00_9E_F0"); err != nil || got != 0 {
		t.Fatalf("RequestPasskey = %d, %v", got, err)
	}
	if err := a.RequestConfirmation("/org/bluez/hci1/dev_41_00_00_00_9E_F0", 123456); err != nil {
		t.Fatalf("RequestConfirmation = %v", err)
	}
	if n := len(a.Requests()); n != 3 {
		t.Fatalf("recorded %d calls, want 3", n)
	}
}

func TestPinAgentCustomPin(t *testing.T) {
	a := &pinAgent{pin: "1234"}
	if got, _ := a.RequestPinCode("/org/bluez/hci1/dev_x"); got != "1234" {
		t.Fatalf("RequestPinCode = %q, want 1234", got)
	}
	if got, _ := a.RequestPasskey("/org/bluez/hci1/dev_x"); got != 1234 {
		t.Fatalf("RequestPasskey = %d, want 1234", got)
	}
}
