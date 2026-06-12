package nspi

import (
	"net/http"

	"github.com/umailserver/umailserver/internal/mapi/wire"
)

// PidTagContainerFlags values (MS-OXNSPI 2.2.4): an address-book container that
// holds recipients and is read-only. AB_SUBCONTAINERS is omitted because the GAL
// is a single flat container with no child containers.
const (
	abRecipients   uint32 = 0x1
	abUnmodifiable uint32 = 0x8
)

// nspiAddressCreationTemplates is the GetSpecialTable flag that requests the
// creation-templates table rather than the address-book hierarchy.
const nspiAddressCreationTemplates uint32 = 0x2

// specialTableVersion is the address-book hierarchy version reported to clients.
// The hierarchy is a single static GAL container, so the version never changes.
const specialTableVersion uint32 = 1

// galContainerRow describes the single Global Address List container the special
// table advertises: its permanent entry id (DT_CONTAINER form), container flags,
// depth, container id, display name, and master flag (MS-OXNSPI 2.2.4 /
// nsp_interface special-table row).
func galContainerRow() []wire.TaggedPropertyValue {
	entryID := wire.PermanentEntryID{DisplayType: dtContainer, X500DN: "/"}.Bytes()
	return []wire.TaggedPropertyValue{
		{Tag: wire.PidTagEntryID, Value: entryID},
		{Tag: wire.PidTagContainerFlags, Value: abRecipients | abUnmodifiable},
		{Tag: wire.PidTagDepth, Value: uint32(0)},
		{Tag: wire.PidTagAddressBookContainerID, Value: uint32(0)},
		{Tag: wire.PidTagDisplayName, Value: "Global Address List"},
		{Tag: wire.PidTagAddressBookIsMaster, Value: false},
	}
}

// handleGetSpecialTable returns the address-book container hierarchy: a single
// Global Address List container (MS-OXNSPI 2.2.4 GetSpecialTable). The
// creation-templates table is not served and yields an empty success.
func (s *Server) handleGetSpecialTable(w http.ResponseWriter, r *http.Request, body []byte) {
	p := wire.NewPull(body, wire.FlagABK|wire.FlagUTF16)
	flags := p.Uint32()
	var stat Stat
	if p.Uint8() != 0 {
		stat = PullStat(p)
	}
	if p.Uint8() != 0 {
		_ = p.Uint32() // the client's cached hierarchy version
	}
	readAuxIn(p)
	if p.Err() != nil {
		s.writeResponse(w, r, "GetSpecialTable", "", getSpecialTableResult(ecError, stat.CodePage, nil))
		return
	}
	if flags&nspiAddressCreationTemplates != 0 {
		s.writeResponse(w, r, "GetSpecialTable", "", getSpecialTableResult(ecSuccess, stat.CodePage, nil))
		return
	}
	rows := [][]wire.TaggedPropertyValue{galContainerRow()}
	s.writeResponse(w, r, "GetSpecialTable", "", getSpecialTableResult(ecSuccess, stat.CodePage, rows))
}

// getSpecialTableResult serializes a GetSpecialTable response (MS-OXNSPI
// 2.2.4.2): status, result, code page, the hierarchy version, then each
// container as a 32-bit-counted tagged-value array on success.
func getSpecialTableResult(result, codepage uint32, rows [][]wire.TaggedPropertyValue) []byte {
	out := wire.NewPush(wire.FlagABK | wire.FlagUTF16)
	out.Uint32(0) // status
	out.Uint32(result)
	out.Uint32(codepage)
	out.Uint8(0xFF) // version present
	out.Uint32(specialTableVersion)
	if result != ecSuccess || len(rows) == 0 {
		out.Uint8(0) // no rows
		out.Uint32(0)
		return out.Bytes()
	}
	out.Uint8(0xFF) // rows present
	out.Uint32(uint32(len(rows)))
	for _, row := range rows {
		if err := pushTaggedValuesL(out, row); err != nil {
			// A container value the server controls failed to serialize; report a
			// server error with no rows rather than a truncated table.
			return getSpecialTableResult(ecError, codepage, nil)
		}
	}
	out.Uint32(0) // cb_auxout
	return out.Bytes()
}
