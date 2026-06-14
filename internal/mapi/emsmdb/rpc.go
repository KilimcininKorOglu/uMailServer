package emsmdb

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/umailserver/umailserver/internal/mapi/ndr"
	"github.com/umailserver/umailserver/internal/mapi/wire"
)

// EMSMDB RPC interface opnums (MS-OXCRPC 3.1.4). Only the operations a modern
// online-mode client drives are handled; the rest fault.
const (
	opEcDoDisconnect uint16 = 1
	opEcDoConnectEx  uint16 = 10
	opEcDoRpcExt2    uint16 = 11
)

const (
	connectUserDNMax = 1024    // EcDoConnectEx userdn ceiling (MS-OXCRPC)
	rpcMaxRopOut     = 0x40000 // EcDoRpcExt2 cb_out ceiling
)

// serverProtocolVersion is the rgwServerVersion triple advertised in
// EcDoConnectEx, derived once from ServerVersion using the MS-OXCRPC 3.1.4.1.1
// "high bit" encoding.
var serverProtocolVersion = encodeServerVersion(ServerVersion)

// encodeServerVersion converts a dotted "a.b.c.d" build string into the 3-word
// version EcDoConnectEx returns. A malformed string yields a zero triple.
func encodeServerVersion(s string) [3]uint16 {
	parts := strings.Split(s, ".")
	if len(parts) != 4 {
		return [3]uint16{}
	}
	var n [4]uint16
	for i, p := range parts {
		v, err := strconv.Atoi(p)
		if err != nil || v < 0 || v > 0xFFFF {
			return [3]uint16{}
		}
		n[i] = uint16(v)
	}
	return [3]uint16{(n[0] << 8) | n[1], n[2] | 0x8000, n[3]}
}

// RPCServer exposes the EMSMDB RPC interface (EcDoConnectEx, EcDoRpcExt2,
// EcDoDisconnect) carried over RPC-over-HTTP (Outlook Anywhere). It is the
// binary-transport peer of the MAPI/HTTP Server: EcDoConnectEx creates a
// session keyed by the returned RPC context handle, and EcDoRpcExt2 runs the
// same ROP buffer through the shared dispatcher.
type RPCServer struct {
	dispatcher ROPDispatcher
	mu         sync.Mutex
	sessions   map[wire.GUID]*Session
}

// NewRPCServer returns an EMSMDB RPC endpoint backed by the given ROP dispatcher.
func NewRPCServer(dispatcher ROPDispatcher) *RPCServer {
	return &RPCServer{dispatcher: dispatcher, sessions: make(map[wire.GUID]*Session)}
}

func (s *RPCServer) putSession(h wire.GUID, sess *Session) {
	s.mu.Lock()
	s.sessions[h] = sess
	s.mu.Unlock()
}

func (s *RPCServer) getSession(h wire.GUID) *Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessions[h]
}

func (s *RPCServer) dropSession(h wire.GUID) {
	s.mu.Lock()
	delete(s.sessions, h)
	s.mu.Unlock()
}

// HandleRPC dispatches one EMSMDB RPC operation. stub is the NDR request body
// from the DCERPC REQUEST; the returned bytes are the NDR response body for the
// DCERPC RESPONSE. email identifies the authenticated mailbox (from Basic auth).
// ok is false for an opnum this server does not implement, so the caller emits
// a FAULT rather than a RESPONSE.
func (s *RPCServer) HandleRPC(opnum uint16, email string, stub []byte) (resp []byte, ok bool) {
	switch opnum {
	case opEcDoConnectEx:
		return s.connectEx(email, stub), true
	case opEcDoRpcExt2:
		return s.rpcExt2(stub), true
	case opEcDoDisconnect:
		return s.disconnect(stub), true
	default:
		return nil, false
	}
}

// connectExIn holds the EcDoConnectEx request fields this server acts on; the
// remaining fields are read to advance the NDR stream but not retained.
type connectExIn struct {
	userDN     string
	clientVers [3]uint16
}

func pullConnectExIn(p *ndr.Pull) connectExIn {
	var in connectExIn
	p.ULong()             // userdn max_count
	p.ULong()             // userdn offset
	length := p.ULong()   // userdn actual_count (includes NUL)
	if length > connectUserDNMax {
		p.Fault()
		return in
	}
	in.userDN = strings.TrimRight(p.Str(int(length)), "\x00")
	p.Uint32()            // flags
	p.Uint32()            // conmod
	p.Uint32()            // limit
	p.Uint32()            // cpid
	p.Uint32()            // lcid_string
	p.Uint32()            // lcid_sort
	p.Uint32()            // cxr_link
	p.Uint16()            // cnvt_cps
	in.clientVers[0] = p.Uint16()
	in.clientVers[1] = p.Uint16()
	in.clientVers[2] = p.Uint16()
	p.Uint32()            // timestamp
	auxSize := p.ULong()  // pauxin max_count
	p.Bytes(int(auxSize)) // pauxin
	p.Uint32()            // cb_auxin
	p.Uint32()            // cb_auxout
	return in
}

func (s *RPCServer) connectEx(email string, stub []byte) []byte {
	p := ndr.NewPull(stub)
	in := pullConnectExIn(p)
	out := ndr.NewPush()
	if p.Err() != nil || email == "" {
		writeConnectExOut(out, ndr.ContextHandle{}, [3]uint16{}, "", "", ecAccessDenied)
		return out.Bytes()
	}
	handle, err := newContextHandle()
	if err != nil {
		writeConnectExOut(out, ndr.ContextHandle{}, [3]uint16{}, "", "", ecError)
		return out.Bytes()
	}
	s.putSession(handle.GUID, &Session{ID: guidHex(handle.GUID), Email: email})
	writeConnectExOut(out, handle, in.clientVers, orgDnPrefix, email, ecSuccess)
	return out.Bytes()
}

