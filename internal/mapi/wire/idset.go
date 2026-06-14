package wire

import "fmt"

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

// SerializeXID encodes an external identifier (MS-OXCDATA 2.2.2.2 XID) as a 22-byte
// value: the replica GUID (16 bytes, standard GUID wire layout) followed by a 6-byte
// GLOBCNT (the low 48 bits of globcntValue, big-endian MSB-first). This is the form
// PidTagSourceKey and PidTagChangeKey carry; the GUID identifies the replica and the
// GLOBCNT is the item id (source key) or change number (change key) within it.
func SerializeXID(replicaGUID GUID, globcntValue uint64) []byte {
	p := NewPush(0)
	p.GUID(replicaGUID)
	gc := valueToGlobcnt(globcntValue)
	p.Raw(gc[:])
	return p.Bytes()
}

// globcntToValue inverts valueToGlobcnt: a 6-byte big-endian GLOBCNT back to the
// 48-bit value it encodes.
func globcntToValue(cb [6]byte) uint64 {
	return uint64(cb[0])<<40 | uint64(cb[1])<<32 | uint64(cb[2])<<24 |
		uint64(cb[3])<<16 | uint64(cb[4])<<8 | uint64(cb[5])
}

// ParseXID decodes a 22-byte XID (MS-OXCDATA 2.2.2.2), inverting SerializeXID: the
// replica GUID (16 bytes, standard GUID wire layout) followed by the 6-byte big-endian
// GLOBCNT. It returns the replica GUID and the 48-bit GLOBCNT value, so a caller can
// decide whether an imported source/change key belongs to this store and recover the
// id within it.
func ParseXID(b []byte) (GUID, uint64, error) {
	if len(b) != 22 {
		return GUID{}, 0, fmt.Errorf("xid: length %d, want 22", len(b))
	}
	p := NewPull(b, 0)
	g := p.GUID()
	var gc [6]byte
	copy(gc[:], b[16:22])
	return g, globcntToValue(gc), nil
}

// ParseGlobset decodes a GLOBSET (MS-OXCFXICS 2.2.2.6) back into its ranges,
// inverting SerializeGlobset. It walks the stack commands — push (0x01-0x06), range
// (0x52), pop (0x50), the bitmask form (0x42, which the encoder never emits but a
// conformant reader must accept), and end (0x00) — reconstructing the common-prefix
// stack to recover each full GLOBCNT. It returns an error on a truncated or
// malformed set, or one with no terminating end command.
func ParseGlobset(data []byte) ([]GlobcntRange, error) {
	var ranges []GlobcntRange
	var stack [][]byte // groups of pushed common-prefix bytes
	commonBytes := func() ([6]byte, int) {
		var cb [6]byte
		n := 0
		for _, g := range stack {
			for _, b := range g {
				if n < 6 {
					cb[n] = b
				}
				n++
			}
		}
		return cb, n
	}
	off := 0
	for off < len(data) {
		cmd := data[off]
		off++
		switch {
		case cmd == 0x00: // end
			return ranges, nil
		case cmd >= 0x01 && cmd <= 0x06: // push N common-prefix bytes
			n := int(cmd)
			if off+n > len(data) {
				return nil, fmt.Errorf("globset: truncated push of %d bytes", n)
			}
			stack = append(stack, append([]byte(nil), data[off:off+n]...))
			off += n
			cb, length := commonBytes()
			if length > 6 {
				return nil, fmt.Errorf("globset: common prefix overflow (%d)", length)
			}
			if length == 6 { // a complete id; emit it and implicitly pop (MS-OXCFXICS 3.1.5.4.3.1.1)
				x := globcntToValue(cb)
				ranges = append(ranges, GlobcntRange{Lo: x, Hi: x})
				stack = stack[:len(stack)-1]
			}
		case cmd == 0x42: // bitmask
			if off+2 > len(data) {
				return nil, fmt.Errorf("globset: truncated bitmask command")
			}
			startValue, bitmask := data[off], data[off+1]
			off += 2
			cb, length := commonBytes()
			if length != 5 {
				return nil, fmt.Errorf("globset: bitmask common prefix = %d, want 5", length)
			}
			cb[5] = startValue
			low := globcntToValue(cb)
			lo, hi, have := low, low, true
			flush := func() {
				if have {
					ranges = append(ranges, GlobcntRange{Lo: lo, Hi: hi})
					have = false
				}
			}
			for i := range 8 {
				switch {
				case bitmask&(1<<i) == 0:
					flush()
				case !have:
					x := low + uint64(i) + 1
					lo, hi, have = x, x, true
				default:
					hi++
				}
			}
			flush()
		case cmd == 0x50: // pop
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		case cmd == 0x52: // range: a low..high suffix completing the common prefix
			cb, length := commonBytes()
			if length > 5 {
				return nil, fmt.Errorf("globset: range common prefix = %d, want <= 5", length)
			}
			rem := 6 - length
			if off+2*rem > len(data) {
				return nil, fmt.Errorf("globset: truncated range command")
			}
			copy(cb[length:], data[off:off+rem])
			off += rem
			low := globcntToValue(cb)
			copy(cb[length:], data[off:off+rem])
			off += rem
			ranges = append(ranges, GlobcntRange{Lo: low, Hi: globcntToValue(cb)})
		default:
			return nil, fmt.Errorf("globset: unknown command %#x", cmd)
		}
	}
	return nil, fmt.Errorf("globset: missing end command")
}

// ParseIDSET decodes a single-replica IDSET (a replica GUID followed by a GLOBSET),
// inverting SerializeIDSET, returning the replica GUID and the ranges. Only the first
// replica node is parsed (a single-store mailbox emits one).
func ParseIDSET(data []byte) (GUID, []GlobcntRange, error) {
	if len(data) < 16 {
		return GUID{}, nil, fmt.Errorf("idset: too short for a replica GUID")
	}
	p := NewPull(data, 0)
	g := p.GUID()
	ranges, err := ParseGlobset(data[16:])
	return g, ranges, err
}

// SerializeIDSET encodes a long-term-id IDSET for a single replica (MS-OXCFXICS
// 2.2.1.1 / 2.2.2.4): the replica GUID (16 bytes, standard GUID wire layout) followed
// by the GLOBSET of its ranges. The ICS state properties (PidTagIdsetGiven,
// MetaTagCnsetSeen) carry an IDSET in this form. A single-replica store emits one
// node; concatenate the results of multiple calls for multiple replicas.
func SerializeIDSET(replicaGUID GUID, ranges []GlobcntRange) []byte {
	p := NewPush(0)
	p.GUID(replicaGUID)
	p.Raw(SerializeGlobset(ranges))
	return p.Bytes()
}
