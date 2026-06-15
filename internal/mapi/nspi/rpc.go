package nspi

import (
	"crypto/rand"
	"encoding/binary"
	"sync"
	"unicode/utf16"

	"github.com/umailserver/umailserver/internal/mapi/ndr"
	"github.com/umailserver/umailserver/internal/mapi/wire"
)

// InterfaceUUID is the MS-OXNSPI abstract-syntax UUID
// (f5cc5a18-4264-101a-8c59-08002b2f8426) clients bind to reach the address book
// over RPC-over-HTTP.
var InterfaceUUID = wire.GUID{
	TimeLow:          0xf5cc5a18,
	TimeMid:          0x4264,
	TimeHiAndVersion: 0x101a,
	ClockSeq:         [2]byte{0x8c, 0x59},
	Node:             [6]byte{0x08, 0x00, 0x2b, 0x2f, 0x84, 0x26},
}

// MS-OXNSPI interface opnums (3.1.4). Only the operations served over RPC are
// listed; the rest fault.
const (
	opNspiBind            uint16 = 0
	opNspiUnbind          uint16 = 1
	opNspiGetSpecialTable uint16 = 12
)

// RPCServer exposes the NSPI address book over the DCERPC transport (Outlook
// Anywhere). It is the binary-transport peer of the MAPI/HTTP Server: NspiBind
// creates a session keyed by the returned RPC context handle, and the read
// operations enumerate the same canonical directory through NDR instead of the
// MS-OXCMAPIHTTP flat shape.
type RPCServer struct {
	dir      Directory
	mu       sync.Mutex
	sessions map[wire.GUID]*rpcSession
}

// rpcSession is one bound NSPI address-book session, keyed by its context-handle
// GUID.
type rpcSession struct {
	email string
}

// NewRPCServer returns an NSPI RPC endpoint with no directory; SetDirectory
// attaches the GAL source the query operations read.
func NewRPCServer() *RPCServer {
	return &RPCServer{sessions: make(map[wire.GUID]*rpcSession)}
}

// SetDirectory attaches the GAL source the address-book query operations read.
func (s *RPCServer) SetDirectory(d Directory) { s.dir = d }

func (s *RPCServer) putSession(h wire.GUID, sess *rpcSession) {
	s.mu.Lock()
	s.sessions[h] = sess
	s.mu.Unlock()
}

func (s *RPCServer) hasSession(h wire.GUID) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.sessions[h]
	return ok
}

func (s *RPCServer) dropSession(h wire.GUID) {
	s.mu.Lock()
	delete(s.sessions, h)
	s.mu.Unlock()
}

// HandleRPC dispatches one NSPI RPC operation. stub is the NDR request body from
// the DCERPC REQUEST; the returned bytes are the NDR response body. email
// identifies the authenticated mailbox. ok is false for an opnum this server
// does not implement, so the tunnel emits a FAULT.
func (s *RPCServer) HandleRPC(opnum uint16, email string, stub []byte) (resp []byte, ok bool) {
	switch opnum {
	case opNspiBind:
		return s.bind(email, stub), true
	case opNspiUnbind:
		return s.unbind(stub), true
	case opNspiGetSpecialTable:
		return s.getSpecialTable(stub), true
	default:
		return nil, false
	}
}

// bind answers NspiBind (MS-OXNSPI 3.1.4.1.1): it authenticates the address-book
// session, mints a context handle, and returns the stable server GUID. The
// request carries dwFlags, a STAT block, and an optional server-GUID hint.
func (s *RPCServer) bind(email string, stub []byte) []byte {
	p := ndr.NewPull(stub)
	p.Uint32() // dwFlags
	pullStatNDR(p)
	if p.Uint32() != 0 { // pServerGuid referent
		p.Bytes(16) // FlatUID hint, ignored
	}
	out := ndr.NewPush()
	if p.Err() != nil || email == "" {
		// NULL server GUID, zeroed handle, error.
		out.UniquePtr(false)
		writeNSPIHandle(out, wire.GUID{})
		out.Uint32(ecError)
		return out.Bytes()
	}
	handle, err := newHandleGUID()
	if err != nil {
		out.UniquePtr(false)
		writeNSPIHandle(out, wire.GUID{})
		out.Uint32(ecError)
		return out.Bytes()
	}
	s.putSession(handle, &rpcSession{email: email})
	out.UniquePtr(true) // pServerGuid present
	out.Raw(flatUID(serverGUID()))
	writeNSPIHandle(out, handle)
	out.Uint32(ecSuccess)
	return out.Bytes()
}

