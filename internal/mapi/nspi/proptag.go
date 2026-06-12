package nspi

import "github.com/umailserver/umailserver/internal/mapi/wire"

// NSPI proptag and id arrays use a 32-bit count throughout (the LPROPTAG_ARRAY /
// long-count form), distinct from the 16-bit count emsmdb tables use.

// pullU32Array reads a 32-bit-counted array of 32-bit values (a MinId array).
func pullU32Array(p *wire.Pull) []uint32 {
	n := int(p.Uint32())
	if n == 0 {
		return nil
	}
	out := make([]uint32, 0, n)
	for range n {
		if p.Err() != nil {
			break
		}
		out = append(out, p.Uint32())
	}
	return out
}

// pullProptags reads a 32-bit-counted property-tag array.
func pullProptags(p *wire.Pull) []wire.PropTag {
	raw := pullU32Array(p)
	tags := make([]wire.PropTag, len(raw))
	for i, v := range raw {
		tags[i] = wire.PropTag(v)
	}
	return tags
}

// pushProptags writes a 32-bit-counted property-tag array.
func pushProptags(p *wire.Push, tags []wire.PropTag) {
	p.Uint32(uint32(len(tags)))
	for _, t := range tags {
		p.Uint32(uint32(t))
	}
}

// pushU32Array writes a 32-bit-counted array of 32-bit values (a MinId array).
func pushU32Array(p *wire.Push, vals []uint32) {
	p.Uint32(uint32(len(vals)))
	for _, v := range vals {
		p.Uint32(v)
	}
}
