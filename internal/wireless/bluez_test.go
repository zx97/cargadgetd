package wireless

import (
	"errors"
	"testing"
)

// dbusErrText is called on errors from D-Bus; a nil argument used to panic the
// daemon right after a failed Bluetooth link.
func TestDBusErrTextHandlesNil(t *testing.T) {
	if got := dbusErrText(nil); got != "<nil>" {
		t.Fatalf("dbusErrText(nil) = %q, want %q", got, "<nil>")
	}
	if got := dbusErrText(errors.New("boom")); got != "boom" {
		t.Fatalf("dbusErrText(plain error) = %q, want %q", got, "boom")
	}
}
