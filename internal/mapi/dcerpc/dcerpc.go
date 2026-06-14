// Package dcerpc implements the subset of the DCE/RPC connection-oriented
// protocol (C706, MS-RPCE 2.2) the EMSMDB endpoint needs when carried over
// RPC-over-HTTP (Outlook Anywhere): the 16-byte common header plus the BIND,
// ALTER, REQUEST, RESPONSE, FAULT and BIND_NAK PDUs.
//
// PDU bodies are encoded with the NDR transfer syntax, so this package builds
// on internal/mapi/ndr for type-size alignment relative to the start of the
// PDU. The server pulls BIND and REQUEST PDUs from the client and pushes
// BIND_ACK, RESPONSE and FAULT PDUs back; those are the directions implemented
// here. Auth trailers (NTLMSSP) are located by AuthLength but parsed by the
// auth layer, not here.
package dcerpc

import (
	"encoding/binary"
	"fmt"

	"github.com/umailserver/umailserver/internal/mapi/ndr"
	"github.com/umailserver/umailserver/internal/mapi/wire"
)

// Connection-oriented PDU types (MS-RPCE 2.2.6.1 PTYPE values). These match the
// DCE 1.2.2 numbering used by interoperating clients (AUTH3 is 16, not 19).
const (
	PktRequest  uint8 = 0
	PktResponse uint8 = 2
	PktFault    uint8 = 3
	PktBind     uint8 = 11
	PktBindAck  uint8 = 12
	PktBindNak  uint8 = 13
	PktAlter    uint8 = 14
	PktAlterAck uint8 = 15
	PktAuth3    uint8 = 16
	PktRTS      uint8 = 20
)

// PFC (protocol feature) flags carried in the common header.
const (
	PFCFirstFrag  uint8 = 0x01
	PFCLastFrag   uint8 = 0x02
	PFCObjectUUID uint8 = 0x80
)

// BIND_NAK reject reasons (C706, used when refusing a bind).
const (
	BindReasonNotSpecified         uint16 = 0
	BindReasonAbstractSyntaxUnsupp uint16 = 1
)

const (
	rpcVers      = 5
	rpcVersMinor = 0
	drepLE       = 0x10 // little-endian integers, ASCII chars, IEEE floats
	headerLen    = 16
)

// SyntaxID is a DCERPC presentation syntax: an interface or transfer-syntax
// UUID and its 32-bit version (C706 p_syntax_id_t).
type SyntaxID struct {
	UUID    wire.GUID
	Version uint32
}

// CtxList is one presentation context offered in a BIND: an abstract interface
// and the transfer syntaxes the client can speak for it.
type CtxList struct {
	ContextID        uint16
	AbstractSyntax   SyntaxID
	TransferSyntaxes []SyntaxID
}

// Bind is a decoded BIND or ALTER PDU body.
type Bind struct {
	MaxXmitFrag  uint16
	MaxRecvFrag  uint16
	AssocGroupID uint32
	Contexts     []CtxList
	AuthInfo     []byte // raw auth verifier (NTLMSSP negotiate); empty under Basic
}

// Request is a decoded REQUEST PDU body.
type Request struct {
	AllocHint uint32
	ContextID uint16
	Opnum     uint16
	Object    *wire.GUID // set only when PFCObjectUUID is present
	Stub      []byte     // operation stub data, auth verifier removed
}

// Packet is a decoded connection-oriented PDU. The body pointer matching Type
// is populated; the others stay nil.
type Packet struct {
	Type        uint8
	PFCFlags    uint8
	DREP        [4]byte
	FragLength  uint16
	AuthLength  uint16
	CallID      uint32
	Bind        *Bind
	Request     *Request
	AuthTrailer []byte // raw DCERPC_AUTH trailer (header + credentials) when AuthLength > 0
}