// writeConnectExOut marshals ECDOCONNECTEX_OUT: the context handle, polling and
// retry hints, the DN prefix and display name (unique-pointer strings), the
// server and best protocol versions, an empty aux buffer, and the result code.
func writeConnectExOut(out *ndr.Push, cxh ndr.ContextHandle, clientVers [3]uint16, dnPrefix, displayName string, result uint32) {
	out.CtxHandle(cxh)
	out.Uint32(connectPollsMax)
	out.Uint32(connectRetryCount)
	out.Uint32(connectRetryDelay)
	out.Uint16(0) // cxr session index
	writeNDRString(out, dnPrefix)
	writeNDRString(out, displayName)
	out.Uint16(serverProtocolVersion[0])
	out.Uint16(serverProtocolVersion[1])
	out.Uint16(serverProtocolVersion[2])
	out.Uint16(clientVers[0]) // best version echoes the client's
	out.Uint16(clientVers[1])
	out.Uint16(clientVers[2])
	out.Uint32(0) // timestamp
	writeNDRBytes(out, nil)
	out.Uint32(result)
}

func (s *RPCServer) rpcExt2(stub []byte) []byte {
	p := ndr.NewPull(stub)
	cxh := p.CtxHandle()
	p.Uint32()          // flags
	pinMax := p.ULong() // pin max_count
	pin := p.Bytes(int(pinMax))
	cbIn := p.Uint32()
	cbOut := p.Uint32() // requested maximum response size
	auxSize := p.ULong()
	p.Bytes(int(auxSize)) // pauxin
	p.Uint32()            // cb_auxin
	p.Uint32()            // cb_auxout

	out := ndr.NewPush()
	if p.Err() != nil || cbIn != pinMax || cbOut > rpcMaxRopOut {
		writeRpcExt2Out(out, cxh, nil, ecError)
		return out.Bytes()
	}
	sess := s.getSession(cxh.GUID)
	if sess == nil {
		writeRpcExt2Out(out, ndr.ContextHandle{}, nil, ecError)
		return out.Bytes()
	}
	version, ropData, handlesIn, derr := DecodeROPBuffer(pin)
	if derr != nil {
		writeRpcExt2Out(out, cxh, nil, ecError)
		return out.Bytes()
	}
	var ropResp []byte
	var handlesOut []uint32
	if s.dispatcher != nil {
		sess.Lock()
		ropResp, handlesOut = s.dispatcher.Dispatch(sess, ropData, handlesIn, int(cbOut))
		sess.Unlock()
	}
	pout := EncodeROPBuffer(version, ropResp, handlesOut, false)
	writeRpcExt2Out(out, cxh, pout, ecSuccess)
	return out.Bytes()
}

// writeRpcExt2Out marshals ECDORPCEXT2_OUT: the echoed context handle, output
// flags, the response ROP buffer, an empty aux buffer, elapsed time, and the
// transport result code (ROP-level failures travel inside pout).
func writeRpcExt2Out(out *ndr.Push, cxh ndr.ContextHandle, pout []byte, result uint32) {
	out.CtxHandle(cxh)
	out.Uint32(0) // flags out
	writeNDRBytes(out, pout)
	writeNDRBytes(out, nil) // pauxout
	out.Uint32(0)           // trans_time
	out.Uint32(result)
}

func (s *RPCServer) disconnect(stub []byte) []byte {
	p := ndr.NewPull(stub)
	cxh := p.CtxHandle()
	if p.Err() == nil {
		if sess := s.getSession(cxh.GUID); sess != nil {
			sess.closeNotify()
		}
		s.dropSession(cxh.GUID)
	}
	out := ndr.NewPush()
	out.CtxHandle(ndr.ContextHandle{}) // handle is invalidated on disconnect
	out.Uint32(ecSuccess)
	return out.Bytes()
}

// writeNDRString marshals an NDR unique-pointer string: a referent id followed
// by the conformant-varying character array (max, offset, actual, then the
// NUL-terminated characters), as EcDoConnectEx returns for the DN prefix and
// display name.
func writeNDRString(out *ndr.Push, s string) {
	out.UniquePtr(true)
	length := uint32(len(s) + 1) // includes the trailing NUL
	out.ULong(length)
	out.ULong(0)
	out.ULong(length)
	out.Raw([]byte(s))
	out.Uint8(0)
}

// writeNDRBytes marshals a conformant-varying octet array (max, offset, actual,
// the bytes) followed by the trailing size scalar, as the EcDoConnectEx and
// EcDoRpcExt2 output buffers use.
func writeNDRBytes(out *ndr.Push, b []byte) {
	n := uint32(len(b))
	out.ULong(n)
	out.ULong(0)
	out.ULong(n)
	out.Raw(b)
	out.Uint32(n)
}

// newContextHandle mints a fresh RPC context handle with a random GUID.
func newContextHandle() (ndr.ContextHandle, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return ndr.ContextHandle{}, err
	}
	var g wire.GUID
	g.TimeLow = binary.LittleEndian.Uint32(b[0:4])
	g.TimeMid = binary.LittleEndian.Uint16(b[4:6])
	g.TimeHiAndVersion = binary.LittleEndian.Uint16(b[6:8])
	copy(g.ClockSeq[:], b[8:10])
	copy(g.Node[:], b[10:16])
	return ndr.ContextHandle{HandleType: 0, GUID: g}, nil
}

// guidHex renders a GUID as a stable hex string for use as a session id.
func guidHex(g wire.GUID) string {
	return fmt.Sprintf("%08x%04x%04x%02x%06x", g.TimeLow, g.TimeMid, g.TimeHiAndVersion, g.ClockSeq, g.Node)
}
