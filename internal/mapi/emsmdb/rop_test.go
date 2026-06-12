package emsmdb

import (
	"bytes"
	"testing"
)

// TestReleaseProducesNoResponse confirms RopRelease frees the handle and emits
// no response bytes (MS-OXCROPS 2.2.15 has no response).
func TestReleaseProducesNoResponse(t *testing.T) {
	p := NewProcessor()
	sess := &Session{ID: "s", Email: "qa.bob@local.test"}
	resp, _ := p.Dispatch(sess, []byte{RopRelease, 0x00, 0x00}, []uint32{0xDEADBEEF}, 0x1000)
	if len(resp) != 0 {
		t.Errorf("Release response = % x, want empty", resp)
	}
}

// TestUnknownRopReturnsFailure verifies an unimplemented ROP yields the standard
// failure response (op id, handle index, result code) and stops parsing.
func TestUnknownRopReturnsFailure(t *testing.T) {
	p := NewProcessor()
	sess := &Session{ID: "s"}
	resp, _ := p.Dispatch(sess, []byte{0x99, 0x00, 0x05}, nil, 0x1000)
	// 0x99, hindex 0x05, ecNotImplemented (0x80040FFF) little-endian.
	want := []byte{0x99, 0x05, 0xFF, 0x0F, 0x04, 0x80}
	if !bytes.Equal(resp, want) {
		t.Errorf("unknown ROP response = % x, want % x", resp, want)
	}
}

// TestChainedReleaseThenUnknown verifies the loop processes a valid ROP then
// halts on the first unimplemented one.
func TestChainedReleaseThenUnknown(t *testing.T) {
	p := NewProcessor()
	sess := &Session{ID: "s"}
	data := []byte{RopRelease, 0x00, 0x00, 0x99, 0x00, 0x01}
	resp, _ := p.Dispatch(sess, data, []uint32{1}, 0x1000)
	want := []byte{0x99, 0x01, 0xFF, 0x0F, 0x04, 0x80}
	if !bytes.Equal(resp, want) {
		t.Errorf("chained response = % x, want only the failure % x", resp, want)
	}
}