// Pull decodes one PDU from the front of b. b may contain trailing bytes (for
// example a following PDU in the same channel buffer); the decoded PDU spans
// b[:FragLength].
func Pull(b []byte) (*Packet, error) {
	if len(b) < headerLen {
		return nil, fmt.Errorf("dcerpc: short header (%d bytes)", len(b))
	}
	p := ndr.NewPull(b)
	pkt := &Packet{}
	vers := p.Uint8()
	p.Uint8() // rpc_vers_minor
	pkt.Type = p.Uint8()
	pkt.PFCFlags = p.Uint8()
	copy(pkt.DREP[:], p.Bytes(4))
	pkt.FragLength = p.Uint16()
	pkt.AuthLength = p.Uint16()
	pkt.CallID = p.Uint32()
	if err := p.Err(); err != nil {
		return nil, err
	}
	if vers != rpcVers {
		return nil, fmt.Errorf("dcerpc: unsupported RPC version %d", vers)
	}
	if pkt.DREP[0] != drepLE {
		return nil, fmt.Errorf("dcerpc: unsupported data representation %#x (only little-endian)", pkt.DREP[0])
	}
	if int(pkt.FragLength) < headerLen || int(pkt.FragLength) > len(b) {
		return nil, fmt.Errorf("dcerpc: fragment length %d out of range (buffer %d)", pkt.FragLength, len(b))
	}

	switch pkt.Type {
	case PktBind, PktAlter:
		pkt.Bind = pullBind(p, pkt)
	case PktRequest:
		pkt.Request = pullRequest(p, pkt)
	}
	if err := p.Err(); err != nil {
		return nil, err
	}
	return pkt, nil
}

func pullSyntax(p *ndr.Pull) SyntaxID {
	p.Align(4)
	return SyntaxID{UUID: p.GUID(), Version: p.Uint32()}
}

func pullCtxList(p *ndr.Pull) CtxList {
	p.Align(4)
	var c CtxList
	c.ContextID = p.Uint16()
	n := p.Uint8()
	// A reserved byte follows num_transfer_syntaxes; the abstract syntax's own
	// 4-alignment consumes it.
	c.AbstractSyntax = pullSyntax(p)
	for i := 0; i < int(n); i++ {
		c.TransferSyntaxes = append(c.TransferSyntaxes, pullSyntax(p))
	}
	return c
}

func pullBind(p *ndr.Pull, pkt *Packet) *Bind {
	p.Align(4)
	b := &Bind{}
	b.MaxXmitFrag = p.Uint16()
	b.MaxRecvFrag = p.Uint16()
	b.AssocGroupID = p.Uint32()
	n := p.Uint8()
	for i := 0; i < int(n); i++ {
		b.Contexts = append(b.Contexts, pullCtxList(p))
	}
	// The auth verifier, when present, is everything left within the fragment.
	if rem := int(pkt.FragLength) - p.Offset(); rem > 0 {
		b.AuthInfo = p.Bytes(rem)
	}
	return b
}

func pullRequest(p *ndr.Pull, pkt *Packet) *Request {
	p.Align(4)
	r := &Request{}
	r.AllocHint = p.Uint32()
	r.ContextID = p.Uint16()
	r.Opnum = p.Uint16()
	if pkt.PFCFlags&PFCObjectUUID != 0 {
		g := p.GUID()
		r.Object = &g
	}
	p.Align(8) // stub data is 8-aligned relative to the start of the PDU
	stubLen := int(pkt.FragLength) - p.Offset()
	if pkt.AuthLength > 0 {
		stubLen -= int(pkt.AuthLength) + authTrailerHeaderLen
	}
	if stubLen < 0 {
		p.Fault()
		return r
	}
	r.Stub = p.Bytes(stubLen)
	if pkt.AuthLength > 0 {
		pkt.AuthTrailer = p.Bytes(int(pkt.AuthLength) + authTrailerHeaderLen)
	}
	return r
}

// authTrailerHeaderLen is the fixed DCERPC_AUTH header preceding the credential
// blob (auth_type, auth_level, auth_pad_length, auth_reserved, auth_context_id).
const authTrailerHeaderLen = 8

// AckResult is one entry in a BIND_ACK result list: the negotiation outcome for
// a presentation context plus the transfer syntax the server accepted.
type AckResult struct {
	Result uint16
	Reason uint16
	Syntax SyntaxID
}

