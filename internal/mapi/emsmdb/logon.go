package emsmdb

import (
	"crypto/sha256"
	"encoding/binary"
	"math/bits"
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

// sfNone marks a folder object that is not one of the special folders (a custom
// user folder resolved through the logon's folder registry).
const sfNone = -1

// customFolderGCBase is the first global counter handed to a custom (non-special)
// folder. It sits well above the special folders' counters (<= 0x1d) so the two
// id ranges never collide.
const customFolderGCBase uint64 = 0x100000

// Replica ids for a private mailbox store. fidReplID is embedded in the low 16
// bits of every folder and message id (MS-OXCDATA 2.2.1.1); logonReplID is the
// store's current replica reported in the Logon response, paired with the
// replica guid (MS-OXCSTOR 2.2.1.1.3). The two namespaces are independent.
const (
	fidReplID   uint16 = 1
	logonReplID uint16 = 5
)

// RopLogon response flags (MS-OXCSTOR 2.2.1.1.3): the reserved bit (0x01) is
// required, and the mailbox owner holds owner (0x02) and send-as (0x04) rights.
const logonResponseFlags uint8 = 0x01 | 0x02 | 0x04

// specialFolderGC holds the well-known 48-bit global counter of each special
// folder, in the fixed order the private-mailbox Logon response requires
// (MS-OXCSTOR 2.2.1.1.3). These are the canonical Exchange folder ids, not a
// sequential assignment: the client caches them and uses them verbatim in later
// ROPs, so they must stay stable across sessions.
var specialFolderGC = [numSpecialFolders]uint64{
	sfRoot:           0x01,
	sfDeferredAction: 0x02,
	sfSpoolerQueue:   0x03,
	sfIPMSubtree:     0x09,
	sfInbox:          0x0d,
	sfOutbox:         0x0c,
	sfSentItems:      0x0a,
	sfDeletedItems:   0x0b,
	sfCommonViews:    0x07,
	sfSchedule:       0x08,
	sfFinder:         0x05,
	sfViews:          0x06,
	sfShortcuts:      0x04,
}

// logonObject is the per-session store logon created by RopLogon. It owns the
// mailbox identity, the special-folder id assignment, and the registry of custom
// folder ids handed out while enumerating the hierarchy, which later ROPs resolve.
type logonObject struct {
	email        string
	replID       uint16
	replGUID     wire.GUID
	mailboxGUID  wire.GUID
	folderIDs    [numSpecialFolders]uint64
	customFIDs   map[string]uint64 // IMAP-canonical name -> folder id, for stable reuse
	folderNames  map[uint64]string // folder id -> IMAP-canonical name, for resolution
	nextCustomGC uint64            // next custom global counter to allocate
}

// makeFID builds a folder or message id from a replica id and a 48-bit global
// counter (MS-OXCDATA 2.2.1.1, 2.2.1.2). On the wire an id is a 2-byte
// little-endian replica id followed by a 6-byte big-endian global counter; since
// the id is later serialized as a little-endian uint64, the global counter is
// byte-reversed here so its bytes land big-endian after serialization.
func makeFID(replID uint16, gc uint64) uint64 {
	return bits.ReverseBytes64(gc) | uint64(replID)
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
		replID:      fidReplID,
		mailboxGUID: derivedGUID("mailbox", email),
		replGUID:    derivedGUID("replica", email),
		customFIDs:  map[string]uint64{},
		folderNames: map[uint64]string{},
	}
	for i := range lo.folderIDs {
		lo.folderIDs[i] = makeFID(lo.replID, specialFolderGC[i])
	}
	return lo
}

// folderIDForName returns the stable folder id for an IMAP-canonical mailbox
// name. A backed special folder (Inbox, Sent, Trash) keeps its well-known id;
// any other folder is assigned the next custom global counter on first use and
// registered so RopOpenFolder can resolve it later in the session.
func (lo *logonObject) folderIDForName(name string) uint64 {
	if slot := specialSlotForName(name); slot >= 0 {
		return lo.folderIDs[slot]
	}
	if fid, ok := lo.customFIDs[name]; ok {
		return fid
	}
	fid := makeFID(lo.replID, customFolderGCBase+lo.nextCustomGC)
	lo.nextCustomGC++
	lo.customFIDs[name] = fid
	lo.folderNames[fid] = name
	return fid
}

// resolveFolder maps a folder id to its mailbox name and special slot. A special
// folder yields its IMAP-canonical mailbox name (empty for the structural
// folders); a registered custom folder yields its name with slot sfNone. ok is
// false for an id the logon has never handed out.
func (lo *logonObject) resolveFolder(fid uint64) (mailbox string, special int, ok bool) {
	if slot := lo.specialIndex(fid); slot >= 0 {
		return storageFolderName(slot), slot, true
	}
	if name, ok := lo.folderNames[fid]; ok {
		return name, sfNone, true
	}
	return "", sfNone, false
}

// specialIndex returns the special-folder slot a folder id maps to, or -1 when
// the id is not one of the mailbox's well-known folders.
func (lo *logonObject) specialIndex(folderID uint64) int {
	for i, fid := range lo.folderIDs {
		if fid == folderID {
			return i
		}
	}
	return -1
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
	_ = c.in.Uint32() // client store state (the response reports the server's)
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
	out.Uint16(logonReplID)
	out.GUID(lo.replGUID)
	now := time.Now().UTC()
	writeLogonTime(out, now)
	out.Uint64(wire.FileTimeFromTime(now)) // gwart time: server logon timestamp
	out.Uint32(0)                          // store state: no special flags set
}
