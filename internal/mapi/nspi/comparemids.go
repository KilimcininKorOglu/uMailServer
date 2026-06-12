package nspi

import (
	"net/http"

	"github.com/umailserver/umailserver/internal/mapi/wire"
)

// handleCompareMinIds compares the table positions of two minimal ids in the GAL
// (MS-OXNSPI 2.2.7 CompareMinIds): it reports a negative value when the second
// id precedes the first, a positive value when it follows, and zero when they
// are equal. An id that is not in the table yields an error.
func (s *Server) handleCompareMinIds(w http.ResponseWriter, r *http.Request, body []byte) {
	email := emailFromContext(r.Context())
	p := wire.NewPull(body, 0)
	_ = p.Uint32() // reserved
	if p.Uint8() != 0 {
		PullStat(p)
	}
	mid1 := p.Uint32()
	mid2 := p.Uint32()
	readAuxIn(p)
	if p.Err() != nil || s.dir == nil || email == "" {
		s.writeResponse(w, r, "CompareMinIds", "", compareMidsResult(ecError, 0))
		return
	}

	gal := s.gal()
	idx1 := midIndex(mid1, len(gal))
	idx2 := midIndex(mid2, len(gal))
	if idx1 < 0 || idx2 < 0 {
		s.writeResponse(w, r, "CompareMinIds", "", compareMidsResult(ecError, 0))
		return
	}
	var cmp int32
	switch {
	case idx2 < idx1:
		cmp = -1
	case idx2 > idx1:
		cmp = 1
	}
	s.writeResponse(w, r, "CompareMinIds", "", compareMidsResult(ecSuccess, cmp))
}

// compareMidsResult serializes a CompareMinIds response (MS-OXNSPI 2.2.7.2):
// status, the signed comparison, result, and the auxiliary-out length.
func compareMidsResult(result uint32, cmp int32) []byte {
	out := wire.NewPush(0)
	out.Uint32(0)           // status
	out.Uint32(uint32(cmp)) // signed comparison
	out.Uint32(result)
	out.Uint32(0) // cb_auxout
	return out.Bytes()
}
