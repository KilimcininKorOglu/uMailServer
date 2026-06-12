package nspi

import (
	"errors"

	"github.com/umailserver/umailserver/internal/mapi/wire"
)

// ErrCorrupt indicates a malformed NSPI request body.
var ErrCorrupt = errors.New("nspi: corrupt request")

// NSPI result codes returned in responses.
const (
	ecSuccess       uint32 = 0x00000000
	ecError         uint32 = 0x80004005
	ecNotFound      uint32 = 0x8004010F // MAPI_E_NOT_FOUND (absent property)
	ecNotSupported  uint32 = 0x80040102 // MAPI_E_NO_SUPPORT (read-only address book)
	ecUnbindSuccess uint32 = 0x00000001 // MAPI_E_UNBINDSUCCESS (NSPI Unbind success)
)

// readAuxIn consumes the trailing cb_auxin-counted auxiliary buffer common to
// every NSPI request and reports a parse error.
func readAuxIn(p *wire.Pull) {
	if cb := int(p.Uint32()); cb > 0 {
		p.Bytes(cb)
	}
}

// BindRequest is the NSPI Bind request (MS-OXNSPI 2.2.1.1 / MS-OXCMAPIHTTP
// 2.2.5.1): bind flags, an optional state block, then the auxiliary buffer.
type BindRequest struct {
	Flags   uint32
	HasStat bool
	Stat    Stat
}

// DecodeBindRequest parses a Bind request body.
func DecodeBindRequest(b []byte) (BindRequest, error) {
	p := wire.NewPull(b, 0)
	var r BindRequest
	r.Flags = p.Uint32()
	if p.Uint8() != 0 {
		r.HasStat = true
		r.Stat = PullStat(p)
	}
	readAuxIn(p)
	if p.Err() != nil {
		return BindRequest{}, ErrCorrupt
	}
	return r, nil
}

// BindResponse is the NSPI Bind response (MS-OXNSPI 2.2.1.1.2): a status, the
// result code, the server GUID, and the auxiliary-out length.
type BindResponse struct {
	Status     uint32
	Result     uint32
	ServerGUID wire.GUID
}

// Encode serializes the Bind response.
func (r BindResponse) Encode() []byte {
	p := wire.NewPush(0)
	p.Uint32(r.Status)
	p.Uint32(r.Result)
	p.GUID(r.ServerGUID)
	p.Uint32(0) // cb_auxout
	return p.Bytes()
}

// DecodeUnbindRequest parses an Unbind request body (a reserved u32 then the
// auxiliary buffer).
func DecodeUnbindRequest(b []byte) error {
	p := wire.NewPull(b, 0)
	_ = p.Uint32() // reserved
	readAuxIn(p)
	if p.Err() != nil {
		return ErrCorrupt
	}
	return nil
}

// UnbindResponse is the NSPI Unbind response (MS-OXNSPI 2.2.1.2.2).
type UnbindResponse struct {
	Status uint32
	Result uint32
}

// Encode serializes the Unbind response.
func (r UnbindResponse) Encode() []byte {
	p := wire.NewPush(0)
	p.Uint32(r.Status)
	p.Uint32(r.Result)
	p.Uint32(0) // cb_auxout
	return p.Bytes()
}
