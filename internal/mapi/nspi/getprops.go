package nspi

import (
	"net/http"

	"github.com/umailserver/umailserver/internal/mapi/wire"
)

// pushTaggedValuesL serializes a 32-bit-counted tagged-value array (LTPROPVAL_ARRAY,
// MS-OXNSPI), the long-count form NSPI uses for a single entry's properties.
func pushTaggedValuesL(p *wire.Push, vals []wire.TaggedPropertyValue) error {
	p.Uint32(uint32(len(vals)))
	for _, v := range vals {
		if err := v.Push(p); err != nil {
			return err
		}
	}
	return nil
}

// handleGetProps returns the requested properties of the address-book entry the
// state block's current record points at (MS-OXNSPI 2.2.8 GetProps). A requested
// property the entry lacks is returned as a PtypErrorCode value.
func (s *Server) handleGetProps(w http.ResponseWriter, r *http.Request, body []byte) {
	email := emailFromContext(r.Context())
	p := wire.NewPull(body, wire.FlagABK|wire.FlagUTF16)
	_ = p.Uint32() // flags
	var stat Stat
	if p.Uint8() != 0 {
		stat = PullStat(p)
	}
	var tags []wire.PropTag
	if p.Uint8() != 0 {
		tags = pullProptags(p)
	}
	readAuxIn(p)
	if len(tags) == 0 {
		tags = defaultColumns
	}
	if p.Err() != nil || s.dir == nil || email == "" {
		s.writeResponse(w, r, "GetProps", "", getPropsResult(ecError, stat.CodePage, nil))
		return
	}
	gal := s.dir.ResolveGAL("")
	idx := midIndex(stat.CurrentRec, len(gal))
	if idx < 0 {
		s.writeResponse(w, r, "GetProps", "", getPropsResult(ecNotFound, stat.CodePage, nil))
		return
	}
	entry := gal[idx]
	vals := make([]wire.TaggedPropertyValue, len(tags))
	for i, t := range tags {
		if v, ok := entryProperty(t, entry); ok {
			vals[i] = wire.TaggedPropertyValue{Tag: t, Value: v}
		} else {
			vals[i] = wire.TaggedPropertyValue{Tag: wire.MakeTag(t.ID(), wire.PtError), Value: ecNotFound}
		}
	}
	s.writeResponse(w, r, "GetProps", "", getPropsResult(ecSuccess, stat.CodePage, vals))
}

// getPropsResult serializes a GetProps response (MS-OXNSPI 2.2.8.2): status,
// result, code page, then the property row on success.
func getPropsResult(result, codepage uint32, vals []wire.TaggedPropertyValue) []byte {
	out := wire.NewPush(wire.FlagABK | wire.FlagUTF16)
	out.Uint32(0) // status
	out.Uint32(result)
	out.Uint32(codepage)
	if result != ecSuccess || vals == nil {
		out.Uint8(0) // no row
		out.Uint32(0)
		return out.Bytes()
	}
	out.Uint8(0xFF) // row present
	if err := pushTaggedValuesL(out, vals); err != nil {
		fail := wire.NewPush(wire.FlagABK | wire.FlagUTF16)
		fail.Uint32(0)
		fail.Uint32(ecError)
		fail.Uint32(codepage)
		fail.Uint8(0)
		fail.Uint32(0)
		return fail.Bytes()
	}
	out.Uint32(0) // cb_auxout
	return out.Bytes()
}
