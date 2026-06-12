package emsmdb

import (
	"crypto/sha256"
	"encoding/binary"
	"time"

	"github.com/umailserver/umailserver/internal/mapi/wire"
)

// Special folders returned in the RopLogon response, in the fixed order the
// private-mailbox response requires (MS-OXCSTOR 2.2.1.1.3).
const (
	sfRoot = iota
	sfDeferredAction
	sfSpoolerQueue
	sfIPMSubtree
	sfInbox
	sfOutbox
	sfSentItems
	sfDeletedItems
	sfCommonViews
	sfSchedule
	sfFinder
	sfViews
	sfShortcuts
	numSpecialFolders
)

// privateReplID is the replica id of a private mailbox store.
const privateReplID uint16 = 1

// RopLogon response flags (MS-OXCSTOR 2.2.1.1.3): the reserved bit must be set,
// and the mailbox owner holds owner and send-as rights.
const logonResponseFlags uint8 = 0x01 | 0x02 | 0x04

// logonObject is the per-session store logon created by RopLogon. It owns the
// mailbox identity and the special-folder id assignment that later ROPs resolve.
type logonObject struct {
	email       string
	replID      uint16
	replGUID    wire.GUID
	mailboxGUID wire.GUID
	folderIDs   [numSpecialFolders]uint64
}

// makeFID builds a folder id from a replica id and a 48-bit global counter
// (MS-OXCDATA 2.2.1.1): the replica id occupies the low 16 bits.
func makeFID(replID uint16, gc uint64) uint64 {
	return uint64(replID) | (gc << 16)
}

// guidFromBytes packs the first 16 bytes of b into a GUID whose serialized form
// equals those bytes, giving a stable identifier derived from a hash.
func guidFromBytes(b []byte) wire.GUID {
	var g wire.GUID
	g.TimeLow = binary.LittleEndian.Uint32(b[0:4])
	g.TimeMid = binary.LittleEndian.Uint16(b[4:6])
	g.TimeHiAndVersion = binary.LittleEndian.Uint16(b[6:8])
	copy(g.ClockSeq[:], b[8:10])
	copy(g.Node[:], b[10:16])
	return g
}

// derivedGUID returns a deterministic GUID for a mailbox-scoped label.
func derivedGUID(label, email string) wire.GUID {
	h := sha256.Sum256([]byte(label + ":" + email))
	return guidFromBytes(h[:])
}

// newLogon builds the logon state for a mailbox. The special folders get stable
// global counters so a client that caches ids across sessions still resolves
// them.
func newLogon(email string) *logonObject {
	lo := &logonObject{
		email:       email,
		replID:      privateReplID,
		mailboxGUID: derivedGUID("mailbox", email),
		replGUID:    derivedGUID("replica", email),
	}
	for i := range lo.folderIDs {
		lo.folderIDs[i] = makeFID(lo.replID, uint64(i+1))
	}
	return lo
}

// writeLogonTime serializes the server logon time (MS-OXCSTOR 2.2.1.1.3):
// seconds, minutes, hour, day of week, day, month (each a byte) then a 16-bit
// year.
func writeLogonTime(out *wire.Push, t time.Time) {
	out.Uint8(uint8(t.Second()))
	out.Uint8(uint8(t.Minute()))
	out.Uint8(uint8(t.Hour()))
	out.Uint8(uint8(t.Weekday()))
	out.Uint8(uint8(t.Day()))
	out.Uint8(uint8(t.Month()))
	out.Uint16(uint16(t.Year()))
}

// ropLogon handles RopLogon for a private mailbox (MS-OXCSTOR 2.2.1.1): it
// creates the logon object, binds it to the output handle, and returns the
// store identity plus the special-folder ids.
func ropLogon(c *ropCtx, _ uint8, hindex uint8) {
	// LOGON_REQUEST: logon flags, open flags, store state, then a counted essdn.
	logonFlags := c.in.Uint8()
	_ = c.in.Uint32() // open flags
	storeStat := c.in.Uint32()
	if n := int(c.in.Uint16()); n > 0 {
		c.in.Bytes(n) // essdn (mailbox DN); the session email already identifies us
	}
	if c.in.Err() != nil {
		writeRopError(c.out, RopLogon, hindex, ecError)
		return
	}

	lo := newLogon(c.email)
	c.setHandle(hindex, c.state.alloc(lo))

	out := c.out
	out.Uint8(RopLogon)
	out.Uint8(hindex)
	out.Uint32(ecSuccess)
	out.Uint8(logonFlags)
	for _, fid := range lo.folderIDs {
		out.Uint64(fid)
	}
	out.Uint8(logonResponseFlags)
	out.GUID(lo.mailboxGUID)
	out.Uint16(lo.replID)
	out.GUID(lo.replGUID)
	writeLogonTime(out, time.Now().UTC())
	out.Uint64(0)         // gwart_time (per-replica watermark; unused online)
	out.Uint32(storeStat) // echo the store state
}
