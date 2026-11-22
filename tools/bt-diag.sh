#!/bin/sh
# Capture the Bluetooth state and the HCI bridge journal for a post-mortem.
# Run this *before* rebooting: the journal is now persistent, so it survives,
# but the dmesg ring buffer does not.
#
# The bridge (bluebinder <-> AppSupport) is the layer that wedges when the radio
# stops answering: look for "Writing packet from HAL to vhci device failed" and
# for the kernel's "link tx timeout / killing stalled connection".
set -u
out="/home/defaultuser/bt-diag-$(date +%Y%m%d-%H%M%S).log"
{
	echo "== date =="
	date
	echo
	echo "== adapter =="
	busctl --system get-property org.bluez /org/bluez/hci1 org.bluez.Adapter1 \
		Address Powered Discoverable Pairable Connectable Discovering 2>&1
	echo
	echo "== timed out links in this boot (kernel) =="
	journalctl -b -k --no-pager 2>/dev/null | grep -iE "link tx timeout|killing stalled|hci[0-9].*(timeout|fail)" | tail -30
	echo
	echo "== bluebinder / appsupport-bridge-bt (this boot) =="
	journalctl -b -u bluebinder -u appsupport-bridge-bt -u appsupport-bridge-bt-aidl \
		--no-pager 2>/dev/null | tail -60
	echo
	echo "== same units (previous boots) =="
	journalctl --no-pager -u bluebinder -u appsupport-bridge-bt 2>/dev/null | tail -40
	echo
	echo "== our daemon tail =="
	tail -20 /home/defaultuser/wireless.log 2>/dev/null
} >"$out" 2>&1
echo "$out"
