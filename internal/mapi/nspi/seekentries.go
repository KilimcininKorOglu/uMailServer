package nspi

import (
	"net/http"
	"strings"

	"github.com/umailserver/umailserver/internal/mapi/wire"
)

// sortTypeDisplayName is the only table sort order this address book supports
// (MS-OXNSPI 2.3.1.1 SortTypeDisplayName): entries ordered by display name.
const sortTypeDisplayName uint32 = 0

// handleSeekEntries positions the table cursor at the first entry whose display
// name is at or after the requested target and returns that row (MS-OXNSPI 2.2.12
// SeekEntries). The table is display-name sorted, so the seek is a lower bound.
func (s *Server) handleSeekEntries(w http.ResponseWriter, r *http.Request, body []byte) {
	email := emailFromContext(r.Context())
	p := wire.NewPull(body, wire.FlagABK|wire.FlagUTF16)
	_ = p.Uint32() // reserved
	var stat Stat
	if p.Uint8() != 0 {
		stat = PullStat(p)
	}
	var target wire.TaggedPropertyValue
	hasTarget := p.Uint8() != 0
	if hasTarget {
		var err error
		if target, err = wire.PullTaggedPropertyValue(p); err != nil && p.Err() == nil {
			hasTarget = false
		}
	}
	var explicit []uint32
	if p.Uint8() != 0 {
		explicit = pullU32Array(p)
	}
	var cols []wire.PropTag
	if p.Uint8() != 0 {
		cols = pullProptags(p)
	}
	readAuxIn(p)
	if len(cols) == 0 {
		cols = defaultColumns
	}

	targetName, ok := target.Value.(string)
	if p.Err() != nil || s.dir == nil || email == "" || !hasTarget || !ok ||
		stat.SortType != sortTypeDisplayName || target.Tag.ID() != wire.PidTagDisplayName.ID() {
		s.writeResponse(w, r, "SeekEntries", "", seekEntriesResult(ecError, stat, nil, cols))
		return
	}

	gal := s.gal()
	targetLower := strings.ToLower(targetName)
	seekPos := -1
	if len(explicit) > 0 {
		for _, mid := range explicit {
			if idx := midIndex(mid, len(gal)); idx >= 0 && strings.ToLower(gal[idx].DisplayName) >= targetLower {
				seekPos = idx
				break
			}
		}
	} else {
		for i := range gal {
			if strings.ToLower(gal[i].DisplayName) >= targetLower {
				seekPos = i
				break
			}
		}
	}
	if seekPos < 0 {
		s.writeResponse(w, r, "SeekEntries", "", seekEntriesResult(ecNotFound, stat, nil, cols))
		return
	}
	stat.NumPos = uint32(seekPos)
	stat.CurrentRec = entryMid(seekPos)
	stat.TotalRec = uint32(len(gal))
	s.writeResponse(w, r, "SeekEntries", "", seekEntriesResult(ecSuccess, stat, []DirectoryEntry{gal[seekPos]}, cols))
}

// seekEntriesResult serializes a SeekEntries response (MS-OXNSPI 2.2.12.2):
// status, result, the updated state block, then the positioned row as a COLROW.
func seekEntriesResult(result uint32, stat Stat, rows []DirectoryEntry, cols []wire.PropTag) []byte {
	out := wire.NewPush(wire.FlagABK | wire.FlagUTF16)
	out.Uint32(0) // status
	out.Uint32(result)
	out.Uint8(0xFF)
	stat.Push(out)
	if result != ecSuccess {
		out.Uint8(0) // no COLROW
		out.Uint32(0)
		return out.Bytes()
	}
	out.Uint8(0xFF) // COLROW present
	if err := pushColRow(out, cols, rows); err != nil {
		return seekEntriesResult(ecError, stat, nil, cols)
	}
	out.Uint32(0) // cb_auxout
	return out.Bytes()
}
