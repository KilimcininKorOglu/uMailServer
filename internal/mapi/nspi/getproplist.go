package nspi

import (
	"net/http"

	"github.com/umailserver/umailserver/internal/mapi/wire"
)

// availableEntryTags lists the property tags a GAL entry carries values for; it
// is the set GetPropList reports and the set entryProperty can serve.
var availableEntryTags = []wire.PropTag{
	wire.PidTagEntryID, wire.PidTagDisplayName, wire.PidTagAddressType,
	wire.PidTagEmailAddress, wire.PidTagSmtpAddress, wire.PidTagAccount,
	wire.PidTagObjectType, wire.PidTagDisplayType,
}

// handleGetPropList returns the property tags available on the entry named by
// the request's minimal id (MS-OXNSPI 2.2.8 GetPropList). An id that names no
// entry yields an error.
func (s *Server) handleGetPropList(w http.ResponseWriter, r *http.Request, body []byte) {
	email := emailFromContext(r.Context())
	p := wire.NewPull(body, wire.FlagABK|wire.FlagUTF16)
	_ = p.Uint32() // flags
	mid := p.Uint32()
	_ = p.Uint32() // code page
	readAuxIn(p)
	if p.Err() != nil || s.dir == nil || email == "" {
		s.writeResponse(w, r, "GetPropList", "", getPropListResult(ecError, nil))
		return
	}
	gal := s.gal()
	if midIndex(mid, len(gal)) < 0 {
		s.writeResponse(w, r, "GetPropList", "", getPropListResult(ecError, nil))
		return
	}
	s.writeResponse(w, r, "GetPropList", "", getPropListResult(ecSuccess, availableEntryTags))
}

// getPropListResult serializes a GetPropList response (MS-OXNSPI 2.2.8.2):
// status, result, then the property-tag array on success.
func getPropListResult(result uint32, tags []wire.PropTag) []byte {
	out := wire.NewPush(wire.FlagABK | wire.FlagUTF16)
	out.Uint32(0) // status
	out.Uint32(result)
	if result != ecSuccess {
		out.Uint8(0) // no tag array
		out.Uint32(0)
		return out.Bytes()
	}
	out.Uint8(0xFF) // tag array present
	pushProptags(out, tags)
	out.Uint32(0) // cb_auxout
	return out.Bytes()
}
