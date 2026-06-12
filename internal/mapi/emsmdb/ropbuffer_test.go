package emsmdb

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/umailserver/umailserver/internal/mapi/wire"
)

// buildFragment assembles a single RPC_HEADER_EXT-prefixed fragment around an
// already-formed inner payload, for the hand-constructed decode tests.
func buildFragment(flags uint16, payload []byte) []byte {
	hdr := wire.RPCHeaderExt{Flags: flags, Size: uint16(len(payload)), SizeActual: uint16(len(payload))}
	p := wire.NewPush(0)
	hdr.Push(p)
	p.Raw(payload)
	return p.Bytes()
}

// innerPayload forms the RopListSize-prefixed payload (ROP bytes + handles) that
// lives inside one or more fragments.
func innerPayload(ropData []byte, handles []uint32) []byte {
	p := wire.NewPush(0)
	p.Uint16(uint16(2 + len(ropData)))
	p.Raw(ropData)
	for _, h := range handles {
		p.Uint32(h)
	}
	return p.Bytes()
}

// TestROPBufferRoundTrip checks the uncompressed Encode→Decode cycle preserves
// the ROP bytes and the handle table.
func TestROPBufferRoundTrip(t *testing.T) {
	ropData := []byte{0x02, 0x00, 0x01, 0x02, 0x03} // arbitrary opaque ROP bytes
	handles := []uint32{0xFFFFFFFF, 0x00000000, 0x12345678}

	buf := EncodeROPBuffer(0, ropData, handles, false)
	ver, gotData, gotHandles, err := DecodeROPBuffer(buf)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if ver != 0 {
		t.Errorf("version = %d, want 0", ver)
	}
	if !bytes.Equal(gotData, ropData) {
		t.Errorf("ropData = % x, want % x", gotData, ropData)
	}
	if len(gotHandles) != len(handles) {
		t.Fatalf("handles = %v, want %v", gotHandles, handles)
	}
	for i := range handles {
		if gotHandles[i] != handles[i] {
			t.Errorf("handle[%d] = %#x, want %#x", i, gotHandles[i], handles[i])
		}
	}
}

// TestROPBufferCompressed verifies that a compressible payload is emitted with
// the compressed flag set and still decodes to the original.
func TestROPBufferCompressed(t *testing.T) {
	ropData := bytes.Repeat([]byte{0xAB, 0xCD}, 2000)
	handles := []uint32{1, 2}

	buf := EncodeROPBuffer(0, ropData, handles, true)
	flags := binary.LittleEndian.Uint16(buf[2:4])
	if flags&wire.RHEFlagCompressed == 0 {
		t.Fatal("expected the compressed flag to be set for a repetitive payload")
	}
	if len(buf) >= 2+len(ropData) {
		t.Errorf("compressed buffer (%d) not smaller than raw payload (%d)", len(buf), len(ropData))
	}
	_, gotData, gotHandles, err := DecodeROPBuffer(buf)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !bytes.Equal(gotData, ropData) || len(gotHandles) != 2 {
		t.Error("compressed round-trip mismatch")
	}
}

// TestROPBufferDeobfuscates ensures an XOR-obfuscated fragment is recovered.
func TestROPBufferDeobfuscates(t *testing.T) {
	ropData := []byte{0x10, 0x20, 0x30}
	payload := innerPayload(ropData, []uint32{7})
	obf := append([]byte(nil), payload...)
	deobfuscate(obf) // XOR with 0xA5 to obfuscate (involution)
	buf := buildFragment(wire.RHEFlagLast|wire.RHEFlagXorMagic, obf)

	_, gotData, gotHandles, err := DecodeROPBuffer(buf)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !bytes.Equal(gotData, ropData) || len(gotHandles) != 1 || gotHandles[0] != 7 {
		t.Errorf("deobfuscated = % x / %v, want % x / [7]", gotData, gotHandles, ropData)
	}
}

// TestROPBufferMultiFragment verifies reassembly across two fragments where only
// the second carries RHE_FLAG_LAST.
func TestROPBufferMultiFragment(t *testing.T) {
	ropData := []byte{0xDE, 0xAD, 0xBE, 0xEF}
	payload := innerPayload(ropData, []uint32{9})
	split := len(payload) / 2
	frag1 := buildFragment(0, payload[:split])                // not last
	frag2 := buildFragment(wire.RHEFlagLast, payload[split:]) // last
	buf := append(frag1, frag2...)

	_, gotData, gotHandles, err := DecodeROPBuffer(buf)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !bytes.Equal(gotData, ropData) || len(gotHandles) != 1 || gotHandles[0] != 9 {
		t.Errorf("reassembled = % x / %v, want % x / [9]", gotData, gotHandles, ropData)
	}
}

// TestROPBufferTruncated rejects a header that promises more bytes than exist.
func TestROPBufferTruncated(t *testing.T) {
	hdr := wire.RPCHeaderExt{Flags: wire.RHEFlagLast, Size: 100, SizeActual: 100}
	p := wire.NewPush(0)
	hdr.Push(p)
	p.Raw([]byte{0x01, 0x02}) // far fewer than 100 bytes
	if _, _, _, err := DecodeROPBuffer(p.Bytes()); err == nil {
		t.Error("expected ErrCorrupt for a truncated fragment")
	}
}
