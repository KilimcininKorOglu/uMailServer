// Package lzxpress implements the "Plain LZ77" variant of the Xpress
// compression algorithm (MS-XCA sections 2.3 and 2.4). This is the compression
// the MAPI/HTTP mailbox connector applies to a ROP buffer when the
// RPC_HEADER_EXT carries the compressed flag (MS-OXCRPC / MS-OXCMAPIHTTP).
//
// The format is byte-oriented LZ77 with a 32-bit indicator word every 32
// tokens: each indicator bit (taken most-significant first) marks the next
// token as a literal byte (0) or a back-reference match (1). Match distances
// are limited to 8192 bytes and lengths use a variable, nibble-sharing
// extension scheme.
//
// The implementation is a from-scratch Go port of the documented algorithm; it
// is verified by round-trip and by the MS-XCA worked example, not against a
// live Windows client.
package lzxpress

import (
	"encoding/binary"
	"errors"
)

// ErrCorrupt indicates the compressed input is malformed or claims more output
// than the caller allotted.
var ErrCorrupt = errors.New("lzxpress: corrupt compressed data")

const (
	hashBits       = 12
	hashSize       = 1 << hashBits
	hashMask       = hashSize - 1
	searchAttempts = 5
	maxDistance    = 8192 // matches may reference at most this many bytes back
)

// threeByteHash hashes three bytes into a 12-bit slot (MS-XCA plain LZ77 match
// finder). The arithmetic is intentionally 16-bit and wraps modulo 65536.
func threeByteHash(b []byte) uint16 {
	a := uint16(b[0])
	bb := uint16(b[1]) ^ 0x2e
	c := uint16(b[2]) ^ 0x55
	ca := c - a
	d := ((a + bb) << 8) ^ (ca << 5) ^ (c + bb) ^ (0xcab + a)
	return d & hashMask
}

// storeMatch records the position of the three bytes hashing to h, probing a
// few adjacent slots and, if full, evicting the most distant entry.
func storeMatch(table *[hashSize]uint32, h uint16, offset uint32) {
	o := table[h]
	if o >= offset {
		table[h] = offset
		return
	}
	for i := uint16(1); i < searchAttempts; i++ {
		h2 := (h + i) & hashMask
		if table[h2] >= offset {
			table[h2] = offset
			return
		}
	}
	worstH := h
	worstScore := offset - o
	for i := uint16(1); i < searchAttempts; i++ {
		h2 := (h + i) & hashMask
		score := offset - table[h2]
		if score > worstScore {
			worstScore = score
			worstH = h2
		}
	}
	table[worstH] = offset
}

// lookupMatch returns the start index and length of the best match for the
// three bytes at data[offset], or (-1, 0) when none is usable.
func lookupMatch(table *[hashSize]uint32, h uint16, data []byte, offset uint32, maxLen int) (there, length int) {
	bestThere := -1
	bestLen := 0
	for i := range uint16(searchAttempts) {
		h2 := (h + i) & hashMask
		o := table[h2]
		if o >= offset {
			break
		}
		if offset-o > maxDistance {
			continue
		}
		cand := int(o)
		here := int(offset)
		if bestLen > 1000 && data[cand+bestLen-1] != data[bestThere+bestLen-1] {
			continue
		}
		l := 0
		for l < maxLen && data[here+l] == data[cand+l] {
			l++
		}
		if l > 2 && l > bestLen {
			bestLen = l
			bestThere = cand
		}
	}
	return bestThere, bestLen
}

