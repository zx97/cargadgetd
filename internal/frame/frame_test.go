package frame

import (
	"bytes"
	"testing"
)

func TestRoundTripBulk(t *testing.T) {
	var buf bytes.Buffer
	in := Frame{Channel: 1, Flags: FlagEncrypted, Payload: []byte("hello-aa")}
	if err := Write(&buf, in); err != nil {
		t.Fatal(err)
	}
	out, err := Read(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if out.Channel != 1 || out.Flags&FlagEncrypted == 0 || string(out.Payload) != "hello-aa" {
		t.Fatalf("got %+v", out)
	}
}

func TestFragmentRoundTrip(t *testing.T) {
	payload := bytes.Repeat([]byte("x"), 4000)
	var buf bytes.Buffer
	if err := Write(&buf, Frame{Channel: 2, Flags: FlagEncrypted, Payload: payload}); err != nil {
		t.Fatal(err)
	}
	var got []byte
	for {
		f, err := Read(&buf)
		if err != nil {
			break
		}
		got = append(got, f.Payload...)
		if f.Flags&FlagLast != 0 {
			break
		}
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("len got=%d want=%d", len(got), len(payload))
	}
}
