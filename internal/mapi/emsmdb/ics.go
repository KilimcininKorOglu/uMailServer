package emsmdb

import "github.com/umailserver/umailserver/internal/mapi/wire"

// Synchronization types (MS-OXCFXICS 2.2.3.2.1.1.1, the RopSynchronizationConfigure
// SynchronizationType byte).
const (
	syncTypeContents  uint8 = 0x1
	syncTypeHierarchy uint8 = 0x2
)

// syncContextObject is a server object bound by RopSynchronizationConfigure: it
// captures the configuration of an ICS synchronization — the folder it runs on, the
// sync type (contents or hierarchy), the sync and send-option flags, and the
// property tags to include — and holds it until RopFastTransferSourceGetBuffer
// streams the changes. The client's prior sync state (the IDSET of ids it already
// holds) is not part of the configuration; it is uploaded separately through the
// RopSynchronizationUploadStateStream ROPs before the buffer is pulled.
type syncContextObject struct {
	mailbox     string
	syncType    uint8
	syncFlags   uint16
	sendOptions uint8
	proptags    []wire.PropTag
	// replicaGUID is the store's stable per-mailbox replica GUID, used for the XIDs
	// (source/change keys) and the IDSET state the download stream carries.
	replicaGUID wire.GUID
	// logon backs a hierarchy sync's folder enumeration (mapping folder names to ids).
	logon *logonObject
	// stream holds the serialized FastTransfer download produced on the first
	// RopFastTransferSourceGetBuffer call; pos is how far it has been drained. produced
	// guards lazy production so the stream is built once and chunked across calls.
	stream   []byte
	pos      int
	produced bool
	// uploadProp/uploadBuf accumulate a client-uploaded state property across the
	// RopSynchronizationUploadStateStream ROPs; seenModSeq is the change high-water
	// parsed from an uploaded CnsetSeen, which the download then treats as the delta
	// baseline (messages with a higher ModSeq are streamed).
	uploadProp wire.PropTag
	uploadBuf  []byte
	seenModSeq uint64
}

// ropSyncConfigure handles RopSynchronizationConfigure (MS-OXCFXICS 2.2.3.2.1.1;
// MS-OXCROPS 2.2.13.1): it configures an ICS download on the folder named by the
// input handle and binds a sync-context object to the request's output handle index
// for the FastTransfer ROPs that follow. The optional restriction filter is read
// past (the download covers the whole folder; restriction filtering at the change
// stage is a later refinement). The response carries no body (the no-body push
// group); the configured context lives at the output handle.
func ropSyncConfigure(c *ropCtx, _ uint8, hindex uint8) {
	ohindex := c.in.Uint8()
	syncType := c.in.Uint8()
	sendOptions := c.in.Uint8()
	syncFlags := c.in.Uint16()
	resSize := int(c.in.Uint16())
	if resSize > 0 {
		// The RESTRICTION is bounded by resSize; skip it as a whole so the trailing
		// extra_flags and property-tag array stay byte-aligned (MS-OXCFXICS 2.2.3.2.1.1).
		c.in.Skip(resSize)
	}
	_ = c.in.Uint32() // extra_flags (FastTransfer extended flags); not used on the download path yet
	proptags := wire.PullPropertyTagArray(c.in)
	if c.in.Err() != nil {
		writeRopError(c.out, RopSyncConfigure, ohindex, ecError)
		return
	}
	if syncType != syncTypeContents && syncType != syncTypeHierarchy {
		writeRopError(c.out, RopSyncConfigure, ohindex, ecError)
		return
	}
	fo, ok := c.objectAt(hindex).(*folderObject)
	if !ok {
		writeRopError(c.out, RopSyncConfigure, ohindex, ecNullObject)
		return
	}
	c.setHandle(ohindex, c.state.alloc(&syncContextObject{
		mailbox:     fo.mailbox,
		syncType:    syncType,
		syncFlags:   syncFlags,
		sendOptions: sendOptions,
		proptags:    proptags,
		replicaGUID: fo.logon.replGUID,
		logon:       fo.logon,
	}))

	out := c.out
	out.Uint8(RopSyncConfigure)
	out.Uint8(ohindex)
	out.Uint32(ecSuccess)
}
