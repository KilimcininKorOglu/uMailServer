package nspi

import (
	"net/http"
	"strings"

	"github.com/umailserver/umailserver/internal/mapi/wire"
)

// Restriction types (MS-OXCDATA 2.12, mapi_rtype).
const (
	resAnd      uint8 = 0x00
	resOr       uint8 = 0x01
	resNot      uint8 = 0x02
	resContent  uint8 = 0x03
	resProperty uint8 = 0x04
	resPropCmp  uint8 = 0x05
	resBitmask  uint8 = 0x06
	resSize     uint8 = 0x07
	resExist    uint8 = 0x08
	resSub      uint8 = 0x09
	resNull     uint8 = 0xFF
)

// Relational operators (MS-OXCDATA 2.12.6, relop).
const (
	relopLT uint8 = 0
	relopLE uint8 = 1
	relopGT uint8 = 2
	relopGE uint8 = 3
	relopEQ uint8 = 4
	relopNE uint8 = 5
)

// restriction is a parsed GetMatches filter (MS-OXCDATA 2.12). Only the fields
// the evaluator reads are retained; the parser still consumes every type's bytes
// so the request stream stays aligned.
type restriction struct {
	rt    uint8
	subs  []restriction // AND/OR operands, or the single NOT/SUB operand
	relop uint8         // PROPERTY
	tag   wire.PropTag  // PROPERTY/EXIST/CONTENT
	val   any           // PROPERTY/CONTENT value, nil when absent
}

// pullRestriction parses a restriction and its operands recursively. The request
// is read under the address-book flag, so a CONTENT/PROPERTY value carries an
// outer presence byte before the tagged value. An unknown type faults the pull
// so the caller rejects the request rather than reading a misaligned stream.
func pullRestriction(p *wire.Pull) restriction {
	r := restriction{rt: p.Uint8()}
	switch r.rt {
	case resAnd, resOr:
		n := int(p.Uint16())
		for range n {
			if p.Err() != nil {
				break
			}
			r.subs = append(r.subs, pullRestriction(p))
		}
	case resNot:
		r.subs = append(r.subs, pullRestriction(p))
	case resContent:
		_ = p.Uint32() // fuzzy level
		r.tag = wire.PropTag(p.Uint32())
		r.val = pullRestrictionValue(p)
	case resProperty:
		r.relop = p.Uint8()
		r.tag = wire.PropTag(p.Uint32())
		r.val = pullRestrictionValue(p)
	case resPropCmp:
		_ = p.Uint8()  // relop
		_ = p.Uint32() // proptag1
		_ = p.Uint32() // proptag2
	case resBitmask:
		_ = p.Uint8()  // bitmask relop
		_ = p.Uint32() // proptag
		_ = p.Uint32() // mask
	case resSize:
		_ = p.Uint8()  // relop
		_ = p.Uint32() // proptag
		_ = p.Uint32() // size
	case resExist:
		r.tag = wire.PropTag(p.Uint32())
	case resSub:
		_ = p.Uint32() // subobject
		r.subs = append(r.subs, pullRestriction(p))
	case resNull:
		// no body
	default:
		p.Fault()
	}
	return r
}

// pullRestrictionValue reads the tagged value of a CONTENT/PROPERTY restriction.
// Under the address-book flag an outer presence byte precedes the value.
func pullRestrictionValue(p *wire.Pull) any {
	if p.Flags()&wire.FlagABK != 0 && p.Uint8() == 0 {
		return nil // value absent
	}
	tpv, err := wire.PullTaggedPropertyValue(p)
	if err != nil {
		return nil
	}
	return tpv.Value
}

// matchEntry reports whether a GAL entry satisfies the restriction (the
// evaluation MS-OXNSPI 3.1.4.1.10 performs). CONTENT and the unevaluated types
// never match, matching the reference server.
func matchEntry(r restriction, e DirectoryEntry) bool {
	switch r.rt {
	case resAnd:
		for _, sub := range r.subs {
			if !matchEntry(sub, e) {
				return false
			}
		}
		return true
	case resOr:
		for _, sub := range r.subs {
			if matchEntry(sub, e) {
				return true
			}
		}
		return false
	case resNot:
		return len(r.subs) == 1 && !matchEntry(r.subs[0], e)
	case resProperty:
		return matchProperty(r, e)
	case resExist:
		_, ok := entryProperty(r.tag, e)
		return ok
	default:
		return false
	}
}

