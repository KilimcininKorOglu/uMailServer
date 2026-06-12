package nspi

import (
	"net/http"
	"strings"

	"github.com/umailserver/umailserver/internal/mapi/wire"
)

// Name-resolution status codes returned per requested name (MS-OXNSPI 2.2.6
// ResolveNamesW). They are not entry ids: a resolved name reports the RESOLVED
// marker rather than the matched entry's minimal id.
const (
	nameUnresolved uint32 = 0x00000000 // no entry matched
	nameAmbiguous  uint32 = 0x00000001 // more than one entry matched
	nameResolved   uint32 = 0x00000002 // exactly one entry matched
)

// stripAddressType drops a leading address-type prefix ("SMTP:user@host" ->
// "user@host"), mirroring the reference server's tokenization before resolution.
func stripAddressType(name string) string {
	if _, rest, ok := strings.Cut(name, ":"); ok {
		return rest
	}
	return name
}

// handleResolveNamesW resolves each requested display name against the GAL by
// ambiguous-name resolution (MS-OXNSPI 2.2.6): every name yields a status code,
// and each name that matches exactly one entry contributes a row to the COLROW.
func (s *Server) handleResolveNamesW(w http.ResponseWriter, r *http.Request, body []byte) {
	email := emailFromContext(r.Context())
	p := wire.NewPull(body, wire.FlagABK|wire.FlagUTF16)
	_ = p.Uint32() // reserved
	var stat Stat
	if p.Uint8() != 0 {
		stat = PullStat(p)
	}
	var cols []wire.PropTag
	if p.Uint8() != 0 {
		cols = pullProptags(p)
	}
	var names []string
	if p.Uint8() != 0 {
		// The name array is read with the address-book presence byte disabled
		// (MS-OXNSPI): a 32-bit count then each name as a bare wide string, with
		// no per-element presence marker.
		n := int(p.Uint32())
		names = make([]string, 0, n)
		for range n {
			if p.Err() != nil {
				break
			}
			names = append(names, p.WStr())
		}
	}
	readAuxIn(p)
	if len(cols) == 0 {
		cols = defaultColumns
	}
	if p.Err() != nil || s.dir == nil || email == "" {
		s.writeResponse(w, r, "ResolveNamesW", "", resolveNamesResult(ecError, stat.CodePage, nil, nil, cols))
		return
	}

	mids := make([]uint32, 0, len(names))
	var rows []DirectoryEntry
	for _, name := range names {
		key := strings.TrimSpace(stripAddressType(name))
		if key == "" {
			mids = append(mids, nameUnresolved)
			continue
		}
		matches := s.dir.ResolveGAL(key)
		switch len(matches) {
		case 0:
			mids = append(mids, nameUnresolved)
		case 1:
			mids = append(mids, nameResolved)
			rows = append(rows, matches[0])
		default:
			mids = append(mids, nameAmbiguous)
		}
	}
	s.writeResponse(w, r, "ResolveNamesW", "", resolveNamesResult(ecSuccess, stat.CodePage, mids, rows, cols))
}

// resolveNamesResult serializes a ResolveNamesW response (MS-OXNSPI 2.2.6.2):
// status, result, code page, then on success the per-name minimal-id array and
// the resolved rows as a COLROW.
func resolveNamesResult(result, codepage uint32, mids []uint32, rows []DirectoryEntry, cols []wire.PropTag) []byte {
	out := wire.NewPush(wire.FlagABK | wire.FlagUTF16)
	out.Uint32(0) // status
	out.Uint32(result)
	out.Uint32(codepage)
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
		// A resolved entry's value failed to serialize; report a server error
		// rather than a truncated COLROW.
		return resolveNamesResult(ecError, codepage, nil, nil, cols)
	}
	out.Uint32(0) // cb_auxout
	return out.Bytes()
}
