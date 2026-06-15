package rpch

import (
	"encoding/binary"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/umailserver/umailserver/internal/mapi/dcerpc"
	"github.com/umailserver/umailserver/internal/mapi/emsmdb"
	"github.com/umailserver/umailserver/internal/mapi/wire"
)

const (
	maxPDU            = 0x100000   // 1 MiB cap on a single framed PDU
	bindAssocGroup    = 0x534D4150 // arbitrary non-zero association group
	bindSecondaryAddr = "6001"     // MS-OXCRPC EMSMDB endpoint annotation
	faultOpRngError   = 0x1C010002 // nca_op_rng_error: opnum not implemented
	faultUnknownIf    = 0x1C010003 // nca_unk_if: request on an unbound context
	outRespBuffer     = 16
)

// transferSyntaxNDR32 is the standard NDR transfer syntax the bind negotiation
// accepts for every interface carried over the tunnel.
var transferSyntaxNDR32 = dcerpc.SyntaxID{
	UUID:    wire.GUID{TimeLow: 0x8a885d04, TimeMid: 0x1ceb, TimeHiAndVersion: 0x11c9, ClockSeq: [2]byte{0x9f, 0xe8}, Node: [6]byte{0x08, 0x00, 0x2b, 0x10, 0x48, 0x60}},
	Version: 2,
}

// emsmdbInterface is the EMSMDB abstract-syntax UUID (MS-OXCRPC 1.5) the Outlook
// Anywhere mailbox connector binds.
var emsmdbInterface = wire.GUID{
	TimeLow:          0xa4f1db00,
	TimeMid:          0xca47,
	TimeHiAndVersion: 0x1067,
	ClockSeq:         [2]byte{0xb3, 0x1f},
	Node:             [6]byte{0x00, 0xdd, 0x01, 0x06, 0x62, 0xda},
}

// rpcBackend is one DCERPC interface the tunnel dispatches operations to. The
// EMSMDB RPC server implements it today; RFR and NSPI-over-RPC register
// alongside it. HandleRPC runs one operation and returns ok=false for an opnum
// the interface does not implement, so the tunnel emits a FAULT.
type rpcBackend interface {
	HandleRPC(opnum uint16, email string, stub []byte) (resp []byte, ok bool)
}

// boundInterface pairs a registered backend with the secondary-address
// annotation its BIND_ACK advertises (MS-OXCRPC endpoint).
type boundInterface struct {
	backend rpcBackend
	secAddr string
}

// Handler terminates the MS-RPCH tunnel served at /rpc/rpcproxy.dll. It pairs an
// OUT channel (RPC_OUT_DATA, server-to-client stream) with an IN channel
// (RPC_IN_DATA, client-to-server stream) into a virtual connection, then
// tunnels DCERPC PDUs to the interface bound for each presentation context.
type Handler struct {
	mu    sync.Mutex
	conns map[cookie]*vconn
	// interfaces maps an abstract-syntax UUID to its backend. It is populated by
	// NewHandler and Register during setup and only read while serving, so it
	// needs no lock once the handler starts.
	interfaces map[wire.GUID]boundInterface
}

// NewHandler returns an RPC-over-HTTP tunnel with the EMSMDB RPC server bound to
// its interface UUID. Register adds further interfaces (RFR, NSPI) before the
// handler starts serving.
func NewHandler(rpc *emsmdb.RPCServer) *Handler {
	h := &Handler{
		conns:      make(map[cookie]*vconn),
		interfaces: make(map[wire.GUID]boundInterface),
	}
	h.Register(emsmdbInterface, rpc, bindSecondaryAddr)
	return h
}

// Register binds an additional DCERPC interface under its abstract-syntax UUID.
// It must be called during setup, before the handler serves any request.
// secAddr is the BIND_ACK secondary-address annotation for the interface.
func (h *Handler) Register(uuid wire.GUID, backend rpcBackend, secAddr string) {
	h.interfaces[uuid] = boundInterface{backend: backend, secAddr: secAddr}
}

