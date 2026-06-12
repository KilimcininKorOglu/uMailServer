package oab

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestCompressHeaderAndBlock verifies the LZX header and a single block's
// framing for a small input (MS-OXOAB §2.11.1, §2.11.2).
func TestCompressHeaderAndBlock(t *testing.T) {
	raw := []byte("the offline address book payload")
	out := Compress(raw)

	if v := binary.LittleEndian.Uint32(out[0:]); v != 3 {
		t.Errorf("ulVersionHi = %d, want 3", v)
	}
	if v := binary.LittleEndian.Uint32(out[4:]); v != 1 {
		t.Errorf("ulVersionLo = %d, want 1", v)
	}
	if v := binary.LittleEndian.Uint32(out[8:]); v != blockMax {
		t.Errorf("ulBlockMax = %#x, want %#x", v, blockMax)
	}
	if v := binary.LittleEndian.Uint32(out[12:]); v != uint32(len(raw)) {
		t.Errorf("ulTargetSize = %d, want %d", v, len(raw))
	}

	// LZX block header at offset 16.
	if v := binary.LittleEndian.Uint32(out[16:]); v != 1 {
		t.Errorf("ulFlags = %d, want 1", v)
	}
	compSize := binary.LittleEndian.Uint32(out[20:])
	if v := binary.LittleEndian.Uint32(out[24:]); v != uint32(len(raw)) {
		t.Errorf("ulUncompSize = %d, want %d", v, len(raw))
	}
	// The OAB block CRC is the running CRC32: stdlib IEEE without the final XOR.
	if v := binary.LittleEndian.Uint32(out[28:]); v != crc32.ChecksumIEEE(raw)^0xFFFFFFFF {
		t.Errorf("ulCRC = %#x, want %#x", v, crc32.ChecksumIEEE(raw)^0xFFFFFFFF)
	}
	// The payload that follows is compSize bytes, and the whole file ends there
	// for a single-chunk input.
	if int(compSize) != len(out)-32 {
		t.Errorf("payload size = %d, want %d", compSize, len(out)-32)
	}
}

// TestCompressMultiChunk verifies inputs larger than one chunk are split into
// successive blocks whose uncompressed sizes tile the input.
func TestCompressMultiChunk(t *testing.T) {
	raw := bytes.Repeat([]byte{0x5A}, chunkSize+5000)
	out := Compress(raw)

	// Walk the blocks after the 16-byte header and sum the uncompressed sizes.
	off := 16
	var total int
	blocks := 0
	for off < len(out) {
		compSize := binary.LittleEndian.Uint32(out[off+4:])
		uncompSize := binary.LittleEndian.Uint32(out[off+8:])
		total += int(uncompSize)
		off += 16 + int(compSize)
		blocks++
	}
	if blocks != 2 {
		t.Errorf("block count = %d, want 2", blocks)
	}
	if total != len(raw) {
		t.Errorf("decoded size = %d, want %d", total, len(raw))
	}
	if off != len(out) {
		t.Errorf("walk ended at %d, want %d", off, len(out))
	}
}

// TestCompressRoundTrip feeds a real compressed OAB to an independent
// decompressor and checks the bytes come back unchanged. It runs only when
// OAB_DECOMPRESSOR names an external tool that takes "<in.lzx> <out.bin>"
// (for example libmspack's OAB decompressor), so CI without that tool skips it.
func TestCompressRoundTrip(t *testing.T) {
	tool := os.Getenv("OAB_DECOMPRESSOR")
	if tool == "" {
		t.Skip("set OAB_DECOMPRESSOR to an external OAB decompressor to run this test")
	}

	records := []Record{
		{X500DN: "/o=org/ou=eag/cn=recipients/cn=alice", SMTP: "alice@x.test", DisplayName: "Alice Example", ObjectType: 6, DisplayType: 0},
		{X500DN: "/o=org/ou=eag/cn=recipients/cn=bob", SMTP: "bob@x.test", DisplayName: "Bob Builder", ObjectType: 6, DisplayType: 0},
	}
	raw := BuildFullDetails(records, 7, "11111111-2222-3333-4444-555555555555", "/")
	compressed := Compress(raw)

	dir := t.TempDir()
	inPath := filepath.Join(dir, "oab.lzx")
	outPath := filepath.Join(dir, "oab.bin")
	if err := os.WriteFile(inPath, compressed, 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(tool, inPath, outPath).CombinedOutput(); err != nil {
		t.Fatalf("decompressor failed: %v\n%s", err, out)
	}
	got, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, raw) {
		t.Errorf("round trip mismatch: decompressed %d bytes, original %d bytes", len(got), len(raw))
	}
}
