//go:build !linux

package wireless

// USBSnapshot is what the status page shows about the wired path.
type USBSnapshot struct {
	Transport       string
	ServiceState    string
	Mode            string
	VendorID        string
	AccessoryLinked bool
	SessionRunning  bool
	Log             []string
	LogPath         string
}

// USBStatus is Linux-only; elsewhere the wired path does not exist.
func USBStatus() USBSnapshot { return USBSnapshot{Transport: "usb", Mode: "absent"} }

// RepairUSB is Linux-only.
func RepairUSB() error { return errUSBLinuxOnly }

type usbStubError struct{}

func (usbStubError) Error() string { return "USB: Linux uniquement" }

var errUSBLinuxOnly = usbStubError{}
