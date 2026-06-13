package wire

// GlobcntRange is an inclusive range of item ids / change numbers within a single
// replica, expressed as the full 64-bit values they derive from. Lo and Hi are
// inclusive; a single id is a range with Lo == Hi. Only the low 48 bits participate
// in the serialized GLOBCNT (MS-OXCFXICS 2.2.2.5).
type GlobcntRange struct {
	Lo uint64
	Hi uint64
}

// valueToGlobcnt converts a 64-bit id/change-number to its 6-byte GLOBCNT: the low
// 48 bits, most-significant byte first (MS-OXCDATA 2.2.1.3 GlobalCounter; the
// reference takes the low 6 bytes of the value's big-endian form).
func valueToGlobcnt(v uint64) [6]byte {
	return [6]byte{
		byte(v >> 40), byte(v >> 32), byte(v >> 24),
		byte(v >> 16), byte(v >> 8), byte(v),
	}
}

// SerializeGlobset encodes a list of GLOBCNT ranges as a GLOBSET (MS-OXCFXICS
// 2.2.2.6): a sequence of stack commands terminated by an end byte. The commands
// factor out the leading bytes common to the ranges:
//
//   - push   0x01..0x06 + N bytes — push N common-prefix bytes onto the stack
//   - range  0x52 ('R') + N low + N high bytes — a low..high suffix range
//   - pop    0x50 ('P') — pop the most recent push
//   - end    0x00 — terminate the GLOBSET
//
// A single id emits one push(6); a single range emits one range(6); multiple ranges
// share their common prefix via an outer push/pop. The ranges MUST be pre-sorted
// ascending and non-overlapping (the encoder relies on front.Lo and back.Hi
// bounding the set, matching the reference encoder byte-for-byte).
func SerializeGlobset(ranges []GlobcntRange) []byte {
	out := make([]byte, 0, 16)
	push := func(b []byte) { // b is exactly the bytes to push (1..6)
		out = append(out, byte(len(b)))
		out = append(out, b...)
	}
	if len(ranges) == 0 {
		return append(out, 0x00)
	}
	if len(ranges) == 1 {
		lo := valueToGlobcnt(ranges[0].Lo)
		if ranges[0].Hi == ranges[0].Lo {
			push(lo[:])
		} else {
			hi := valueToGlobcnt(ranges[0].Hi)
			out = append(out, 0x52)
			out = append(out, lo[:]...)
			out = append(out, hi[:]...)
		}
		return append(out, 0x00)
	}
	front := valueToGlobcnt(ranges[0].Lo)
	back := valueToGlobcnt(ranges[len(ranges)-1].Hi)
	stackLen := 0
	for stackLen < 6 && front[stackLen] == back[stackLen] {
		stackLen++
	}
	if stackLen != 0 {
		push(front[:stackLen])
	}
	for _, r := range ranges {
		lo := valueToGlobcnt(r.Lo)
		if r.Hi == r.Lo {
			push(lo[stackLen:])
			continue
		}
		hi := valueToGlobcnt(r.Hi)
		i := stackLen
		for i < 6 && lo[i] == hi[i] {
			i++
		}
		if stackLen != i {
			push(lo[stackLen:i])
		}
		out = append(out, 0x52)
		out = append(out, lo[i:]...)
		out = append(out, hi[i:]...)
		if stackLen != i {
			out = append(out, 0x50) // pop the per-range prefix
		}
	}
	if stackLen != 0 {
		out = append(out, 0x50) // pop the shared prefix
	}
	return append(out, 0x00)
}
