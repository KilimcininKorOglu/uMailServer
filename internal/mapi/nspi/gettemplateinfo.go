package nspi

import (
	"net/http"

	"github.com/umailserver/umailserver/internal/mapi/wire"
)

// handleGetTemplateInfo returns the display template for an address-book entry
// type (MS-OXNSPI 2.2.7 GetTemplateInfo). No custom templates are served, so the
// response carries no template row and the client falls back to its built-ins.
func (s *Server) handleGetTemplateInfo(w http.ResponseWriter, r *http.Request, body []byte) {
	p := wire.NewPull(body, wire.FlagABK|wire.FlagUTF16)
	_ = p.Uint32() // flags
	_ = p.Uint32() // template type
	if p.Uint8() != 0 {
		p.Str() // distinguished name
	}
	codepage := p.Uint32()
	_ = p.Uint32() // locale id
	readAuxIn(p)
	if p.Err() != nil {
		s.writeResponse(w, r, "GetTemplateInfo", "", getTemplateInfoResult(ecError, codepage))
		return
	}
	s.writeResponse(w, r, "GetTemplateInfo", "", getTemplateInfoResult(ecSuccess, codepage))
}

// getTemplateInfoResult serializes a GetTemplateInfo response (MS-OXNSPI
// 2.2.7.2): status, result, code page, then no template row.
func getTemplateInfoResult(result, codepage uint32) []byte {
	out := wire.NewPush(wire.FlagABK | wire.FlagUTF16)
	out.Uint32(0) // status
	out.Uint32(result)
	out.Uint32(codepage)
	out.Uint8(0)  // no template row
	out.Uint32(0) // cb_auxout
	return out.Bytes()
}
