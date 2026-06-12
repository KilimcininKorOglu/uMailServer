package oab

// crc32Table is the reflected CRC-32 table for polynomial 0xEDB88320.
var crc32Table = func() [256]uint32 {
	var t [256]uint32
	for i := range t {
		c := uint32(i)
		for range 8 {
			if c&1 != 0 {
				c = (c >> 1) ^ 0xEDB88320
			} else {
				c >>= 1
			}
		}
		t[i] = c
	}
	return t
}()

// crc32OAB computes the running CRC-32 used by OAB LZX block headers. Unlike the
// standard CRC-32, the value is seeded with 0xFFFFFFFF and returned without the
// final XOR, because the OAB decompressor compares the running CRC directly
// against the stored value.
func crc32OAB(data []byte) uint32 {
	crc := uint32(0xFFFFFFFF)
	for _, b := range data {
		crc = (crc >> 8) ^ crc32Table[byte(crc)^b]
	}
	return crc
}
