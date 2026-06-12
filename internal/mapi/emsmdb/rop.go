package emsmdb

import "github.com/umailserver/umailserver/internal/mapi/wire"

// ROP operation ids (MS-OXCROPS 2.2). Only the operations on the online-mode
// mailbox path are named; handlers are registered for those that are
// implemented.
const (
	RopRelease               uint8 = 0x01
	RopOpenFolder            uint8 = 0x02
	RopOpenMessage           uint8 = 0x03
	RopGetHierarchyTable     uint8 = 0x04
	RopGetContentsTable      uint8 = 0x05
	RopGetPropertiesSpecific uint8 = 0x07
	RopGetPropertiesAll      uint8 = 0x08
	RopSetColumns            uint8 = 0x12
	RopSortTable             uint8 = 0x13
	RopQueryRows             uint8 = 0x15
	RopGetReceiveFolder      uint8 = 0x27
	RopGetPropertyIdsByNames uint8 = 0x56
	RopGetStoreState         uint8 = 0x7B
	RopLogon                 uint8 = 0xFE
)

// sessionObjects is the per-session server-object table (the LOGMAP): handle
// values map to the logon, folder, table, and message objects a client opens.
type sessionObjects struct {
	objects    map[uint32]any
	nextHandle uint32
}

func newSessionObjects() *sessionObjects {
	return &sessionObjects{objects: make(map[uint32]any)}
}

// alloc stores an object and returns its new handle value.
func (s *sessionObjects) alloc(o any) uint32 {
	h := s.nextHandle
	s.nextHandle++
	s.objects[h] = o
	return h
}

func (s *sessionObjects) release(h uint32) { delete(s.objects, h) }

// stateFor returns the session's object table, creating it on first use. The
// caller holds the session lock for the duration of an Execute.
func stateFor(sess *Session) *sessionObjects {
	st, ok := sess.Logon.(*sessionObjects)
	if !ok {
		st = newSessionObjects()
		sess.Logon = st
	}
	return st
}

// Processor implements the ROP dispatcher: it parses a ROP request list and
// produces the ROP response list against a session's object table. Store-backed
// operations read the canonical mailbox store.
type Processor struct {
	store Store
}

// NewProcessor returns a ROP processor backed by the canonical mailbox store.
func NewProcessor(store Store) *Processor { return &Processor{store: store} }

var _ ROPDispatcher = (*Processor)(nil)

// ropCtx carries the per-ROP execution state.
type ropCtx struct {
	email   string
	store   Store
	state   *sessionObjects
	handles []uint32
	in      *wire.Pull
	out     *wire.Push
}

// setHandle writes a server-object handle value to the given handle-table index,
// growing the table (padding with the empty handle 0xFFFFFFFF) when the index is
// beyond its current length.
func (c *ropCtx) setHandle(index uint8, value uint32) {
	for len(c.handles) <= int(index) {
		c.handles = append(c.handles, 0xFFFFFFFF)
	}
	c.handles[index] = value
}

// objectAt returns the server object referenced by the handle-table index, or
// nil when the index is out of range or holds the empty handle.
func (c *ropCtx) objectAt(index uint8) any {
	if int(index) >= len(c.handles) {
		return nil
	}
	h := c.handles[index]
	if h == 0xFFFFFFFF {
		return nil
	}
	return c.state.objects[h]
}

// ropHandler reads a ROP request body, executes it, and writes its response.
type ropHandler func(c *ropCtx, logonID, hindex uint8)

var ropHandlers = map[uint8]ropHandler{
	RopRelease:          ropRelease,
	RopLogon:            ropLogon,
	RopGetReceiveFolder: ropGetReceiveFolder,
	RopOpenFolder:       ropOpenFolder,
	RopGetContentsTable: ropGetContentsTable,
}

// Dispatch parses ropData as a chained ROP request list and returns the encoded
// ROP response list and the (possibly updated) server-object handle table. An
// unimplemented ROP yields a standard failure response and stops further
// parsing, since ROP requests have no length prefix to skip past.
func (p *Processor) Dispatch(sess *Session, ropData []byte, handlesIn []uint32, _ int) ([]byte, []uint32) {
	st := stateFor(sess)
	handles := append([]uint32(nil), handlesIn...)
	in := wire.NewPull(ropData, wire.FlagUTF16)
	out := wire.NewPush(wire.FlagUTF16)

	for in.Remaining() > 0 {
		ropID := in.Uint8()
		logonID := in.Uint8()
		hindex := in.Uint8()
		if in.Err() != nil {
			break
		}
		h := ropHandlers[ropID]
		if h == nil {
			writeRopError(out, ropID, hindex, ecNotImplemented)
			break
		}
		c := &ropCtx{email: sess.Email, store: p.store, state: st, handles: handles, in: in, out: out}
		h(c, logonID, hindex)
		handles = c.handles
		if in.Err() != nil {
			break
		}
	}
	return out.Bytes(), handles
}

// writeRopError emits the standard ROP failure response: the op id, the handle
// index, and the result code (MS-OXCROPS 2.2.1.2).
func writeRopError(out *wire.Push, ropID, hindex uint8, code uint32) {
	out.Uint8(ropID)
	out.Uint8(hindex)
	out.Uint32(code)
}

// ropRelease frees the server object referenced by the handle index. RopRelease
// has no response (MS-OXCROPS 2.2.15).
func ropRelease(c *ropCtx, _ uint8, hindex uint8) {
	if int(hindex) < len(c.handles) {
		c.state.release(c.handles[hindex])
	}
}