// unbind answers NspiUnbind (MS-OXNSPI 3.1.4.1.2): it tears down the session and
// returns the now-invalid (zeroed) handle. Success is reported as
// MAPI_E_UNBINDSUCCESS, per the interface contract.
func (s *RPCServer) unbind(stub []byte) []byte {
	p := ndr.NewPull(stub)
	handle := readNSPIHandle(p)
	p.Uint32() // reserved
	out := ndr.NewPush()
	if p.Err() != nil {
		writeNSPIHandle(out, wire.GUID{})
		out.Uint32(ecError)
		return out.Bytes()
	}
	s.dropSession(handle)
	writeNSPIHandle(out, wire.GUID{}) // handle invalidated on unbind
	out.Uint32(ecUnbindSuccess)
	return out.Bytes()
}

// getSpecialTable answers NspiGetSpecialTable (MS-OXNSPI 3.1.4.1.3): it returns
// the address-book container hierarchy — a single Global Address List container.
// The creation-templates table is not served and yields an empty success.
func (s *RPCServer) getSpecialTable(stub []byte) []byte {
	p := ndr.NewPull(stub)
	handle := readNSPIHandle(p)
	flags := p.Uint32()
	pullStatNDR(p)
	p.Uint32() // client's cached hierarchy version
	out := ndr.NewPush()
	if p.Err() != nil || !s.hasSession(handle) {
		out.Uint32(specialTableVersion)
		out.UniquePtr(false) // NULL rowset
		out.Uint32(ecError)
		return out.Bytes()
	}
	var rows [][]wire.TaggedPropertyValue
	if flags&nspiAddressCreationTemplates == 0 {
		rows = [][]wire.TaggedPropertyValue{galContainerRow()}
	}
	out.Uint32(specialTableVersion)
	if len(rows) == 0 {
		out.UniquePtr(false) // empty success: NULL rowset
		out.Uint32(ecSuccess)
		return out.Bytes()
	}
	out.UniquePtr(true) // ppRows present
	pushRowSet(out, rows)
	out.Uint32(ecSuccess)
	return out.Bytes()
}

// pullStatNDR reads a STAT block from an NDR stream (MS-OXNSPI 2.3.7): nine
// 4-byte fields, 4-aligned. The decoded value is currently unused by the served
// operations but must be consumed to keep the stream aligned.
func pullStatNDR(p *ndr.Pull) Stat {
	p.Align(4)
	return Stat{
		SortType:       p.Uint32(),
		ContainerID:    p.Uint32(),
		CurrentRec:     p.Uint32(),
		Delta:          int32(p.Uint32()),
		NumPos:         p.Uint32(),
		TotalRec:       p.Uint32(),
		CodePage:       p.Uint32(),
		TemplateLocale: p.Uint32(),
		SortLocale:     p.Uint32(),
	}
}

// writeNSPIHandle marshals an NSPI context handle: a 4-byte handle type (always
// 0) followed by the 16-byte handle GUID (NDR CONTEXT_HANDLE).
func writeNSPIHandle(out *ndr.Push, g wire.GUID) {
	out.CtxHandle(ndr.ContextHandle{HandleType: 0, GUID: g})
}

// readNSPIHandle reads an NSPI context handle and returns its GUID, the session
// key.
func readNSPIHandle(p *ndr.Pull) wire.GUID {
	return p.CtxHandle().GUID
}

// pushRowSet marshals an NSP_ROWSET (MS-OXNSPI 2.3.4) in the two NDR passes the
// transfer syntax requires: the conformant count and every row's header first,
// then every row's deferred content.
func pushRowSet(out *ndr.Push, rows [][]wire.TaggedPropertyValue) {
	out.ULong(uint32(len(rows))) // conformant max_count
	out.Align(4)
	out.Uint32(uint32(len(rows))) // cRows
	for _, row := range rows {
		pushPropRowHeader(out, row)
	}
	for _, row := range rows {
		pushPropRowContent(out, row)
	}
}

// pushPropRowHeader writes a property row's fixed NDR header: a reserved word,
// the value count, and the unique-pointer referent for the value array.
func pushPropRowHeader(out *ndr.Push, row []wire.TaggedPropertyValue) {
	out.Align(4)
	out.Uint32(0) // reserved
	out.Uint32(uint32(len(row)))
	out.UniquePtr(len(row) > 0)
}

