package nspi

import (
	"net/http"

	"github.com/umailserver/umailserver/internal/mapi/wire"
)

// handleModProps rejects an attempt to modify an address-book entry's properties
// (MS-OXNSPI 2.2.10 ModProps). The GAL is read-only, so the operation is not
// supported.
func (s *Server) handleModProps(w http.ResponseWriter, r *http.Request, _ []byte) {
	s.writeResponse(w, r, "ModProps", "", modResult(ecNotSupported))
}

// handleModLinkAtt rejects an attempt to modify an address-book link attribute
// such as group membership (MS-OXNSPI 2.2.11 ModLinkAtt). The GAL is read-only.
func (s *Server) handleModLinkAtt(w http.ResponseWriter, r *http.Request, _ []byte) {
	s.writeResponse(w, r, "ModLinkAtt", "", modResult(ecNotSupported))
}

// modResult serializes the response shared by the address-book modification
// operations (MS-OXNSPI 2.2.10.2 / 2.2.11.2): status, result, and the
// auxiliary-out length. These responses carry no address-book flag.
func modResult(result uint32) []byte {
	out := wire.NewPush(0)
	out.Uint32(0) // status
	out.Uint32(result)
	out.Uint32(0) // cb_auxout
	return out.Bytes()
}
