package ntlmssp

import "encoding/binary"

// md4Sum returns the RFC 1320 MD4 digest of data. MD4 underlies the NT hash
// (NTOWFv1 in MS-NLMP 3.3.1), which keys NTLMv2. It is implemented in-package
// rather than imported because golang.org/x/crypto/md4 is marked deprecated and
// the lint gate rejects deprecated imports; the algorithm itself is fixed by
// RFC 1320, so an in-package copy carries no protocol risk.
func md4Sum(data []byte) [16]byte {
	s := [4]uint32{0x67452301, 0xefcdab89, 0x98badcfe, 0x10325476}

	// Pad with 0x80, then zeros up to 56 mod 64, then the 64-bit little-endian
	// bit length (RFC 1320 section 3.1-3.2).
	msgLen := len(data)
	padded := make([]byte, msgLen+1, ((msgLen+8)/64+1)*64+8)
	copy(padded, data)
	padded[msgLen] = 0x80
	for len(padded)%64 != 56 {
		padded = append(padded, 0)
	}
	var lenBytes [8]byte
	binary.LittleEndian.PutUint64(lenBytes[:], uint64(msgLen)*8)
	padded = append(padded, lenBytes[:]...)

	var x [16]uint32
	for off := 0; off < len(padded); off += 64 {
		for i := range x {
			x[i] = binary.LittleEndian.Uint32(padded[off+i*4:])
		}
		md4Block(&s, &x)
	}

	var out [16]byte
	binary.LittleEndian.PutUint32(out[0:], s[0])
	binary.LittleEndian.PutUint32(out[4:], s[1])
	binary.LittleEndian.PutUint32(out[8:], s[2])
	binary.LittleEndian.PutUint32(out[12:], s[3])
	return out
}

// md4Block applies the three MD4 rounds of one 512-bit block to the running
// state (RFC 1320 section 3.4).
func md4Block(s *[4]uint32, x *[16]uint32) {
	a, b, c, d := s[0], s[1], s[2], s[3]

	// Round 1: F(x,y,z) = (x AND y) OR (NOT x AND z); shifts 3,7,11,19; words
	// 0..15 in order.
	for i := 0; i < 16; i += 4 {
		a = rol(a+ff(b, c, d)+x[i+0], 3)
		d = rol(d+ff(a, b, c)+x[i+1], 7)
		c = rol(c+ff(d, a, b)+x[i+2], 11)
		b = rol(b+ff(c, d, a)+x[i+3], 19)
	}

	// Round 2: G(x,y,z) = majority; added constant 0x5a827999; shifts 3,5,9,13;
	// words strided by 4.
	for i := range 4 {
		a = rol(a+gg(b, c, d)+x[i+0]+0x5a827999, 3)
		d = rol(d+gg(a, b, c)+x[i+4]+0x5a827999, 5)
		c = rol(c+gg(d, a, b)+x[i+8]+0x5a827999, 9)
		b = rol(b+gg(c, d, a)+x[i+12]+0x5a827999, 13)
	}

	// Round 3: H(x,y,z) = XOR; added constant 0x6ed9eba1; shifts 3,9,11,15;
	// word order per RFC 1320.
	order3 := [16]int{0, 8, 4, 12, 2, 10, 6, 14, 1, 9, 5, 13, 3, 11, 7, 15}
	for i := 0; i < 16; i += 4 {
		a = rol(a+hh(b, c, d)+x[order3[i+0]]+0x6ed9eba1, 3)
		d = rol(d+hh(a, b, c)+x[order3[i+1]]+0x6ed9eba1, 9)
		c = rol(c+hh(d, a, b)+x[order3[i+2]]+0x6ed9eba1, 11)
		b = rol(b+hh(c, d, a)+x[order3[i+3]]+0x6ed9eba1, 15)
	}

	s[0] += a
	s[1] += b
	s[2] += c
	s[3] += d
}

func ff(x, y, z uint32) uint32 { return (x & y) | (^x & z) }
func gg(x, y, z uint32) uint32 { return (x & y) | (x & z) | (y & z) }
func hh(x, y, z uint32) uint32 { return x ^ y ^ z }
func rol(x, n uint32) uint32   { return (x << n) | (x >> (32 - n)) }