// vconn is the rendezvous between the two channels of one virtual connection.
// The IN handler feeds response PDUs onto outResp; the OUT handler drains it to
// the client. inReady lets the OUT handler hold CONN/C2 until the IN channel
// has presented its CONN/B1.
type vconn struct {
	outResp   chan []byte
	inReady   chan struct{}
	readyOnce sync.Once
	respOnce  sync.Once
	// contexts maps a presentation-context id negotiated in a BIND/ALTER to the
	// interface its REQUESTs route to. It is touched only by the serveIn
	// goroutine, which processes the connection's PDUs sequentially.
	contexts map[uint16]rpcBackend
}

func (v *vconn) signalReady() { v.readyOnce.Do(func() { close(v.inReady) }) }
func (v *vconn) closeResp()   { v.respOnce.Do(func() { close(v.outResp) }) }

// getOrCreate returns the virtual connection for a cookie, creating it on first
// touch. The IN and OUT channels arrive on separate sockets and race, so either
// may create it.
func (h *Handler) getOrCreate(c cookie) *vconn {
	h.mu.Lock()
	defer h.mu.Unlock()
	v := h.conns[c]
	if v == nil {
		v = &vconn{
			outResp:  make(chan []byte, outRespBuffer),
			inReady:  make(chan struct{}),
			contexts: make(map[uint16]rpcBackend),
		}
		h.conns[c] = v
	}
	return v
}

func (h *Handler) remove(c cookie) {
	h.mu.Lock()
	delete(h.conns, c)
	h.mu.Unlock()
}

