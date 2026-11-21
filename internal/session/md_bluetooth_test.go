package session

import (
	"bytes"
	"io"
	"log"
	"testing"
)

// btChannelBlob builds a Channel descriptor with channel_id and a
// bluetooth_channel{adapter_address, supported_pairing_methods} payload, the
// shape the head unit puts in ServiceDiscoveryResponse.channels (field 6).
func btChannelBlob(id uint64, adapter string, methods []uint32) []byte {
	var bt []byte
	bt = append(bt, encodeProtoString(1, adapter)...)
	if len(methods) > 0 {
		packed := []byte{}
		for _, m := range methods {
			packed = append(packed, byte(m))
		}
		bt = append(bt, 0x12, byte(len(packed)))
		bt = append(bt, packed...)
	}
	b := []byte{0x08, byte(id)}
	b = append(b, 0x32, byte(len(bt)))
	return append(b, bt...)
}

func TestParseBluetoothChannel(t *testing.T) {
	blob := btChannelBlob(10, "10:32:2C:50:5D:5E", []uint32{2, 4})
	if got, kind := parseChannelBlob(blob); got != 10 || kind != "bt" {
		t.Fatalf("parseChannelBlob = (%d, %q), want (10, bt)", got, kind)
	}
	addr, methods := parseBluetoothChannel(blob)
	if addr != "10:32:2C:50:5D:5E" {
		t.Fatalf("adapter address = %q", addr)
	}
	if len(methods) != 2 || methods[0] != 2 || methods[1] != 4 {
		t.Fatalf("pairing methods = %v, want [2 4]", methods)
	}
}

func TestParseSDRRecordsBluetoothChannel(t *testing.T) {
	md := NewMD(new(bytes.Buffer), nil, log.New(io.Discard, "", 0))
	// ServiceDiscoveryResponse: repeated Channel channels = 1.
	blob := btChannelBlob(10, "10:32:2C:50:5D:5E", []uint32{2})
	sdr := append([]byte{0x0a, byte(len(blob))}, blob...)
	md.parseSDR(sdr)
	if md.BluetoothCh != 10 {
		t.Fatalf("BluetoothCh = %d, want 10", md.BluetoothCh)
	}
	if len(md.btMethods) != 1 || md.btMethods[0] != 2 {
		t.Fatalf("btMethods = %v, want [2]", md.btMethods)
	}
}

func TestParseBluetoothPairingResponse(t *testing.T) {
	// already_paired=true, status=OK
	already, status := parseBluetoothPairingResponse([]byte{0x08, 0x01, 0x10, 0x01})
	if !already || status != 1 {
		t.Fatalf("parseBluetoothPairingResponse = (%v, %d), want (true, 1)", already, status)
	}
	// already_paired absent, status=FAIL
	already, status = parseBluetoothPairingResponse([]byte{0x10, 0x02})
	if already || status != 2 {
		t.Fatalf("parseBluetoothPairingResponse = (%v, %d), want (false, 2)", already, status)
	}
}

func TestBluetoothPairingBody(t *testing.T) {
	body := bluetoothPairingBody("50:56:A8:06:03:39", 2)
	want := append(encodeProtoString(1, "50:56:A8:06:03:39"), 0x10, 0x02)
	if !bytes.Equal(body, want) {
		t.Fatalf("pairing body = %x, want %x", body, want)
	}
	// NONE omits the enum field.
	body = bluetoothPairingBody("50:56:A8:06:03:39", 0)
	if !bytes.Equal(body, encodeProtoString(1, "50:56:A8:06:03:39")) {
		t.Fatalf("pairing body with NONE = %x", body)
	}
}