// matchProperty evaluates a property restriction: an ambiguous-name target is a
// case-insensitive substring of the display name or address; any other property
// is compared by the relational operator.
func matchProperty(r restriction, e DirectoryEntry) bool {
	if r.val == nil {
		return true
	}
	search, ok := r.val.(string)
	if !ok {
		return false
	}
	if r.tag.ID() == wire.PidTagAnr.ID() {
		needle := strings.ToLower(stripAddressType(search))
		return strings.Contains(strings.ToLower(e.DisplayName), needle) ||
			strings.Contains(strings.ToLower(e.Email), needle)
	}
	v, present := entryProperty(r.tag, e)
	if !present {
		return false
	}
	vs, vok := v.(string)
	if !vok {
		return false
	}
	return evalRelop(r.relop, strings.Compare(strings.ToLower(vs), strings.ToLower(search)))
}

// evalRelop applies a relational operator to a three-way comparison result.
func evalRelop(relop uint8, cmp int) bool {
	switch relop {
	case relopLT:
		return cmp < 0
	case relopLE:
		return cmp <= 0
	case relopGT:
		return cmp > 0
	case relopGE:
		return cmp >= 0
	case relopEQ:
		return cmp == 0
	case relopNE:
		return cmp != 0
	default:
		return false
	}
}

// handleGetMatches evaluates a restriction over the GAL and returns the matching
// entries' minimal ids and rows (MS-OXNSPI 2.2.3 GetMatches), capped at the
// requested row count.
func (s *Server) handleGetMatches(w http.ResponseWriter, r *http.Request, body []byte) {
	email := emailFromContext(r.Context())
	p := wire.NewPull(body, wire.FlagABK|wire.FlagUTF16)
	_ = p.Uint32() // reserved1
	var stat Stat
	if p.Uint8() != 0 {
		stat = PullStat(p)
	}
	if p.Uint8() != 0 {
		pullU32Array(p) // explicit minimal-id list (unused by the GAL search)
	}
	_ = p.Uint32() // reserved2
	var filter restriction
	hasFilter := p.Uint8() != 0
	if hasFilter {
		filter = pullRestriction(p)
	}
	if p.Uint8() != 0 {
		p.GUID()       // property-name guid
		_ = p.Uint32() // property-name id
	}
	requested := int(p.Uint32())
	var cols []wire.PropTag
	if p.Uint8() != 0 {
		cols = pullProptags(p)
	}
	readAuxIn(p)
	if len(cols) == 0 {
		cols = defaultColumns
	}
	if p.Err() != nil || s.dir == nil || email == "" {
		s.writeResponse(w, r, "GetMatches", "", getMatchesResult(ecError, stat, nil, nil, cols))
		return
	}

	gal := s.gal()
	stat.TotalRec = uint32(len(gal))
	var mids []uint32
	var rows []DirectoryEntry
	if hasFilter {
		start := int(stat.NumPos)
		if start < 0 || start > len(gal) {
			start = 0
		}
		for i := start; i < len(gal); i++ {
			if requested > 0 && len(mids) >= requested {
				break
			}
			if matchEntry(filter, gal[i]) {
				mids = append(mids, entryMid(i))
				rows = append(rows, gal[i])
			}
		}
	}
	s.writeResponse(w, r, "GetMatches", "", getMatchesResult(ecSuccess, stat, mids, rows, cols))
}

// getMatchesResult serializes a GetMatches response (MS-OXNSPI 2.2.3.2): status,
// result, the state block, then the matched minimal-id array and the rows as a
// COLROW on success.
func getMatchesResult(result uint32, stat Stat, mids []uint32, rows []DirectoryEntry, cols []wire.PropTag) []byte {
	out := wire.NewPush(wire.FlagABK | wire.FlagUTF16)
	out.Uint32(0) // status
	out.Uint32(result)
	out.Uint8(0xFF)
	stat.Push(out)
	if result != ecSuccess {
		out.Uint8(0) // no minimal-id array
		out.Uint8(0) // no COLROW
		out.Uint32(0)
		return out.Bytes()
	}
	out.Uint8(0xFF) // minimal-id array present
	pushU32Array(out, mids)
	out.Uint8(0xFF) // COLROW present
	if err := pushColRow(out, cols, rows); err != nil {
		return getMatchesResult(ecError, stat, nil, nil, cols)
	}
	out.Uint32(0) // cb_auxout
	return out.Bytes()
}
