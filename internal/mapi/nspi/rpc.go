package nspi

import (
	"crypto/rand"
	"encoding/binary"
	"sort"
	"strings"
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
	opNspiUpdateStat       uint16 = 2
	opNspiQueryRows        uint16 = 3
	opNspiSeekEntries      uint16 = 4
	opNspiResortRestriction uint16 = 6
	opNspiDNToMId          uint16 = 7
	opNspiGetPropList      uint16 = 8
	opNspiGetProps         uint16 = 9
	opNspiCompareMIds      uint16 = 10
	opNspiModProps         uint16 = 11
	opNspiGetSpecialTable  uint16 = 12
	opNspiGetTemplateInfo  uint16 = 13
	opNspiModLinkAtt       uint16 = 14
	opNspiQueryColumns     uint16 = 16
	opNspiResolveNames     uint16 = 19
	opNspiResolveNamesW    uint16 = 20
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
	case opNspiUpdateStat:
		return s.updateStat(stub), true
	case opNspiQueryRows:
		return s.queryRows(stub), true
	case opNspiSeekEntries:
		return s.seekEntries(stub), true
	case opNspiGetPropList:
		return s.getPropList(stub), true
	case opNspiCompareMIds:
		return s.compareMIds(stub), true
	case opNspiQueryColumns:
		return s.queryColumns(stub), true
	case opNspiDNToMId:
		return s.dnToMID(stub), true
	case opNspiResolveNames:
		return s.resolveNames(stub), true
	case opNspiResolveNamesW:
		return s.resolveNamesW(stub), true
	case opNspiResortRestriction:
		return s.resortRestriction(stub), true
	case opNspiGetTemplateInfo:
		return s.getTemplateInfo(stub), true
	case opNspiGetProps:
		return s.getProps(stub), true
	case opNspiModProps:
		return s.refuseWrite(stub), true
	case opNspiGetSpecialTable:
		return s.getSpecialTable(stub), true
	case opNspiModLinkAtt:
		return s.refuseWrite(stub), true
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

// queryRows answers NspiQueryRows (MS-OXNSPI 3.1.4.1.8): it returns a block of
// address-book rows, either for an explicit minimal-id list or by reading forward
// from the table cursor the STAT block carries. The request is the context
// handle, dwFlags, the STAT block, an explicit minimal-id count and its optional
// [unique] table, the maximum row count, then an optional [unique] property-tag
// array (an absent array selects the default column set). The response echoes the
// advanced STAT, then a result-gated [unique] row set. Cursor enumeration reads
// the same stable GAL order every position-based operation shares, advancing
// NumPos and CurrentRec so the next call resumes where this one stopped.
func (s *RPCServer) queryRows(stub []byte) []byte {
	p := ndr.NewPull(stub)
	handle := readNSPIHandle(p)
	p.Uint32() // dwFlags
	stat := pullStatNDR(p)
	tableCount := p.Uint32()
	var explicit []uint32
	if p.Uint32() != 0 { // explicit minimal-id table [unique] referent
		size := p.ULong()
		if size != tableCount {
			p.Fault()
		}
		explicit = make([]uint32, 0, tableCount)
		for range tableCount {
			if p.Err() != nil {
				break
			}
			explicit = append(explicit, p.Uint32())
		}
	}
	count := int(p.Uint32())
	var cols []wire.PropTag
	if p.Uint32() != 0 { // pPropTags [unique] referent
		cols = pullProptagArrayNDR(p)
	}
	if len(cols) == 0 {
		cols = defaultColumns
	}
	out := ndr.NewPush()
	if p.Err() != nil || !s.hasSession(handle) {
		pushStatNDR(out, stat)
		out.UniquePtr(false) // NULL row set
		out.Uint32(ecError)
		return out.Bytes()
	}
	gal := sortedGAL(s.dir)
	stat.TotalRec = uint32(len(gal))
	var entries []DirectoryEntry
	if len(explicit) > 0 {
		for _, mid := range explicit {
			if idx := midIndex(mid, len(gal)); idx >= 0 {
				entries = append(entries, gal[idx])
			}
		}
	} else {
		start := int(stat.NumPos)
		if start < 0 || start > len(gal) {
			start = len(gal)
		}
		end := min(start+count, len(gal))
		entries = gal[start:end]
		stat.NumPos = uint32(end)
		if end >= len(gal) {
			stat.CurrentRec = midEnd
		} else {
			stat.CurrentRec = entryMid(end)
		}
	}
	rows := make([][]wire.TaggedPropertyValue, len(entries))
	for i, e := range entries {
		rows[i] = galRowValues(e, cols)
	}
	pushStatNDR(out, stat)
	out.UniquePtr(true) // row set present (possibly empty) on success
	pushRowSet(out, rows)
	out.Uint32(ecSuccess)
	return out.Bytes()
}

// galRowValues builds the property values for one GAL entry over the requested
// columns, marking an absent column PtypErrorCode(ecNotFound). It reads the same
// entryProperty mapping the MAPI/HTTP surface uses, so a row reports identical
// values on either transport.
func galRowValues(entry DirectoryEntry, cols []wire.PropTag) []wire.TaggedPropertyValue {
	vals := make([]wire.TaggedPropertyValue, len(cols))
	for i, c := range cols {
		if v, ok := entryProperty(c, entry); ok {
			vals[i] = wire.TaggedPropertyValue{Tag: c, Value: v}
			continue
		}
		vals[i] = wire.TaggedPropertyValue{Tag: wire.MakeTag(c.ID(), wire.PtError), Value: ecNotFound}
	}
	return vals
}

// getProps answers NspiGetProps (MS-OXNSPI 3.1.4.1.7): it returns the requested
// properties of the address-book entry the state block's current record points
// at, as a single property row. The request carries the context handle, dwFlags,
// the STAT block, then an optional [unique] property-tag array; an absent array
// selects the default column set. A requested property the entry lacks comes back
// as a PtypErrorCode value and the call result becomes ecWarnWithErrors; with the
// default column set those absent properties are dropped and the result stays
// ecSuccess. The row is encoded only when the result is a success or a warning,
// matching the result-gated [unique] pointer the transfer syntax requires.
func (s *RPCServer) getProps(stub []byte) []byte {
	p := ndr.NewPull(stub)
	handle := readNSPIHandle(p)
	p.Uint32() // dwFlags
	stat := pullStatNDR(p)
	var tags []wire.PropTag
	explicit := false
	if p.Uint32() != 0 { // pPropTags [unique] referent
		tags = pullProptagArrayNDR(p)
		explicit = true
	}
	out := ndr.NewPush()
	if p.Err() != nil || !s.hasSession(handle) {
		out.UniquePtr(false) // NULL property row
		out.Uint32(ecError)
		return out.Bytes()
	}
	if !explicit {
		tags = defaultColumns
	}
	vals, result := buildGetPropsRow(sortedGAL(s.dir), stat, tags, explicit)
	if result != ecSuccess && result != ecWarnWithErrors {
		out.UniquePtr(false) // NULL property row
		out.Uint32(result)
		return out.Bytes()
	}
	out.UniquePtr(true) // pRow present
	pushPropRowHeader(out, vals)
	pushPropRowContent(out, vals)
	out.Uint32(result)
	return out.Bytes()
}

// buildGetPropsRow resolves the GAL entry the state block points at and builds
// its property values over the requested tags (MS-OXNSPI 3.1.4.1.7). An absent
// property is marked PtypErrorCode(ecNotFound); the result is ecWarnWithErrors
// when the position resolves to no entry, or when an explicit column request
// leaves any such marker in the row. With the default column set the absent-
// property markers are dropped and the result stays ecSuccess. It reads the same
// entryProperty mapping the MAPI/HTTP surface uses, so both report identical
// values for an entry.
func buildGetPropsRow(gal []DirectoryEntry, stat Stat, tags []wire.PropTag, explicit bool) (vals []wire.TaggedPropertyValue, result uint32) {
	idx := midIndex(stat.CurrentRec, len(gal))
	if idx < 0 {
		// No entry at this position: report every requested property absent.
		vals = make([]wire.TaggedPropertyValue, len(tags))
		for i, t := range tags {
			vals[i] = wire.TaggedPropertyValue{Tag: wire.MakeTag(t.ID(), wire.PtError), Value: ecNotFound}
		}
		return vals, ecWarnWithErrors
	}
	entry := gal[idx]
	vals = make([]wire.TaggedPropertyValue, 0, len(tags))
	missing := false
	for _, t := range tags {
		if v, ok := entryProperty(t, entry); ok {
			vals = append(vals, wire.TaggedPropertyValue{Tag: t, Value: v})
			continue
		}
		missing = true
		if explicit {
			vals = append(vals, wire.TaggedPropertyValue{Tag: wire.MakeTag(t.ID(), wire.PtError), Value: ecNotFound})
		}
		// Default column set: drop an absent property rather than flag it.
	}
	if explicit && missing {
		return vals, ecWarnWithErrors
	}
	return vals, ecSuccess
}

// pullProptagArrayNDR reads an NDR LPROPTAG_ARRAY (MS-OXNSPI 2.3.2): a conformant
// 32-bit-counted property-tag array. The conformant max_count carries one
// sentinel slot beyond the live count (max_count == cValues+1) per the transfer
// syntax, the offset is zero, and the varying length equals cValues. A layout
// that violates any of these invariants faults the stream so a malformed request
// is rejected rather than silently mis-parsed.
func pullProptagArrayNDR(p *ndr.Pull) []wire.PropTag {
	vals := pullU32ArrayNDR(p)
	tags := make([]wire.PropTag, len(vals))
	for i, v := range vals {
		tags[i] = wire.PropTag(v)
	}
	return tags
}

// pullU32ArrayNDR reads the NDR conformant-varying uint32 array (MS-OXNSPI 2.3.2)
// shared by the property-tag and minimal-id lists: the conformant max_count
// carries one sentinel slot beyond the live count (max_count == cValues+1), the
// offset is zero, and the varying length equals cValues. A layout that violates
// any invariant faults the stream so a malformed request is rejected.
func pullU32ArrayNDR(p *ndr.Pull) []uint32 {
	size := p.ULong()
	p.Align(4)
	cvalues := p.Uint32()
	offset := p.ULong()
	length := p.ULong()
	if cvalues > 100001 || offset != 0 || length > size || size != cvalues+1 || length != cvalues {
		p.Fault()
		return nil
	}
	vals := make([]uint32, 0, length)
	for range length {
		if p.Err() != nil {
			break
		}
		vals = append(vals, p.Uint32())
	}
	return vals
}

// pushProptagArrayNDR writes an NDR LPROPTAG_ARRAY (MS-OXNSPI 2.3.2).
func pushProptagArrayNDR(out *ndr.Push, tags []wire.PropTag) {
	vals := make([]uint32, len(tags))
	for i, t := range tags {
		vals[i] = uint32(t)
	}
	pushU32ArrayNDR(out, vals)
}

// pushU32ArrayNDR writes the NDR conformant-varying uint32 array shared by the
// property-tag and minimal-id lists: the conformant max_count carries one
// sentinel slot beyond the live count, the offset is zero, and the varying length
// equals the count.
func pushU32ArrayNDR(out *ndr.Push, vals []uint32) {
	out.ULong(uint32(len(vals)) + 1) // max_count = count + 1 (sentinel slot)
	out.Align(4)
	out.Uint32(uint32(len(vals))) // count
	out.ULong(0)                  // offset
	out.ULong(uint32(len(vals)))  // length
	for _, v := range vals {
		out.Uint32(v)
	}
}

// pullStringsArrayNDR reads an NDR array of [unique] 8-bit string pointers
// (MS-OXNSPI 2.3.x): a conformant size, a matching count, one referent per slot,
// then each present slot's conformant-varying string. A NULL slot decodes to an
// empty string; the trailing NUL each string carries is trimmed.
func pullStringsArrayNDR(p *ndr.Pull) []string {
	present, ok := pullStringArrayHeaderNDR(p)
	if !ok {
		return nil
	}
	out := make([]string, len(present))
	for i := range out {
		if !present[i] {
			continue
		}
		length, ok := pullStringElementHeaderNDR(p)
		if !ok {
			return nil
		}
		out[i] = strings.TrimRight(string(p.Bytes(int(length))), "\x00")
	}
	return out
}

// pullWStringsArrayNDR reads the UTF-16 form of pullStringsArrayNDR: each present
// slot's conformant-varying string is length code units of little-endian UTF-16.
func pullWStringsArrayNDR(p *ndr.Pull) []string {
	present, ok := pullStringArrayHeaderNDR(p)
	if !ok {
		return nil
	}
	out := make([]string, len(present))
	for i := range out {
		if !present[i] {
			continue
		}
		length, ok := pullStringElementHeaderNDR(p)
		if !ok {
			return nil
		}
		out[i] = utf16LEToString(p.Bytes(int(length) * 2))
	}
	return out
}

// pullStringArrayHeaderNDR reads the common header of a [unique]-string array: the
// conformant size, the matching count, and the per-slot referents. It reports the
// presence flags and whether the header was well-formed.
func pullStringArrayHeaderNDR(p *ndr.Pull) (present []bool, ok bool) {
	size := p.ULong()
	p.Align(4)
	count := p.Uint32()
	if count != size || count > 100000 {
		p.Fault()
		return nil, false
	}
	present = make([]bool, count)
	for i := range present {
		present[i] = p.Uint32() != 0
	}
	return present, true
}

// pullStringElementHeaderNDR reads a conformant-varying string's max_count, offset
// and length, returning the length (in characters) and whether it was well-formed.
func pullStringElementHeaderNDR(p *ndr.Pull) (length uint32, ok bool) {
	size := p.ULong()
	offset := p.ULong()
	length = p.ULong()
	if offset != 0 || length > size {
		p.Fault()
		return 0, false
	}
	return length, true
}

// utf16LEToString decodes little-endian UTF-16 and trims the trailing NUL.
func utf16LEToString(b []byte) string {
	units := make([]uint16, 0, len(b)/2)
	for i := 0; i+1 < len(b); i += 2 {
		units = append(units, binary.LittleEndian.Uint16(b[i:]))
	}
	for len(units) > 0 && units[len(units)-1] == 0 {
		units = units[:len(units)-1]
	}
	return string(utf16.Decode(units))
}

// updateStat answers NspiUpdateStat (MS-OXNSPI 3.1.4.1.9): it advances the table
// cursor by the state block's signed delta and returns the updated state. When
// the client supplies the optional delta pointer the response reports the actual
// number of positions moved.
func (s *RPCServer) updateStat(stub []byte) []byte {
	p := ndr.NewPull(stub)
	handle := readNSPIHandle(p)
	p.Uint32() // reserved
	stat := pullStatNDR(p)
	deltaRequested := false
	if p.Uint32() != 0 { // [in, out, unique] pdelta referent
		p.Uint32() // inbound delta value, unused
		deltaRequested = true
	}
	out := ndr.NewPush()
	if p.Err() != nil || !s.hasSession(handle) {
		pushStatNDR(out, stat)
		out.UniquePtr(false)
		out.Uint32(ecError)
		return out.Bytes()
	}
	total := uint32(len(sortedGAL(s.dir)))
	initRow := positionInList(stat, total)
	row := initRow
	if stat.Delta < 0 && uint32(-stat.Delta) >= row {
		row = 0
	} else {
		row = uint32(int32(row) + stat.Delta)
	}
	if row >= total {
		row = total
		stat.CurrentRec = midEnd
	} else {
		stat.CurrentRec = entryMid(int(row))
	}
	delta := int32(row) - int32(initRow)
	stat.Delta = 0
	stat.NumPos = row
	stat.TotalRec = total
	pushStatNDR(out, stat)
	out.UniquePtr(deltaRequested)
	if deltaRequested {
		out.Uint32(uint32(delta))
	}
	out.Uint32(ecSuccess)
	return out.Bytes()
}

// compareMIds answers NspiCompareMIds (MS-OXNSPI 3.1.4.1.10): it reports the
// relative table order of two minimal ids — negative when the second precedes the
// first, positive when it follows, zero when equal. An id absent from the table
// yields an error.
func (s *RPCServer) compareMIds(stub []byte) []byte {
	p := ndr.NewPull(stub)
	handle := readNSPIHandle(p)
	p.Uint32() // reserved
	pullStatNDR(p)
	mid1 := p.Uint32()
	mid2 := p.Uint32()
	out := ndr.NewPush()
	if p.Err() != nil || !s.hasSession(handle) {
		out.Uint32(0) // comparison
		out.Uint32(ecError)
		return out.Bytes()
	}
	gal := sortedGAL(s.dir)
	idx1 := midIndex(mid1, len(gal))
	idx2 := midIndex(mid2, len(gal))
	if idx1 < 0 || idx2 < 0 {
		out.Uint32(0)
		out.Uint32(ecError)
		return out.Bytes()
	}
	var cmp int32
	switch {
	case idx2 < idx1:
		cmp = -1
	case idx2 > idx1:
		cmp = 1
	}
	out.Uint32(uint32(cmp))
	out.Uint32(ecSuccess)
	return out.Bytes()
}

// getPropList answers NspiGetPropList (MS-OXNSPI 3.1.4.1.6): it returns the
// property tags the entry named by the minimal id carries values for. An id that
// names no entry yields an error and a NULL list.
func (s *RPCServer) getPropList(stub []byte) []byte {
	p := ndr.NewPull(stub)
	handle := readNSPIHandle(p)
	p.Uint32() // dwFlags
	mid := p.Uint32()
	p.Uint32() // code page
	out := ndr.NewPush()
	if p.Err() != nil || !s.hasSession(handle) || midIndex(mid, len(sortedGAL(s.dir))) < 0 {
		out.UniquePtr(false)
		out.Uint32(ecError)
		return out.Bytes()
	}
	out.UniquePtr(true)
	pushProptagArrayNDR(out, availableEntryTags)
	out.Uint32(ecSuccess)
	return out.Bytes()
}

// queryColumns answers NspiQueryColumns (MS-OXNSPI 3.1.4.1.5): it returns the set
// of property tags the address book can serve as columns.
func (s *RPCServer) queryColumns(stub []byte) []byte {
	p := ndr.NewPull(stub)
	handle := readNSPIHandle(p)
	p.Uint32() // reserved
	p.Uint32() // dwFlags
	out := ndr.NewPush()
	if p.Err() != nil || !s.hasSession(handle) {
		out.UniquePtr(false)
		out.Uint32(ecError)
		return out.Bytes()
	}
	out.UniquePtr(true)
	pushProptagArrayNDR(out, defaultColumns)
	out.Uint32(ecSuccess)
	return out.Bytes()
}

// dnToMID answers NspiDNToMId (MS-OXNSPI 3.1.4.1.13): it maps each requested
// distinguished name to the minimal id of the GAL entry that carries it, or to
// zero when no entry matches. Names are 8-bit strings.
func (s *RPCServer) dnToMID(stub []byte) []byte {
	p := ndr.NewPull(stub)
	handle := readNSPIHandle(p)
	p.Uint32() // reserved
	dns := pullStringsArrayNDR(p)
	out := ndr.NewPush()
	if p.Err() != nil || !s.hasSession(handle) {
		out.UniquePtr(false) // NULL minimal-id list
		out.Uint32(ecError)
		return out.Bytes()
	}
	gal := sortedGAL(s.dir)
	mids := make([]uint32, len(dns))
	for i, dn := range dns {
		mids[i] = nameUnresolved // zero when the name resolves to no entry
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
	out.UniquePtr(true)
	pushU32ArrayNDR(out, mids)
	out.Uint32(ecSuccess)
	return out.Bytes()
}

// resolveNames answers NspiResolveNames (MS-OXNSPI 3.1.4.1.16, 8-bit names) and
// resolveNamesW answers NspiResolveNamesW (MS-OXNSPI 3.1.4.1.17, Unicode names):
// each requested name is resolved against the GAL by ambiguous-name resolution.
func (s *RPCServer) resolveNames(stub []byte) []byte  { return s.resolve(stub, false) }
func (s *RPCServer) resolveNamesW(stub []byte) []byte { return s.resolve(stub, true) }

// resolve performs the shared ambiguous-name resolution for the 8-bit and Unicode
// variants: every requested name yields a status code (unresolved, ambiguous, or
// resolved), and each uniquely-resolved name contributes a row to the matched row
// set. The response is a result-gated minimal-id status array followed by the row
// set, mirroring the canonical MAPI/HTTP ResolveNamesW path.
func (s *RPCServer) resolve(stub []byte, wide bool) []byte {
	p := ndr.NewPull(stub)
	handle := readNSPIHandle(p)
	p.Uint32() // reserved
	pullStatNDR(p)
	var cols []wire.PropTag
	if p.Uint32() != 0 { // pPropTags [unique] referent
		cols = pullProptagArrayNDR(p)
	}
	var names []string
	if wide {
		names = pullWStringsArrayNDR(p)
	} else {
		names = pullStringsArrayNDR(p)
	}
	if len(cols) == 0 {
		cols = defaultColumns
	}
	out := ndr.NewPush()
	if p.Err() != nil || s.dir == nil || !s.hasSession(handle) {
		out.UniquePtr(false) // NULL minimal-id list
		out.UniquePtr(false) // NULL row set
		out.Uint32(ecError)
		return out.Bytes()
	}
	mids := make([]uint32, 0, len(names))
	var rows [][]wire.TaggedPropertyValue
	for _, name := range names {
		key := strings.TrimSpace(stripAddressType(name))
		if key == "" {
			mids = append(mids, nameUnresolved)
			continue
		}
		switch matches := s.dir.ResolveGAL(key); len(matches) {
		case 0:
			mids = append(mids, nameUnresolved)
		case 1:
			mids = append(mids, nameResolved)
			rows = append(rows, galRowValues(matches[0], cols))
		default:
			mids = append(mids, nameAmbiguous)
		}
	}
	out.UniquePtr(true)
	pushU32ArrayNDR(out, mids)
	out.UniquePtr(true) // row set present (possibly empty)
	pushRowSet(out, rows)
	out.Uint32(ecSuccess)
	return out.Bytes()
}

// seekEntries answers NspiSeekEntries (MS-OXNSPI 3.1.4.1.9): it positions the
// table cursor at the first entry whose display name is at or after the requested
// target and returns that row. The table is display-name sorted, so the seek is a
// lower bound; the optional explicit minimal-id table narrows the search to those
// entries. The target must be a display-name string under the display-name sort,
// matching the canonical MAPI/HTTP behavior.
func (s *RPCServer) seekEntries(stub []byte) []byte {
	p := ndr.NewPull(stub)
	handle := readNSPIHandle(p)
	p.Uint32() // reserved
	stat := pullStatNDR(p)
	target := pullPropValueNDR(p)
	var explicit []uint32
	if p.Uint32() != 0 { // [unique] explicit minimal-id table
		explicit = pullU32ArrayNDR(p)
	}
	var cols []wire.PropTag
	if p.Uint32() != 0 { // [unique] proptag array
		cols = pullProptagArrayNDR(p)
	}
	if len(cols) == 0 {
		cols = defaultColumns
	}
	out := ndr.NewPush()
	targetName, isStr := target.Value.(string)
	if p.Err() != nil || !s.hasSession(handle) || !isStr ||
		stat.SortType != sortTypeDisplayName || target.Tag.ID() != wire.PidTagDisplayName.ID() {
		pushStatNDR(out, stat)
		out.UniquePtr(false) // NULL row set
		out.Uint32(ecError)
		return out.Bytes()
	}
	gal := sortedGAL(s.dir)
	targetLower := strings.ToLower(targetName)
	seekPos := -1
	if len(explicit) > 0 {
		for _, mid := range explicit {
			if idx := midIndex(mid, len(gal)); idx >= 0 && strings.ToLower(gal[idx].DisplayName) >= targetLower {
				seekPos = idx
				break
			}
		}
	} else {
		for i := range gal {
			if strings.ToLower(gal[i].DisplayName) >= targetLower {
				seekPos = i
				break
			}
		}
	}
	if seekPos < 0 {
		pushStatNDR(out, stat)
		out.UniquePtr(false)
		out.Uint32(ecNotFound)
		return out.Bytes()
	}
	stat.NumPos = uint32(seekPos)
	stat.CurrentRec = entryMid(seekPos)
	stat.TotalRec = uint32(len(gal))
	pushStatNDR(out, stat)
	out.UniquePtr(true)
	pushRowSet(out, [][]wire.TaggedPropertyValue{galRowValues(gal[seekPos], cols)})
	out.Uint32(ecSuccess)
	return out.Bytes()
}

// pullPropValueNDR reads an NSPI property value (MS-OXNSPI 2.3.3): the property
// tag, a reserved field, the value union's type discriminant (which must echo the
// tag's type), then the value. It decodes the scalar, string and binary types the
// address-book requests carry; an unhandled type faults the stream so the request
// is rejected rather than mis-parsed.
func pullPropValueNDR(p *ndr.Pull) wire.TaggedPropertyValue {
	p.Align(4)
	tag := wire.PropTag(p.Uint32())
	p.Uint32()        // ulReserved
	typ := p.Uint32() // value-union type discriminant
	if uint16(typ) != uint16(tag.Type()) {
		p.Fault()
		return wire.TaggedPropertyValue{Tag: tag}
	}
	var val any
	var sref, bref uint32
	switch wire.PropType(uint16(typ)) {
	case wire.PtShort:
		val = uint32(p.Uint16())
	case wire.PtLong, wire.PtError:
		val = p.Uint32()
	case wire.PtBoolean:
		val = p.Uint8() != 0
	case wire.PtString8, wire.PtUnicode:
		sref = p.Uint32() // deferred-string referent
	case wire.PtBinary:
		p.Uint32()        // cb
		bref = p.Uint32() // deferred-binary referent
	default:
		p.Fault()
		return wire.TaggedPropertyValue{Tag: tag}
	}
	switch wire.PropType(uint16(typ)) {
	case wire.PtString8:
		if sref != 0 {
			if length, ok := pullStringElementHeaderNDR(p); ok {
				val = strings.TrimRight(string(p.Bytes(int(length))), "\x00")
			}
		}
	case wire.PtUnicode:
		if sref != 0 {
			if length, ok := pullStringElementHeaderNDR(p); ok {
				val = utf16LEToString(p.Bytes(int(length) * 2))
			}
		}
	case wire.PtBinary:
		if bref != 0 {
			val = p.Bytes(int(p.ULong()))
		}
	}
	return wire.TaggedPropertyValue{Tag: tag, Value: val}
}

// resortRestriction answers NspiResortRestriction (MS-OXNSPI 3.1.4.1.12): it
// re-sorts a client-supplied minimal-id list into the table's display-name order,
// dropping ids absent from the table, and resets the cursor to the start.
func (s *RPCServer) resortRestriction(stub []byte) []byte {
	p := ndr.NewPull(stub)
	handle := readNSPIHandle(p)
	p.Uint32() // reserved
	stat := pullStatNDR(p)
	inmids := pullU32ArrayNDR(p)
	if p.Uint32() != 0 { // [in, out, unique] poutmids: inbound value ignored
		pullU32ArrayNDR(p)
	}
	out := ndr.NewPush()
	if p.Err() != nil || !s.hasSession(handle) {
		pushStatNDR(out, stat)
		out.UniquePtr(false)
		out.Uint32(ecError)
		return out.Bytes()
	}
	gal := sortedGAL(s.dir)
	outmids := make([]uint32, 0, len(inmids))
	for _, mid := range inmids {
		if midIndex(mid, len(gal)) >= 0 {
			outmids = append(outmids, mid)
		}
	}
	sort.SliceStable(outmids, func(i, j int) bool {
		return midIndex(outmids[i], len(gal)) < midIndex(outmids[j], len(gal))
	})
	stat.TotalRec = uint32(len(outmids))
	stat.NumPos = 0
	stat.CurrentRec = midBeginningOfTable
	pushStatNDR(out, stat)
	out.UniquePtr(true)
	pushU32ArrayNDR(out, outmids)
	out.Uint32(ecSuccess)
	return out.Bytes()
}

// getTemplateInfo answers NspiGetTemplateInfo (MS-OXNSPI 3.1.4.1.18): no custom
// display templates are served, so the response carries no template row and the
// client falls back to its built-in templates.
func (s *RPCServer) getTemplateInfo(stub []byte) []byte {
	p := ndr.NewPull(stub)
	handle := readNSPIHandle(p)
	p.Uint32() // dwFlags
	p.Uint32() // template type
	if p.Uint32() != 0 { // [unique] pDN referent
		if length, ok := pullStringElementHeaderNDR(p); ok {
			p.Bytes(int(length)) // distinguished name, unused
		}
	}
	p.Uint32() // code page
	p.Uint32() // locale id
	out := ndr.NewPush()
	if p.Err() != nil || !s.hasSession(handle) {
		out.UniquePtr(false)
		out.Uint32(ecError)
		return out.Bytes()
	}
	out.UniquePtr(false) // no template row
	out.Uint32(ecSuccess)
	return out.Bytes()
}

// refuseWrite answers the NSPI write operations NspiModProps (MS-OXNSPI
// 3.1.4.1.11) and NspiModLinkAtt (MS-OXNSPI 3.1.4.1.14): the address book is a
// read-only projection of the canonical directory, so a bound session is told the
// write is unsupported (MAPI_E_NO_SUPPORT) and an unbound one gets an error. Both
// responses carry only the result code. The handle is the first field of either
// request, so reading it is enough to validate the session.
func (s *RPCServer) refuseWrite(stub []byte) []byte {
	p := ndr.NewPull(stub)
	handle := readNSPIHandle(p)
	out := ndr.NewPush()
	if p.Err() != nil || !s.hasSession(handle) {
		out.Uint32(ecError)
		return out.Bytes()
	}
	out.Uint32(ecNotSupported)
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

// pushStatNDR writes a STAT block to an NDR stream (MS-OXNSPI 2.3.7), the mirror
// of pullStatNDR: nine 4-byte fields, 4-aligned. Operations that advance the
// table cursor echo the updated block so the client resumes from it.
func pushStatNDR(out *ndr.Push, s Stat) {
	out.Align(4)
	out.Uint32(s.SortType)
	out.Uint32(s.ContainerID)
	out.Uint32(s.CurrentRec)
	out.Uint32(uint32(s.Delta))
	out.Uint32(s.NumPos)
	out.Uint32(s.TotalRec)
	out.Uint32(s.CodePage)
	out.Uint32(s.TemplateLocale)
	out.Uint32(s.SortLocale)
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

// pushPropValueHeader writes a property value's NDR header (MS-OXNSPI 2.3.3
// PropertyValue_r): the property tag, a reserved field, the value union's type
// discriminant, then either the inline scalar or the unique-pointer referent for
// deferred data (strings, binaries). The reserved field and the discriminant are
// distinct uint32s — the discriminant repeats the tag's type and a receiver
// validates the two agree — so all three precede the value. Only the property
// types the served rows use are encoded.
func pushPropValueHeader(out *ndr.Push, pv wire.TaggedPropertyValue) {
	pv = normalizePropValue(pv)
	out.Align(4)
	out.Uint32(uint32(pv.Tag))         // ulPropTag
	out.Uint32(0)                      // ulReserved
	out.Uint32(uint32(pv.Tag.Type()))  // value-union type discriminant
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
	pv = normalizePropValue(pv)
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

// normalizePropValue degrades a property whose type this NDR codec does not
// encode to a PtypErrorCode(ecNotFound) marker, keeping the value codec total
// over every property the directory can produce. The address book serves the
// long, error, boolean, 8-bit-string, Unicode-string and binary value types;
// emsmdb32 likewise refuses the wider floating-point and 64-bit types over RPC
// (MS-OXNSPI 2.3.3), so an unexpected type yields an "unavailable" marker rather
// than desynchronizing the two-pass row stream. Both encoding passes call this
// with the same value, so the header and content always agree on the type.
func normalizePropValue(pv wire.TaggedPropertyValue) wire.TaggedPropertyValue {
	switch pv.Tag.Type() {
	case wire.PtLong, wire.PtError, wire.PtBoolean, wire.PtString8, wire.PtUnicode, wire.PtBinary:
		return pv
	default:
		return wire.TaggedPropertyValue{Tag: wire.MakeTag(pv.Tag.ID(), wire.PtError), Value: ecNotFound}
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