func writeHeader(p *ndr.Push, pktType, pfcFlags uint8, authLength uint16, callID uint32) {
	p.Uint8(rpcVers)
	p.Uint8(rpcVersMinor)
	p.Uint8(pktType)
	p.Uint8(pfcFlags)
	p.Raw([]byte{drepLE, 0, 0, 0})
	p.Uint16(0) // frag_length placeholder, patched once the PDU is complete
	p.Uint16(authLength)
	p.Uint32(callID)
}

func patchFragLength(b []byte) []byte {
	binary.LittleEndian.PutUint16(b[8:10], uint16(len(b)))
	return b
}

func pushSyntax(p *ndr.Push, s SyntaxID) {
	p.Align(4)
	p.GUID(s.UUID)
	p.Uint32(s.Version)
}

// EncodeBindAck builds a complete BIND_ACK PDU accepting the negotiated
// presentation contexts. secAddr is the secondary address annotation (empty for
// a zero-length field).
func EncodeBindAck(callID uint32, maxXmit, maxRecv uint16, assocGroup uint32, secAddr string, results []AckResult) []byte {
	p := ndr.NewPush()
	writeHeader(p, PktBindAck, PFCFirstFrag|PFCLastFrag, 0, callID)
	p.Uint16(maxXmit)
	p.Uint16(maxRecv)
	p.Uint32(assocGroup)
	if secAddr == "" {
		p.Uint16(0)
	} else {
		p.Uint16(uint16(len(secAddr) + 1))
		p.Raw([]byte(secAddr))
		p.Uint8(0)
	}
	p.Align(4) // pad the result list to a 4-byte boundary
	p.Uint8(uint8(len(results)))
	for _, r := range results {
		p.Align(4)
		p.Uint16(r.Result)
		p.Uint16(r.Reason)
		pushSyntax(p, r.Syntax)
	}
	return patchFragLength(p.Bytes())
}

// EncodeBindNak builds a BIND_NAK PDU refusing a bind with the given reason.
func EncodeBindNak(callID uint32, reason uint16) []byte {
	p := ndr.NewPush()
	writeHeader(p, PktBindNak, PFCFirstFrag|PFCLastFrag, 0, callID)
	p.Uint16(reason)
	return patchFragLength(p.Bytes())
}

// EncodeResponse builds a complete RESPONSE PDU carrying stub as the response
// stub data.
func EncodeResponse(callID uint32, contextID uint16, stub []byte) []byte {
	p := ndr.NewPush()
	writeHeader(p, PktResponse, PFCFirstFrag|PFCLastFrag, 0, callID)
	p.Uint32(uint32(len(stub))) // alloc_hint
	p.Uint16(contextID)
	p.Uint8(0) // cancel_count
	p.Align(8) // reserved byte plus 8-alignment of the stub
	p.Raw(stub)
	return patchFragLength(p.Bytes())
}

// EncodeFault builds a FAULT PDU reporting an RPC runtime status to the client.
func EncodeFault(callID uint32, contextID uint16, status uint32) []byte {
	p := ndr.NewPush()
	writeHeader(p, PktFault, PFCFirstFrag|PFCLastFrag, 0, callID)
	p.Uint32(0) // alloc_hint
	p.Uint16(contextID)
	p.Uint8(0)      // cancel_count
	p.Uint32(status) // 4-alignment inserts the reserved byte
	return patchFragLength(p.Bytes())
}

// EncodeRTS builds an RTS (Request To Send) PDU used by the RPC-over-HTTP
// tunnel: the common header with the RTS packet type, then the RTS-specific
// flags and command count, then the caller's pre-marshaled command bytes. RTS
// PDUs carry no auth and use call id 0 (MS-RPCH 2.2.3.6.1).
func EncodeRTS(flags, numCommands uint16, commands []byte) []byte {
	p := ndr.NewPush()
	writeHeader(p, PktRTS, PFCFirstFrag|PFCLastFrag, 0, 0)
	p.Uint16(flags)
	p.Uint16(numCommands)
	p.Raw(commands)
	return patchFragLength(p.Bytes())
}
