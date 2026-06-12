package oab

import (
	"encoding/binary"

	"github.com/umailserver/umailserver/internal/mapi/lzx"
)

// LZX wrapper parameters (MS-OXOAB §2.11.1).
const (
	// blockMax is ulBlockMax: the decompression window size advertised in the
	// LZX header.
	blockMax = 0x40000
	// chunkSize is the data covered by one LZX block, matching the LZXD chunk
	// size so each block holds exactly one chunk.
	chunkSize = lzx.MaxChunk
)

// Compress wraps an uncompressed OAB binary file (the output of
// BuildFullDetails or BuildTemplate) in the MS-OXOAB §2.11 compressed form: a
// 16-byte LZX header followed by one LZX block per chunkSize bytes of input.
// Each block records the chunk's uncompressed size and running CRC32 and
// carries an LZXD-framed payload, which Outlook decodes verbatim.
func Compress(raw []byte) []byte {
	var out []byte
	put := func(v uint32) { out = binary.LittleEndian.AppendUint32(out, v) }

	// LZX header (§2.11.1).
	put(3)                // ulVersionHi
	put(1)                // ulVersionLo
	put(blockMax)         // ulBlockMax
	put(uint32(len(raw))) // ulTargetSize

	if len(raw) == 0 {
		// A stored, empty block (no data to copy).
		put(0)             // ulFlags: not compressed
		put(0)             // ulCompSize
		put(0)             // ulUncompSize
		put(crc32OAB(nil)) // ulCRC
		return out
	}

	for pos := 0; pos < len(raw); {
		end := min(pos+chunkSize, len(raw))
		chunk := raw[pos:end]
		blk := lzx.EncodeUncompressed(chunk)

		// LZX block header (§2.11.2).
		put(1)                  // ulFlags: LZXD payload
		put(uint32(len(blk)))   // ulCompSize: bytes of LZXD payload
		put(uint32(len(chunk))) // ulUncompSize: bytes the block decodes to
		put(crc32OAB(chunk))    // ulCRC: running CRC32 of the decoded data
		out = append(out, blk...)
		pos = end
	}
	return out
}
