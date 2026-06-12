package nspi

import (
	"net/http"

	"github.com/umailserver/umailserver/internal/mapi/wire"
)

// defaultColumns are the address-book columns returned when a request omits an
// explicit column set: enough for Outlook to display and resolve a GAL entry.
var defaultColumns = []wire.PropTag{
	wire.PidTagEntryID, wire.PidTagDisplayName, wire.PidTagAddressType,
	wire.PidTagEmailAddress, wire.PidTagSmtpAddress, wire.PidTagObjectType,
	wire.PidTagDisplayType,
}

// pushColRow serializes a COLROW (MS-OXNSPI 2.2.1.8): the column tags, a 32-bit
// row count, then each entry as an address-book property row over the columns,
// under the ABK flag so string and binary values carry the presence byte.
func pushColRow(p *wire.Push, cols []wire.PropTag, entries []DirectoryEntry) error {
	pushProptags(p, cols)
	p.Uint32(uint32(len(entries)))
	for _, e := range entries {
		if err := pushEntryRow(p, cols, e); err != nil {
			return err
		}
	}
	return nil
}

// pushEntryRow serializes one entry as a PropertyRow over the columns. A row whose
// every column is available uses the compact untagged form; a row with an
// unmapped column uses the flagged form, marking the missing column in error.
func pushEntryRow(p *wire.Push, cols []wire.PropTag, e DirectoryEntry) error {
	values := make([]any, len(cols))
	allPresent := true
	for i, col := range cols {
		v, ok := entryProperty(col, e)
		values[i] = v
		if !ok {
			allPresent = false
		}
	}
	if allPresent {
		return wire.PushPropertyRow(p, cols, wire.PropertyRow{Flag: wire.RowFlagNone, Values: values})
	}
	flagged := make([]any, len(cols))
	for i := range cols {
		if values[i] != nil {
			flagged[i] = wire.FlaggedPropertyValue{Flag: wire.FlaggedAvailable, Value: values[i]}
		} else {
			flagged[i] = wire.FlaggedPropertyValue{Flag: wire.FlaggedError, Value: ecNotFound}
		}
	}
	return wire.PushPropertyRow(p, cols, wire.PropertyRow{Flag: wire.RowFlagFlagged, Values: flagged})
}

// handleQueryRows returns address-book rows from the table cursor, or for an
// explicit minimal-id list, as a COLROW (MS-OXNSPI 2.2.4).
func (s *Server) handleQueryRows(w http.ResponseWriter, r *http.Request, body []byte) {
	email := emailFromContext(r.Context())
	p := wire.NewPull(body, wire.FlagABK|wire.FlagUTF16)
	_ = p.Uint32() // flags
	var stat Stat
	if p.Uint8() != 0 {
		stat = PullStat(p)
	}
	explicit := pullU32Array(p) // explicit minimal-id list, or empty for the cursor
	count := int(p.Uint32())
	var cols []wire.PropTag
	if p.Uint8() != 0 {
		cols = pullProptags(p)
	}
	readAuxIn(p)
	if len(cols) == 0 {
		cols = defaultColumns
	}
	if p.Err() != nil || s.dir == nil || email == "" {
		s.writeResponse(w, r, "QueryRows", "", queryRowsResult(ecError, stat, nil, cols))
		return
	}

	gal := s.gal()
	stat.TotalRec = uint32(len(gal))
	var rows []DirectoryEntry
	if len(explicit) > 0 {
		for _, mid := range explicit {
			if idx := midIndex(mid, len(gal)); idx >= 0 {
				rows = append(rows, gal[idx])
			}
		}
	} else {
		start := int(stat.NumPos)
		if start < 0 || start > len(gal) {
			start = len(gal)
		}
		end := min(start+count, len(gal))
		rows = gal[start:end]
		stat.NumPos = uint32(end)
		if end >= len(gal) {
			stat.CurrentRec = midEnd
		} else {
			stat.CurrentRec = entryMid(end)
		}
	}
	s.writeResponse(w, r, "QueryRows", "", queryRowsResult(ecSuccess, stat, rows, cols))
}

// queryRowsResult serializes a QueryRows response (MS-OXNSPI 2.2.4.2): status,
// result, the updated state block, then the COLROW on success.
func queryRowsResult(result uint32, stat Stat, rows []DirectoryEntry, cols []wire.PropTag) []byte {
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
		// Property serialization failed for a value the online path should serve;
		// report a server error with no rows rather than a truncated COLROW.
		fail := wire.NewPush(wire.FlagABK | wire.FlagUTF16)
		fail.Uint32(0)
		fail.Uint32(ecError)
		fail.Uint8(0xFF)
		stat.Push(fail)
		fail.Uint8(0)
		fail.Uint32(0)
		return fail.Bytes()
	}
	out.Uint32(0) // cb_auxout
	return out.Bytes()
}