// pushPropRowContent writes a property row's deferred content: the conformant
// value count, then every value's header followed by every value's content.
func pushPropRowContent(out *ndr.Push, row []wire.TaggedPropertyValue) {
	if len(row) == 0 {
		return
	}
	out.ULong(uint32(len(row))) // conformant max_count of the value array
	for _, pv := range row {
		pushPropValueHeader(out, pv)
	}
	for _, pv := range row {
		pushPropValueContent(out, pv)
	}
}

// pushPropValueHeader writes a property value's NDR header: its tag, type, and
// either the inline scalar or the unique-pointer referent for deferred data
// (strings, binaries). Only the property types the served rows use are encoded.
func pushPropValueHeader(out *ndr.Push, pv wire.TaggedPropertyValue) {
	out.Align(4)
	out.Uint32(uint32(pv.Tag))
	out.Uint32(uint32(pv.Tag.Type()))
	switch pv.Tag.Type() {
	case wire.PtLong, wire.PtError:
		out.Uint32(valU32(pv.Value))
	case wire.PtBoolean:
		if valBool(pv.Value) {
			out.Uint8(1)
		} else {
			out.Uint8(0)
		}
	case wire.PtString8, wire.PtUnicode:
		out.UniquePtr(valStr(pv.Value) != "")
	case wire.PtBinary:
		b := valBytes(pv.Value)
		out.Uint32(uint32(len(b)))
		out.UniquePtr(len(b) > 0)
	}
}

// pushPropValueContent writes a property value's deferred NDR content. Scalars
// emitted everything in the header and write nothing here; strings and binaries
// write their conformant-varying payload.
func pushPropValueContent(out *ndr.Push, pv wire.TaggedPropertyValue) {
	switch pv.Tag.Type() {
	case wire.PtString8:
		s := valStr(pv.Value)
		if s == "" {
			return
		}
		n := uint32(len(s) + 1)
		out.ULong(n)
		out.ULong(0)
		out.ULong(n)
		out.Raw([]byte(s))
		out.Uint8(0)
	case wire.PtUnicode:
		s := valStr(pv.Value)
		if s == "" {
			return
		}
		u := utf16LEWithNUL(s)
		n := uint32(len(u) / 2)
		out.ULong(n)
		out.ULong(0)
		out.ULong(n)
		out.Raw(u)
	case wire.PtBinary:
		b := valBytes(pv.Value)
		if len(b) == 0 {
			return
		}
		out.ULong(uint32(len(b)))
		out.Raw(b)
	}
}

// valU32, valStr, valBytes and valBool extract a typed property value, returning
// the zero value when the stored type does not match. The rows are server-built,
// so a mismatch is a programming error rendered as an empty value, never a panic.
func valU32(v any) uint32 {
	if u, ok := v.(uint32); ok {
		return u
	}
	return 0
}

func valStr(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func valBytes(v any) []byte {
	if b, ok := v.([]byte); ok {
		return b
	}
	return nil
}

func valBool(v any) bool {
	b, ok := v.(bool)
	return ok && b
}

// utf16LEWithNUL encodes s as little-endian UTF-16 with a trailing NUL, the form
// the NSPI PtypString content carries (the count includes the terminator).
func utf16LEWithNUL(s string) []byte {
	units := utf16.Encode([]rune(s))
	b := make([]byte, 0, (len(units)+1)*2)
	for _, u := range units {
		b = binary.LittleEndian.AppendUint16(b, u)
	}
	return binary.LittleEndian.AppendUint16(b, 0)
}

// flatUID renders a GUID as the 16-byte FlatUID the NSPI server-GUID field
// carries: the standard binary GUID layout (Data1/2/3 little-endian, then the
// clock-sequence and node bytes verbatim).
func flatUID(g wire.GUID) []byte {
	b := make([]byte, 16)
	binary.LittleEndian.PutUint32(b[0:], g.TimeLow)
	binary.LittleEndian.PutUint16(b[4:], g.TimeMid)
	binary.LittleEndian.PutUint16(b[6:], g.TimeHiAndVersion)
	copy(b[8:], g.ClockSeq[:])
	copy(b[10:], g.Node[:])
	return b
}

// newHandleGUID mints a random context-handle GUID, the NSPI session key.
func newHandleGUID() (wire.GUID, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return wire.GUID{}, err
	}
	var g wire.GUID
	g.TimeLow = binary.LittleEndian.Uint32(b[0:4])
	g.TimeMid = binary.LittleEndian.Uint16(b[4:6])
	g.TimeHiAndVersion = binary.LittleEndian.Uint16(b[6:8])
	copy(g.ClockSeq[:], b[8:10])
	copy(g.Node[:], b[10:16])
	return g, nil
}
