package nspi

import (
	"net/http"
	"strings"

	"github.com/umailserver/umailserver/internal/mapi/wire"
)

// handleDNToMID maps each requested distinguished name to the minimal id of the
// GAL entry it names (MS-OXNSPI 2.2.5 DNToMId). A name that matches no entry
// maps to 0. The request and response carry no address-book presence bytes, and
// the names are 8-bit strings.
func (s *Server) handleDNToMID(w http.ResponseWriter, r *http.Request, body []byte) {
	email := emailFromContext(r.Context())
	p := wire.NewPull(body, 0)
	_ = p.Uint32() // reserved
	var dns []string
	if p.Uint8() != 0 {
		n := int(p.Uint32())
		dns = make([]string, 0, n)
		for range n {
			if p.Err() != nil {
				break
			}
			dns = append(dns, p.Str())
		}
	}
	readAuxIn(p)
	if p.Err() != nil || s.dir == nil || email == "" {
		s.writeResponse(w, r, "DNToMId", "", dnToMidResult(ecError, nil))
		return
	}

	gal := s.dir.ResolveGAL("")
	mids := make([]uint32, len(dns))
	for i, dn := range dns {
		mids[i] = nameUnresolved // 0 when the name resolves to no entry
		if strings.TrimSpace(dn) == "" {
			continue
		}
		for idx, e := range gal {
			if strings.EqualFold(e.x500DN(), dn) {
				mids[i] = entryMid(idx)
				break
			}
		}
	}
	s.writeResponse(w, r, "DNToMId", "", dnToMidResult(ecSuccess, mids))
}

// dnToMidResult serializes a DNToMId response (MS-OXNSPI 2.2.5.2): status,
// result, then the minimal-id array on success. The op carries no ABK flag.
func dnToMidResult(result uint32, mids []uint32) []byte {
	out := wire.NewPush(0)
	out.Uint32(0) // status
	out.Uint32(result)
	if result != ecSuccess {
		out.Uint8(0) // no minimal-id array
		out.Uint32(0)
		return out.Bytes()
	}
	out.Uint8(0xFF) // minimal-id array present
	pushU32Array(out, mids)
	out.Uint32(0) // cb_auxout
	return out.Bytes()
}
