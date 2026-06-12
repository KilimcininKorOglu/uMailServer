package nspi

import (
	"net/http"

	"github.com/umailserver/umailserver/internal/mapi/wire"
)

// Table-positioning ids carried in the state block's current record (MS-OXNSPI
// 2.2.8, the OLE stream-seek origins). END_OF_TABLE is midEnd.
const (
	midBeginningOfTable uint32 = 0 // STREAM_SEEK_SET
	midCurrent          uint32 = 1 // STREAM_SEEK_CUR
)

// positionInList resolves the state block's current record to a row index in the
// GAL (MS-OXNSPI 3.1.4.5): the table start, a fractional position, the table
// end, or the position of a specific entry.
func positionInList(stat Stat, total uint32) uint32 {
	switch stat.CurrentRec {
	case midBeginningOfTable:
		return 0
	case midCurrent:
		if stat.TotalRec == 0 {
			return 0
		}
		pos := uint32(float64(total) * float64(stat.NumPos) / float64(stat.TotalRec))
		return min(pos, total)
	case midEnd:
		return total
	default:
		idx := midIndex(stat.CurrentRec, int(total))
		if idx < 0 {
			return 0
		}
		return uint32(idx)
	}
}

// handleUpdateStat moves the table cursor by the state block's requested delta
// and returns the updated state, optionally reporting the actual movement
// (MS-OXNSPI 2.2.13 UpdateStat).
func (s *Server) handleUpdateStat(w http.ResponseWriter, r *http.Request, body []byte) {
	email := emailFromContext(r.Context())
	p := wire.NewPull(body, 0)
	_ = p.Uint32() // reserved
	var stat Stat
	if p.Uint8() != 0 {
		stat = PullStat(p)
	}
	deltaRequested := p.Uint8() != 0
	readAuxIn(p)
	if p.Err() != nil || s.dir == nil || email == "" {
		s.writeResponse(w, r, "UpdateStat", "", updateStatResult(ecError, stat, false, 0))
		return
	}

	gal := s.gal()
	total := uint32(len(gal))
	initRow := positionInList(stat, total)
	row := initRow
	if stat.Delta < 0 && uint32(-stat.Delta) >= row {
		row = 0
	} else {
		row = uint32(int32(row) + stat.Delta)
	}
	if row >= total {
		row = total
		stat.CurrentRec = midEnd
	} else {
		stat.CurrentRec = entryMid(int(row))
	}
	delta := int32(row) - int32(initRow)
	stat.Delta = 0
	stat.NumPos = row
	stat.TotalRec = total
	s.writeResponse(w, r, "UpdateStat", "", updateStatResult(ecSuccess, stat, deltaRequested, delta))
}

// updateStatResult serializes an UpdateStat response (MS-OXNSPI 2.2.13.2):
// status, result, the updated state block, then the actual movement when the
// client requested it. The response carries no address-book flag.
func updateStatResult(result uint32, stat Stat, hasDelta bool, delta int32) []byte {
	out := wire.NewPush(0)
	out.Uint32(0) // status
	out.Uint32(result)
	out.Uint8(0xFF) // state block present
	stat.Push(out)
	if !hasDelta || result != ecSuccess {
		out.Uint8(0) // no delta
	} else {
		out.Uint8(0xFF)
		out.Uint32(uint32(delta))
	}
	out.Uint32(0) // cb_auxout
	return out.Bytes()
}