// ServeHTTP routes the two MS-RPCH channel methods.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "RPC_OUT_DATA":
		h.serveOut(w, r)
	case "RPC_IN_DATA":
		h.serveIn(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// serveOut handles the long-lived OUT channel: it reads CONN/A1, replies with
// CONN/A3, waits for the IN channel, replies with CONN/C2, then streams the
// response PDUs the IN handler produces.
func (h *Handler) serveOut(w http.ResponseWriter, r *http.Request) {
	rc := http.NewResponseController(w)
	if err := rc.SetWriteDeadline(time.Time{}); err != nil {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	a1, err := readPDU(r.Body)
	if err != nil {
		http.Error(w, "bad CONN/A1", http.StatusBadRequest)
		return
	}
	cookies, err := parseConnSetup(a1)
	if err != nil {
		http.Error(w, "bad CONN/A1", http.StatusBadRequest)
		return
	}
	v := h.getOrCreate(cookies.virtualConnection)
	defer h.remove(cookies.virtualConnection)

	w.Header().Set("Content-Type", "application/rpc")
	w.WriteHeader(http.StatusOK)
	if !writeFlush(w, rc, buildConnA3()) {
		return
	}

	select {
	case <-v.inReady:
	case <-r.Context().Done():
		return
	}
	if !writeFlush(w, rc, buildConnC2()) {
		return
	}

	for {
		select {
		case pdu, ok := <-v.outResp:
			if !ok {
				return
			}
			if !writeFlush(w, rc, pdu) {
				return
			}
		case <-r.Context().Done():
			return
		}
	}
}

// serveIn handles the long-lived IN channel: it reads CONN/B1, signals the OUT
// channel, then frames and dispatches the stream of DCERPC PDUs the client
// sends, routing each reply onto the OUT channel.
func (h *Handler) serveIn(w http.ResponseWriter, r *http.Request) {
	rc := http.NewResponseController(w)
	if err := rc.SetReadDeadline(time.Time{}); err != nil {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	email := emsmdb.EmailFromContext(r.Context())
	b1, err := readPDU(r.Body)
	if err != nil {
		http.Error(w, "bad CONN/B1", http.StatusBadRequest)
		return
	}
	cookies, err := parseConnSetup(b1)
	if err != nil {
		http.Error(w, "bad CONN/B1", http.StatusBadRequest)
		return
	}
	v := h.getOrCreate(cookies.virtualConnection)
	defer h.remove(cookies.virtualConnection)
	defer v.closeResp()
	v.signalReady()

	ctx := r.Context()
loop:
	for {
		pdu, rerr := readPDU(r.Body)
		if rerr != nil {
			break
		}
		resp := h.dispatch(pdu, email, v)
		if resp == nil {
			continue
		}
		select {
		case v.outResp <- resp:
		case <-ctx.Done():
			break loop
		}
	}
	// The tunnel result rides the OUT channel; acknowledge the IN request so it
	// completes cleanly.
	w.WriteHeader(http.StatusOK)
}

// dispatch turns one inbound DCERPC PDU into the response PDU to stream back, or
// nil for RTS keepalive/flow-control PDUs that need no reply. v carries the
// per-connection presentation contexts negotiated by BIND/ALTER.
func (h *Handler) dispatch(pdu []byte, email string, v *vconn) []byte {
	pkt, err := dcerpc.Pull(pdu)
	if err != nil {
		return nil
	}
	switch pkt.Type {
	case dcerpc.PktBind:
		return h.handleBind(pkt, v)
	case dcerpc.PktAlter:
		return h.handleAlter(pkt, v)
	case dcerpc.PktRequest:
		return h.handleRequest(pkt, email, v)
	default:
		return nil
	}
}

// negotiateContexts resolves each presentation context a BIND/ALTER offers
// against the registered interfaces, recording the accepted ones on the virtual
// connection so later REQUESTs route by context id. It returns the per-context
// ack results and the secondary-address annotation of the first accepted
// interface (empty when none is accepted).
func (h *Handler) negotiateContexts(contexts []dcerpc.CtxList, v *vconn) (results []dcerpc.AckResult, secAddr string) {
	results = make([]dcerpc.AckResult, 0, len(contexts))
	for _, c := range contexts {
		iface, ok := h.interfaces[c.AbstractSyntax.UUID]
		if !ok {
			results = append(results, dcerpc.AckResult{Result: dcerpc.BindResultProviderReject, Reason: dcerpc.BindReasonAbstractSyntaxUnsupp, Syntax: transferSyntaxNDR32})
			continue
		}
		v.contexts[c.ContextID] = iface.backend
		if secAddr == "" {
			secAddr = iface.secAddr
		}
		results = append(results, dcerpc.AckResult{Result: dcerpc.BindResultAccept, Reason: dcerpc.BindReasonNotSpecified, Syntax: transferSyntaxNDR32})
	}
	return results, secAddr
}

func (h *Handler) handleBind(pkt *dcerpc.Packet, v *vconn) []byte {
	results, secAddr := h.negotiateContexts(pkt.Bind.Contexts, v)
	return dcerpc.EncodeBindAck(pkt.CallID, pkt.Bind.MaxXmitFrag, pkt.Bind.MaxRecvFrag, bindAssocGroup, secAddr, results)
}

func (h *Handler) handleAlter(pkt *dcerpc.Packet, v *vconn) []byte {
	results, _ := h.negotiateContexts(pkt.Bind.Contexts, v)
	return dcerpc.EncodeAlterAck(pkt.CallID, pkt.Bind.MaxXmitFrag, pkt.Bind.MaxRecvFrag, bindAssocGroup, results)
}

func (h *Handler) handleRequest(pkt *dcerpc.Packet, email string, v *vconn) []byte {
	backend := v.contexts[pkt.Request.ContextID]
	if backend == nil {
		return dcerpc.EncodeFault(pkt.CallID, pkt.Request.ContextID, faultUnknownIf)
	}
	resp, ok := backend.HandleRPC(pkt.Request.Opnum, email, pkt.Request.Stub)
	if !ok {
		return dcerpc.EncodeFault(pkt.CallID, pkt.Request.ContextID, faultOpRngError)
	}
	return dcerpc.EncodeResponse(pkt.CallID, pkt.Request.ContextID, resp)
}

// readPDU frames one connection-oriented PDU from a stream: the 16-byte common
// header, then the rest up to the header's fragment length.
func readPDU(r io.Reader) ([]byte, error) {
	hdr := make([]byte, 16)
	if _, err := io.ReadFull(r, hdr); err != nil {
		return nil, err
	}
	fragLen := int(binary.LittleEndian.Uint16(hdr[8:10]))
	if fragLen < 16 || fragLen > maxPDU {
		return nil, fmt.Errorf("rpch: fragment length %d out of range", fragLen)
	}
	pdu := make([]byte, fragLen)
	copy(pdu, hdr)
	if _, err := io.ReadFull(r, pdu[16:]); err != nil {
		return nil, err
	}
	return pdu, nil
}

// writeFlush writes a PDU to the OUT stream and flushes it, reporting whether
// the client connection is still healthy.
func writeFlush(w http.ResponseWriter, rc *http.ResponseController, pdu []byte) bool {
	if _, err := w.Write(pdu); err != nil {
		return false
	}
	return rc.Flush() == nil
}
