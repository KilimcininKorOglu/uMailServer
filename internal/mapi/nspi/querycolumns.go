package nspi

import (
	"net/http"

	"github.com/umailserver/umailserver/internal/mapi/wire"
)

// handleQueryColumns returns the set of property columns the address book serves
// (MS-OXNSPI 2.2.9 QueryColumns): the same default columns the GAL rows carry.
func (s *Server) handleQueryColumns(w http.ResponseWriter, r *http.Request, body []byte) {
	p := wire.NewPull(body, wire.FlagABK|wire.FlagUTF16)
	_ = p.Uint32() // reserved
	_ = p.Uint32() // flags
	readAuxIn(p)
	if p.Err() != nil {
		s.writeResponse(w, r, "QueryColumns", "", queryColumnsResult(ecError, nil))
		return
	}
	s.writeResponse(w, r, "QueryColumns", "", queryColumnsResult(ecSuccess, defaultColumns))
}

// queryColumnsResult serializes a QueryColumns response (MS-OXNSPI 2.2.9.2):
// status, result, then the column tags on success.
func queryColumnsResult(result uint32, cols []wire.PropTag) []byte {
	out := wire.NewPush(wire.FlagABK | wire.FlagUTF16)
	out.Uint32(0) // status
	out.Uint32(result)
	if result != ecSuccess {
		out.Uint8(0) // no columns
		out.Uint32(0)
		return out.Bytes()
	}
	out.Uint8(0xFF) // columns present
	pushProptags(out, cols)
	out.Uint32(0) // cb_auxout
	return out.Bytes()
}
