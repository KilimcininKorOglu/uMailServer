package tnef

import (
	"encoding/binary"
	"fmt"
)

// Compressed-RTF magic values (the uint32 at header offset 8), per MS-OXRTFCP:
// RTF_COMPRESSED carries an LZFu stream, RTF_UNCOMPRESSED carries raw RTF after
// the 16-byte header.
const (
	rtfCompressed   = 0x75465a4c // "LZFu"
	rtfUncompressed = 0x414c454d // "MELA"
)

const (
	rtfDictLength  = 0x1000 // dictionary ring buffer size
	rtfHeaderLen   = 0x10   // compressed-RTF header length
	rtfInitDictLen = 207    // preloaded dictionary length
)

// rtfInitDict is the preset dictionary every compressed-RTF stream starts with
// (the MS-OXRTFCP preset dictionary). Its length must be rtfInitDictLen (asserted in tests).
const rtfInitDict = "{\\rtf1\\ansi\\mac\\deff0\\deftab720{\\fonttbl;}" +
	"{\\f0\\fnil \\froman \\fswiss \\fmodern \\fscrip" +
	"t \\fdecor MS Sans SerifSymbolArialTimes Ne" +
	"w RomanCourier{\\colortbl\\red0\\green0\\blue0" +
	"\r\n\\par \\pard\\plain\\f0\\fs20\\b\\i\\u\\tab" +
	"\\tx"

// decompressRTF expands a PR_RTF_COMPRESSED value into raw RTF bytes. It handles
// both the LZFu-compressed and the verbatim ("MELA") forms, per the MS-OXRTFCP
// decompression. The 16-byte header is: compressed-size, raw-size, magic, CRC
// (all little-endian uint32); the size field must equal len(in)-4.
func decompressRTF(in []byte) ([]byte, error) {
	if len(in) < rtfHeaderLen {
		return nil, fmt.Errorf("tnef: compressed RTF shorter than header")
	}
	size := binary.LittleEndian.Uint32(in[0:])
	rawsize := binary.LittleEndian.Uint32(in[4:])
	magic := binary.LittleEndian.Uint32(in[8:])
	// in[12:] is the CRC, not validated here.
	if int(size) != len(in)-4 {
		return nil, fmt.Errorf("tnef: compressed RTF size %d != %d", size, len(in)-4)
	}
	switch magic {
	case rtfUncompressed:
		return append([]byte(nil), in[rtfHeaderLen:]...), nil
	case rtfCompressed:
		// fall through to the LZFu loop below
	default:
		return nil, fmt.Errorf("tnef: unknown compressed-RTF magic 0x%08x", magic)
	}

	var dict [rtfDictLength]byte
	copy(dict[:], rtfInitDict)
	writeOff := rtfInitDictLen

	// Cap output to guard against a malformed stream that never hits the
	// termination sentinel.
	maxOut := int(rawsize) + rtfHeaderLen + 4
	out := make([]byte, 0, rawsize)

	pos := rtfHeaderLen
	for pos+1 < len(in) {
		control := in[pos]
		pos++
		for bit := 0; bit < 8; bit++ {
			if control&(1<<bit) != 0 {
				if pos+1 >= len(in) {
					return out, nil
				}
				high := uint16(in[pos])
				low := uint16(in[pos+1])
				pos += 2
				length := int(low&0x0F) + 2
				offset := int((((high << 8) + low) & 0xFFF0) >> 4)
				if offset == writeOff {
					return out, nil // termination sentinel
				}
				for i := 0; i < length; i++ {
					if len(out) >= maxOut {
						return nil, fmt.Errorf("tnef: compressed RTF overflow")
					}
					c := dict[(offset+i)%rtfDictLength]
					out = append(out, c)
					dict[writeOff] = c
					writeOff = (writeOff + 1) % rtfDictLength
				}
			} else {
				if pos >= len(in) {
					return out, nil
				}
				if len(out) >= maxOut {
					return nil, fmt.Errorf("tnef: compressed RTF overflow")
				}
				c := in[pos]
				pos++
				out = append(out, c)
				dict[writeOff] = c
				writeOff = (writeOff + 1) % rtfDictLength
			}
		}
	}
	return out, nil
}
