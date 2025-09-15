package proto

import (
	"encoding/binary"
	"fmt"
	"io"
)

const (
	TypeCreateTopic uint8 = 1
	TypeProduce     uint8 = 2
	TypeFetch       uint8 = 3
	TypeCommit      uint8 = 4
	TypeHeartbeat   uint8 = 5
)

type Frame struct {
	Type    uint8
	Payload []byte
}

func ReadFrame(r io.Reader) (Frame, error) {
	var t [1]byte
	if _, err := io.ReadFull(r, t[:]); err != nil {
		return Frame{}, err
	}
	var lenBuf [4]byte
	if _, err := io.ReadFull(r, lenBuf[:]); err != nil {
		return Frame{}, err
	}
	n := binary.BigEndian.Uint32(lenBuf[:])
	if n > 10<<20 { // 10MB guard
		return Frame{}, fmt.Errorf("frame too large: %d", n)
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		return Frame{}, err
	}
	return Frame{Type: t[0], Payload: buf}, nil
}

func WriteFrame(w io.Writer, t uint8, payload []byte) error {
	if _, err := w.Write([]byte{t}); err != nil {
		return err
	}
	var lenBuf [4]byte
	binary.BigEndian.PutUint32(lenBuf[:], uint32(len(payload)))
	if _, err := w.Write(lenBuf[:]); err != nil {
		return err
	}
	_, err := w.Write(payload)
	return err
}
