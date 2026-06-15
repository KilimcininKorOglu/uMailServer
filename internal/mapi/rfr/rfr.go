// Package rfr implements the MS-OXABREF Address Book Name Service Provider
// Referral interface (RfrGetNewDSA, RfrGetFQDNFromLegacyDN) carried over
// RPC-over-HTTP. A client calls it to discover the NSPI server it should bind
// for the address book. uMailServer is single-homed, so both operations refer
// every mailbox to the server's own DNS FQDN.
package rfr

import (
	"strings"

	"github.com/umailserver/umailserver/internal/mapi/ndr"
	"github.com/umailserver/umailserver/internal/mapi/wire"
)

// InterfaceUUID is the MS-OXABREF abstract-syntax UUID
// (1544f5e0-613c-11d1-93df-00c04fd7bd09) clients bind to reach the referral
// interface.
var InterfaceUUID = wire.GUID{
	TimeLow:          0x1544f5e0,
	TimeMid:          0x613c,
	TimeHiAndVersion: 0x11d1,
	ClockSeq:         [2]byte{0x93, 0xdf},
	Node:             [6]byte{0x00, 0xc0, 0x4f, 0xd7, 0xbd, 0x09},
}

// MS-OXABREF interface opnums (3.1.4).
const (
	opRfrGetNewDSA           uint16 = 0
	opRfrGetFQDNFromLegacyDN uint16 = 1
)

// MAPI result codes returned in the operation status field.
const (
	rfrSuccess uint32 = 0x00000000
	rfrError   uint32 = 0x80004005 // ecError: general failure
)

// dnMax bounds the strings a client may submit: MS-OXABREF 3.1.4.2 caps
// cbMailboxServerDN at 1024, and the referral hint strings are far shorter.
const (
	dnMax   = 1024
	hintMax = 256
)

// Server answers MS-OXABREF referrals. fqdn returns the DNS name of the NSPI
// server to advertise; it is read live so a hot-reloaded hostname takes effect.
type Server struct {
	fqdn func() string
}

// NewServer returns an RFR endpoint that advertises the FQDN fqdn yields.
func NewServer(fqdn func() string) *Server { return &Server{fqdn: fqdn} }

// HandleRPC dispatches one MS-OXABREF operation. stub is the NDR request body;
// the returned bytes are the NDR response body. ok is false for an opnum this
// interface does not implement, so the tunnel emits a FAULT. The authenticated
// email is unused: a single-homed server refers every caller to the same NSPI
// FQDN.
func (s *Server) HandleRPC(opnum uint16, _ string, stub []byte) (resp []byte, ok bool) {
	switch opnum {
	case opRfrGetNewDSA:
		return s.getNewDSA(stub), true
	case opRfrGetFQDNFromLegacyDN:
		return s.getFQDNFromLegacyDN(stub), true
	default:
		return nil, false
	}
}

// getNewDSA answers RfrGetNewDSA (MS-OXABREF 3.1.4.1): it returns the NSPI
// server's DNS FQDN in ppszServer. pUserDN and the in/out ppszUnused/ppszServer
// hints are decoded only to validate the request — a single-homed server refers
// every mailbox to itself.
func (s *Server) getNewDSA(stub []byte) []byte {
	p := ndr.NewPull(stub)
	parseNewDSAIn(p)
	out := ndr.NewPush()
	server := strings.TrimSpace(s.fqdn())
	if p.Err() != nil || server == "" {
		out.UniquePtr(false) // ppszUnused: NULL
		out.UniquePtr(false) // ppszServer: NULL
		out.Uint32(rfrError)
		return out.Bytes()
	}
	out.UniquePtr(false)             // ppszUnused: not returned
	writeReferralPtrPtr(out, server) // ppszServer: [in, out, unique, string] **
	out.Uint32(rfrSuccess)
	return out.Bytes()
}

// getFQDNFromLegacyDN answers RfrGetFQDNFromLegacyDN (MS-OXABREF 3.1.4.2): it
// maps a mailbox-server legacyExchangeDN to the NSPI server's DNS FQDN. A
// single-homed server returns its own FQDN for any well-formed DN.
func (s *Server) getFQDNFromLegacyDN(stub []byte) []byte {
	p := ndr.NewPull(stub)
	parseFQDNIn(p)
	out := ndr.NewPush()
	fqdn := strings.TrimSpace(s.fqdn())
	if p.Err() != nil || fqdn == "" {
		out.UniquePtr(false) // ppszServerFQDN: NULL
		out.Uint32(rfrError)
		return out.Bytes()
	}
	out.UniquePtr(true) // ppszServerFQDN: [out, ref, string] ** — single inner referent
	writeConformantStr(out, fqdn)
	out.Uint32(rfrSuccess)
	return out.Bytes()
}

// parseNewDSAIn decodes the RfrGetNewDSA request, faulting the pull on a
// structural violation. ulFlags then pUserDN (an [in, string] reference pointer,
// inline) then the two [in, out, unique, string] ** hint parameters.
func parseNewDSAIn(p *ndr.Pull) {
	p.Uint32() // ulFlags
	if !pullConformantStr(p, dnMax) {
		return
	}
	pullDoublePtrStr(p, hintMax) // ppszUnused
	pullDoublePtrStr(p, hintMax) // ppszServer
}

// parseFQDNIn decodes the RfrGetFQDNFromLegacyDN request: ulFlags, the bounded
// cbMailboxServerDN, then szMailboxServerDN as an [in, string] reference pointer.
func parseFQDNIn(p *ndr.Pull) {
	p.Uint32() // ulFlags
	cb := p.Uint32()
	if p.Err() != nil || cb < 10 || cb > dnMax {
		p.Fault()
		return
	}
	pullConformantStr(p, dnMax)
}

// pullConformantStr reads an NDR conformant-varying string header (max_count,
// offset, actual_count) and its characters, faulting the pull when the bounds
// are invalid or the length exceeds max.
func pullConformantStr(p *ndr.Pull, max uint32) bool {
	size := p.ULong()
	offset := p.ULong()
	length := p.ULong()
	if p.Err() != nil || offset != 0 || length > size || length > max {
		p.Fault()
		return false
	}
	p.Str(int(length))
	return p.Err() == nil
}

// pullDoublePtrStr reads a [unique] pointer to a [unique, string] pointer: the
// outer referent id, then — when present — the inner referent id and its string.
func pullDoublePtrStr(p *ndr.Pull, max uint32) {
	if p.ULong() == 0 {
		return
	}
	if p.ULong() == 0 {
		return
	}
	pullConformantStr(p, max)
}

// writeReferralPtrPtr marshals an [in, out, unique, string] ** out parameter:
// the outer and inner referent ids, then the conformant-varying string.
func writeReferralPtrPtr(out *ndr.Push, s string) {
	out.UniquePtr(true) // outer **
	out.UniquePtr(true) // inner *
	writeConformantStr(out, s)
}

// writeConformantStr marshals max_count, offset, actual_count and the
// NUL-terminated characters of an NDR conformant-varying string.
func writeConformantStr(out *ndr.Push, s string) {
	n := uint32(len(s) + 1) // characters plus the trailing NUL
	out.ULong(n)
	out.ULong(0)
	out.ULong(n)
	out.Raw([]byte(s))
	out.Uint8(0)
}
