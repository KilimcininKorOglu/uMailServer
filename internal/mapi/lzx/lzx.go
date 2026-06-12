// Package lzx implements the LZXD compressed-block framing (MS-PATCH) used by
// the Offline Address Book.
//
// Only the uncompressed block type is implemented. Outlook always treats an OAB
// payload as LZXD regardless of the block flags, but an LZXD "uncompressed"
// (type 3) block carries its data verbatim, so a valid, Outlook-decodable
// stream needs no Huffman trees and no match finder. This keeps the encoder
// trivial and exact; the cost is that the OAB files are not actually smaller
// than their uncompressed form.
package lzx

import "encoding/binary"

// MaxChunk is the largest data block a single LZXD chunk may carry.
const MaxChunk = 32768

// EncodeUncompressed frames data as one LZXD uncompressed (type 3) chunk
// (MS-PATCH §2.2.3 / §2.3.1.1):
//
//   - a 16-bit little-endian chunk-size prefix (the byte count that follows);
//   - the bit fields E8 translation(=0), block type(=3), and the 24-bit block
//     size, packed most-significant-bit-first into two 16-bit little-endian
//     words, the second padded with four zero bits to a word boundary;
//   - the three recent-match-offset registers R0, R1, R2, each the 32-bit
//     little-endian value 1;
//   - the raw data bytes;
//   - a single pad byte when the data length is odd, restoring 16-bit
//     alignment.
//
// Each chunk is self-contained: a decoder resets its state per chunk, so the
// E8 header and offset registers are emitted every time. data must not exceed
// MaxChunk bytes.
func EncodeUncompressed(data []byte) []byte {
	n := uint32(len(data))
	padded := len(data) + (len(data) & 1)
	chunkPayload := 4 + 12 + padded // two header words + R0/R1/R2 + padded data

	out := make([]byte, 0, 2+chunkPayload)
	// 16-bit chunk-size prefix: the number of bytes following this field.
	out = binary.LittleEndian.AppendUint16(out, uint16(chunkPayload))
	// Word 0: E8(bit 15, 0) | block type(bits 14-12, 3) | block_size[23:12].
	w0 := uint16(3<<12) | uint16((n>>12)&0xFFF)
	// Word 1: block_size[11:0] (bits 15-4) | four padding bits.
	w1 := uint16((n & 0xFFF) << 4)
	out = binary.LittleEndian.AppendUint16(out, w0)
	out = binary.LittleEndian.AppendUint16(out, w1)
	// Recent match offsets, all 1 (MS-PATCH §2.3.1.1).
	out = binary.LittleEndian.AppendUint32(out, 1)
	out = binary.LittleEndian.AppendUint32(out, 1)
	out = binary.LittleEndian.AppendUint32(out, 1)
	// Raw data plus a pad byte to keep 16-bit alignment.
	out = append(out, data...)
	if len(data)&1 != 0 {
		out = append(out, 0)
	}
	return out
}