// Compress encodes src with Plain LZ77. It always succeeds (the output grows as
// needed); an empty input yields nil.
func Compress(src []byte) []byte {
	if len(src) == 0 {
		return nil
	}
	var table [hashSize]uint32
	for i := range table {
		table[i] = 0xFFFFFFFF
	}

	out := make([]byte, 0, len(src)/2+8)
	var indic uint32
	var indicBit uint32
	indicPos := 0
	out = append(out, 0, 0, 0, 0) // first indicator placeholder
	nibbleIndex := -1             // -1 means no pending shared nibble

	pushBit := func(bit uint32) {
		indic = (indic << 1) | bit
		indicBit++
		if indicBit == 32 {
			binary.LittleEndian.PutUint32(out[indicPos:], indic)
			indicBit = 0
			indicPos = len(out)
			out = append(out, 0, 0, 0, 0)
		}
	}

	pos := 0
	for pos < len(src) {
		maxLen := min(len(src)-pos, 0xFFFF+3)
		matchThere, matchLen := -1, 0
		if maxLen >= 3 {
			h := threeByteHash(src[pos:])
			matchThere, matchLen = lookupMatch(&table, h, src, uint32(pos), maxLen)
			storeMatch(&table, h, uint32(pos))
		}
		if matchThere < 0 {
			out = append(out, src[pos])
			pos++
			pushBit(0)
			continue
		}

		enc := matchLen - 3
		bestOffset := pos - matchThere - 1
		meta := uint16((bestOffset << 3) | min(enc, 7))
		out = binary.LittleEndian.AppendUint16(out, meta)
		if enc >= 7 {
			ml := enc - 7
			if nibbleIndex < 0 {
				nibbleIndex = len(out)
				out = append(out, byte(min(ml, 15)))
			} else {
				out[nibbleIndex] |= byte(min(ml, 15)) << 4
				nibbleIndex = -1
			}
			if ml >= 15 {
				ml2 := ml - 15
				out = append(out, byte(min(ml2, 255)))
				if ml2 >= 255 {
					full := enc // re-add: equals match length - 3
					if full < 1<<16 {
						out = binary.LittleEndian.AppendUint16(out, uint16(full))
					} else {
						out = binary.LittleEndian.AppendUint16(out, 0)
						out = binary.LittleEndian.AppendUint32(out, uint32(full))
					}
				}
			}
		}
		pos += matchLen
		pushBit(1)
	}

	if indicBit != 0 {
		indic <<= 32 - indicBit
	}
	indic |= 0xFFFFFFFF >> indicBit
	binary.LittleEndian.PutUint32(out[indicPos:], indic)
	return out
}

// Decompress decodes Plain LZ77 input into at most maxOut bytes, returning
// ErrCorrupt on malformed data or output that would exceed maxOut.
func Decompress(input []byte, maxOut int) ([]byte, error) {
	if len(input) == 0 {
		return nil, nil
	}
	out := make([]byte, 0, maxOut)
	in := 0
	var indicator uint32
	indicatorBit := 0
	nibbleIndex := -1

	for {
		if indicatorBit == 0 {
			if in+4 > len(input) {
				return nil, ErrCorrupt
			}
			indicator = binary.LittleEndian.Uint32(input[in:])
			in += 4
			if in == len(input) {
				break // trailing indicator for data that does not exist
			}
			indicatorBit = 32
		}
		indicatorBit--

		if (indicator>>uint(indicatorBit))&1 == 0 {
			if in >= len(input) || len(out) >= maxOut {
				return nil, ErrCorrupt
			}
			out = append(out, input[in])
			in++
		} else {
			if in+2 > len(input) {
				return nil, ErrCorrupt
			}
			meta := int(binary.LittleEndian.Uint16(input[in:]))
			in += 2
			offset := (meta >> 3) + 1
			length := meta & 7
			if length == 7 {
				if nibbleIndex < 0 {
					if in >= len(input) {
						return nil, ErrCorrupt
					}
					nibbleIndex = in
					length = int(input[in] & 0xf)
					in++
				} else {
					length = int(input[nibbleIndex] >> 4)
					nibbleIndex = -1
				}
				if length == 15 {
					if in >= len(input) {
						return nil, ErrCorrupt
					}
					length = int(input[in])
					in++
					if length == 255 {
						if in+2 > len(input) {
							return nil, ErrCorrupt
						}
						length = int(binary.LittleEndian.Uint16(input[in:]))
						in += 2
						if length == 0 {
							if in+4 > len(input) {
								return nil, ErrCorrupt
							}
							length = int(binary.LittleEndian.Uint32(input[in:]))
							in += 4
						}
						if length < 15+7 {
							return nil, ErrCorrupt
						}
						length -= 15 + 7
					}
					length += 15
				}
				length += 7
			}
			length += 3

			for ; length > 0; length-- {
				if offset > len(out) || len(out) >= maxOut {
					return nil, ErrCorrupt
				}
				out = append(out, out[len(out)-offset])
			}
		}

		if len(out) >= maxOut || in >= len(input) {
			break
		}
	}
	return out, nil
}
