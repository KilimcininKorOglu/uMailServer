package nspi

import (
	"net/http"
	"sort"

	"github.com/umailserver/umailserver/internal/mapi/wire"
)

// handleResortRestriction re-sorts a client-supplied minimal-id list into the
// table's display-name order and returns it (MS-OXNSPI 2.2.14 ResortRestriction).
// The GAL is already display-name ordered, so a minimal id's table position is
// its sort position. The response carries no address-book flag.
func (s *Server) handleResortRestriction(w http.ResponseWriter, r *http.Request, body []byte) {
	email := emailFromContext(r.Context())
	p := wire.NewPull(body, 0)
	_ = p.Uint32() // reserved
	var stat Stat
	if p.Uint8() != 0 {
		stat = PullStat(p)
	}
	var inmids []uint32
	if p.Uint8() != 0 {
		inmids = pullU32Array(p)
	}
	readAuxIn(p)
	if p.Err() != nil || s.dir == nil || email == "" {
		s.writeResponse(w, r, "ResortRestriction", "", resortRestrictionResult(ecError, stat, nil))
		return
	}

	gal := s.gal()
	outmids := make([]uint32, 0, len(inmids))
	for _, mid := range inmids {
		if midIndex(mid, len(gal)) >= 0 {
			outmids = append(outmids, mid)
		}
	}
	sort.SliceStable(outmids, func(i, j int) bool {
		return midIndex(outmids[i], len(gal)) < midIndex(outmids[j], len(gal))
	})
	stat.TotalRec = uint32(len(outmids))
	stat.NumPos = 0
	stat.CurrentRec = midBeginningOfTable
	s.writeResponse(w, r, "ResortRestriction", "", resortRestrictionResult(ecSuccess, stat, outmids))
}

// resortRestrictionResult serializes a ResortRestriction response (MS-OXNSPI
// 2.2.14.2): status, result, the updated state block, then the sorted
// minimal-id array on success.
func resortRestrictionResult(result uint32, stat Stat, outmids []uint32) []byte {
	out := wire.NewPush(0)
	out.Uint32(0) // status
	out.Uint32(result)
	out.Uint8(0xFF)
	stat.Push(out)
	if result != ecSuccess {
		out.Uint8(0) // no minimal-id array
		out.Uint32(0)
		return out.Bytes()
	}
	out.Uint8(0xFF) // minimal-id array present
	pushU32Array(out, outmids)
	out.Uint32(0) // cb_auxout
	return out.Bytes()
}
