package lzx

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// TestEncodeUncompressedOddLength pins the exact bytes of a three-byte chunk.
// The expected layout is derived from MS-PATCH §2.2.3 by hand, independent of
// the encoder.
func TestEncodeUncompressedOddLength(t *testing.T) {
	got := EncodeUncompressed([]byte{0xAA, 0xBB, 0xCC})
	want := []byte{
		0x14, 0x00, // chunk size = 20
		0x00, 0x30, // word 0: E8=0, type=3, block_size[23:12]=0
		0x30, 0x00, // word 1: block_size[11:0]=3 << 4
		0x01, 0x00, 0x00, 0x00, // R0
		0x01, 0x00, 0x00, 0x00, // R1
		0x01, 0x00, 0x00, 0x00, // R2
		0xAA, 0xBB, 0xCC, // data
		0x00, // pad to 16-bit alignment
	}
	if !bytes.Equal(got, want) {
		t.Errorf("EncodeUncompressed(3 bytes) =\n% x\nwant\n% x", got, want)
	}
}

// TestEncodeUncompressedEvenLength pins a two-byte chunk: no pad byte is added.
func TestEncodeUncompressedEvenLength(t *testing.T) {
	got := EncodeUncompressed([]byte{0x01, 0x02})
	want := []byte{
		0x12, 0x00, // chunk size = 18
		0x00, 0x30, // word 0: E8=0, type=3, block_size hi=0
		0x20, 0x00, // word 1: block_size[11:0]=2 << 4
		0x01, 0x00, 0x00, 0x00,
		0x01, 0x00, 0x00, 0x00,
		0x01, 0x00, 0x00, 0x00,
		0x01, 0x02, // data, even length so no pad
	}
	if !bytes.Equal(got, want) {
		t.Errorf("EncodeUncompressed(2 bytes) =\n% x\nwant\n% x", got, want)
	}
}

// TestEncodeUncompressedBlockSizeSplit verifies the 24-bit block size is split
// across the two header words when it exceeds 12 bits.
func TestEncodeUncompressedBlockSizeSplit(t *testing.T) {
	const n = 0x1234
	got := EncodeUncompressed(make([]byte, n))

	if cs := binary.LittleEndian.Uint16(got[0:]); int(cs) != 4+12+n {
		t.Errorf("chunk size = %d, want %d", cs, 4+12+n)
	}
	w0 := binary.LittleEndian.Uint16(got[2:])
	w1 := binary.LittleEndian.Uint16(got[4:])
	if e8 := w0 >> 15; e8 != 0 {
		t.Errorf("E8 flag = %d, want 0", e8)
	}
	if typ := (w0 >> 12) & 0x7; typ != 3 {
		t.Errorf("block type = %d, want 3", typ)
	}
	blockSize := uint32(w0&0xFFF)<<12 | uint32(w1>>4)
	if blockSize != n {
		t.Errorf("block size = %#x, want %#x", blockSize, n)
	}
	if pad := w1 & 0xF; pad != 0 {
		t.Errorf("padding bits = %d, want 0", pad)
	}
}
