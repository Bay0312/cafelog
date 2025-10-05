package proto

import (
	"bytes"
	"testing"
)

func TestFrameRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	payload := []byte(`{"hello":"world"}`)

	if err := WriteFrame(&buf, TypeProduce, payload); err != nil {
		t.Fatalf("write error: %v", err)
	}
	fr, err := ReadFrame(&buf)
	if err != nil {
		t.Fatalf("read error: %v", err)
	}
	if fr.Type != TypeProduce {
		t.Fatalf("type mismatch: got %d", fr.Type)
	}
	if string(fr.Payload) != string(payload) {
		t.Fatalf("payload mismatch: got=%s want=%s", fr.Payload, payload)
	}
}