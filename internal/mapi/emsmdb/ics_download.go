package emsmdb

import (
	"time"

	"github.com/umailserver/umailserver/internal/mapi/wire"
	"github.com/umailserver/umailserver/internal/storage"
)

// FastTransfer stream markers (MS-OXCFXICS 2.2.4; each is a PtypLong proptag written
// as a little-endian u32). Only the markers a contents-download stream emits are
// named.
const (
	markerIncrSyncChg        uint32 = 0x40120003
	markerIncrSyncMessage    uint32 = 0x40150003
	markerIncrSyncEnd        uint32 = 0x40140003
	markerIncrSyncStateBegin uint32 = 0x403A0003
	markerIncrSyncStateEnd   uint32 = 0x403B0003
)

// ICS state meta-properties (MS-OXCFXICS 2.2.1.3) carried in the state block; their
// value is a serialized IDSET framed as PtypBinary. MetaTagIdsetGiven1 is the
// PtypBinary form (id 0x4017) that carries the actual id bytes.
const (
	metaTagIdsetGiven1 wire.PropTag = 0x40170102
	metaTagCnsetSeen   wire.PropTag = 0x67960102
)

// FastTransfer transfer-status values (MS-OXCFXICS 2.2.3.1.1.5.2).
const (
	transferStatusPartial uint16 = 0x0001
	transferStatusDone    uint16 = 0x0003
)

// syncMessage is one message to emit in a contents-download stream: its uid (whose
// GLOBCNT is the source-key id and the IdsetGiven member), its modseq (the change
// number, the change-key id and the CnsetSeen member), its modification time, and the
// resolved property values the client requested.
type syncMessage struct {
	uid     uint32
	modseq  uint64
	lastMod time.Time
	props   []wire.TaggedPropertyValue
}

// buildContentsSyncStream serializes a contents-download FastTransfer stream
// (MS-OXCFXICS 2.2.4.1): a change block (INCRSYNCCHG + change header + INCRSYNCMESSAGE
// + message proplist) for each message in streamed, then the ICS state block
// (INCRSYNCSTATEBEGIN + IdsetGiven over allUIDs + CnsetSeen over [1, maxModSeq] +
// INCRSYNCSTATEEND) and the terminating INCRSYNCEND. streamed is the set of changed
// messages to send (every message for a full sync, only the newer ones for a delta);
// allUIDs and maxModSeq describe the FULL current folder state the client should hold
// afterward, independent of which messages were streamed. It is a pure function so
// the wire shape can be asserted directly.
func buildContentsSyncStream(streamed []syncMessage, allUIDs []uint32, maxModSeq uint64, replicaGUID wire.GUID) ([]byte, error) {
	p := wire.NewPush(0)
	for _, m := range streamed {
		p.Uint32(markerIncrSyncChg)
		if err := writeChangeHeader(p, m, replicaGUID); err != nil {
			return nil, err
		}
		p.Uint32(markerIncrSyncMessage)
		for _, pv := range m.props {
			if err := wire.PushFastTransferPropval(p, pv.Tag, pv.Value); err != nil {
				return nil, err
			}
		}
	}

	p.Uint32(markerIncrSyncStateBegin)
	idsetGiven := wire.SerializeIDSET(replicaGUID, coalesceUIDs(allUIDs))
	if err := wire.PushFastTransferPropval(p, metaTagIdsetGiven1, idsetGiven); err != nil {
		return nil, err
	}
	var cnRanges []wire.GlobcntRange
	if maxModSeq > 0 {
		cnRanges = []wire.GlobcntRange{{Lo: 1, Hi: maxModSeq}}
	}
	cnsetSeen := wire.SerializeIDSET(replicaGUID, cnRanges)
	if err := wire.PushFastTransferPropval(p, metaTagCnsetSeen, cnsetSeen); err != nil {
		return nil, err
	}
	p.Uint32(markerIncrSyncStateEnd)
	p.Uint32(markerIncrSyncEnd)
	return p.Bytes(), nil
}

// writeChangeHeader emits the per-message change header proplist (MS-OXCFXICS
// 2.2.4.1.1.1 / the five always-present properties): the source key (a stable XID of
// the store replica GUID + the message uid), the last-modification time, the change
// key (the same replica GUID + the modseq as the change number), a predecessor change
// list carrying that single change key, and the associated (FAI) flag. The header is
// a bare proplist, delimited by the surrounding markers.
func writeChangeHeader(p *wire.Push, m syncMessage, replicaGUID wire.GUID) error {
	sourceKey := wire.SerializeXID(replicaGUID, uint64(m.uid))
	changeKey := wire.SerializeXID(replicaGUID, m.modseq)
	// A fresh predecessor change list is one sized XID: a 1-byte length then the XID.
	pcl := append([]byte{byte(len(changeKey))}, changeKey...)
	header := []wire.TaggedPropertyValue{
		{Tag: wire.PidTagSourceKey, Value: sourceKey},
		{Tag: wire.PidTagLastModificationTime, Value: wire.FileTimeFromTime(m.lastMod)},
		{Tag: wire.PidTagChangeKey, Value: changeKey},
		{Tag: wire.PidTagPredecessorChangeList, Value: pcl},
		{Tag: wire.PidTagAssociated, Value: false},
	}
	for _, pv := range header {
		if err := wire.PushFastTransferPropval(p, pv.Tag, pv.Value); err != nil {
			return err
		}
	}
	return nil
}

// coalesceUIDs turns an ascending uid list into the minimal set of inclusive ranges
// (contiguous uids merge into one range), the compact form a GLOBSET expects.
func coalesceUIDs(uids []uint32) []wire.GlobcntRange {
	if len(uids) == 0 {
		return nil
	}
	ranges := []wire.GlobcntRange{{Lo: uint64(uids[0]), Hi: uint64(uids[0])}}
	for _, u := range uids[1:] {
		last := &ranges[len(ranges)-1]
		if uint64(u) == last.Hi+1 {
			last.Hi = uint64(u)
		} else {
			ranges = append(ranges, wire.GlobcntRange{Lo: uint64(u), Hi: uint64(u)})
		}
	}
	return ranges
}

// gatherSyncProps resolves the requested property tags for one message, reusing the
// read-path mapping (messageProperty for scalars, the body store for the plain/HTML
// bodies). An empty request falls back to the default scalar set. Unavailable
// properties are simply omitted from the message proplist.
func (c *ropCtx) gatherSyncProps(meta *storage.MessageMetadata, tags []wire.PropTag) []wire.TaggedPropertyValue {
	if len(tags) == 0 {
		tags = messageAllTags
	}
	var raw []byte
	readBody := false
	vals := make([]wire.TaggedPropertyValue, 0, len(tags))
	for _, t := range tags {
		switch t {
		case wire.PidTagBody, wire.PidTagHtml:
			if c.body != nil && !readBody {
				readBody = true
				if r, rerr := c.body.ReadMessage(c.email, meta.MessageID); rerr == nil {
					raw = r
				}
			}
			if t == wire.PidTagBody && raw != nil {
				vals = append(vals, wire.TaggedPropertyValue{Tag: t, Value: extractMessageBody(raw)})
			} else if t == wire.PidTagHtml {
				if h := extractHTMLBody(raw); h != nil {
					vals = append(vals, wire.TaggedPropertyValue{Tag: t, Value: h})
				}
			}
		default:
			if v, ok := messageProperty(t, meta); ok {
				vals = append(vals, wire.TaggedPropertyValue{Tag: t, Value: v})
			}
		}
	}
	return vals
}

// buildContentsDownload gathers the folder's messages from the canonical store and
// serializes them as a contents-download stream. A message that vanished from the
// snapshot between the uid scan and the metadata read is skipped, as in the table
// ROPs.
func (c *ropCtx) buildContentsDownload(sc *syncContextObject) ([]byte, error) {
	uids, err := c.store.GetMessageUIDs(c.email, sc.mailbox)
	if err != nil {
		return nil, err
	}
	// allUIDs is the full current membership for IdsetGiven; maxModSeq is the current
	// change high-water for CnsetSeen — both describe the post-sync state regardless of
	// which messages are streamed. streamed carries only the messages whose change
	// number (ModSeq) exceeds what the client reported seeing (sc.seenModSeq), so an
	// initial sync (seenModSeq 0) streams everything and a follow-up streams only the
	// changes.
	allUIDs := make([]uint32, 0, len(uids))
	streamed := make([]syncMessage, 0, len(uids))
	var maxModSeq uint64
	for _, uid := range uids {
		meta, merr := c.store.GetMessageMetadata(c.email, sc.mailbox, uid)
		if merr != nil {
			continue
		}
		allUIDs = append(allUIDs, uid)
		if meta.ModSeq > maxModSeq {
			maxModSeq = meta.ModSeq
		}
		if meta.ModSeq > sc.seenModSeq {
			streamed = append(streamed, syncMessage{
				uid:     uid,
				modseq:  meta.ModSeq,
				lastMod: meta.InternalDate,
				props:   c.gatherSyncProps(meta, sc.proptags),
			})
		}
	}
	return buildContentsSyncStream(streamed, allUIDs, maxModSeq, sc.replicaGUID)
}

// ropFastTransferSourceGetBuffer handles RopFastTransferSourceGetBuffer (MS-OXCFXICS
// 2.2.3.1.1.5; MS-OXCROPS 2.2.13.2): it streams the configured ICS download in
// chunks. The full stream is produced once on the first call and drained across
// calls; each response reports PARTIAL while bytes remain and DONE on the last chunk.
// The client's buffer size and the u16 transfer-data count bound each chunk.
func ropFastTransferSourceGetBuffer(c *ropCtx, _ uint8, hindex uint8) {
	bufferSize := c.in.Uint16()
	maxBufferSize := uint16(0)
	if bufferSize == 0xBABE { // the sentinel that introduces an explicit maximum
		maxBufferSize = c.in.Uint16()
	}
	if c.in.Err() != nil {
		writeRopError(c.out, RopFastTransferSourceGetBuffer, hindex, ecError)
		return
	}
	sc, ok := c.objectAt(hindex).(*syncContextObject)
	if !ok {
		writeRopError(c.out, RopFastTransferSourceGetBuffer, hindex, ecNullObject)
		return
	}
	if sc.syncType != syncTypeContents {
		writeRopError(c.out, RopFastTransferSourceGetBuffer, hindex, ecNotImplemented)
		return
	}
	if !sc.produced {
		stream, err := c.buildContentsDownload(sc)
		if err != nil {
			writeRopError(c.out, RopFastTransferSourceGetBuffer, hindex, ecError)
			return
		}
		sc.stream = stream
		sc.produced = true
	}

	limit := int(bufferSize)
	if bufferSize == 0xBABE {
		limit = int(maxBufferSize)
	}
	const wireMax = 0xFFFF // transfer_data is u16-counted, so a chunk cannot exceed this
	if limit <= 0 || limit > wireMax {
		limit = wireMax
	}
	chunkLen := min(len(sc.stream)-sc.pos, limit)
	chunk := sc.stream[sc.pos : sc.pos+chunkLen]
	sc.pos += chunkLen
	status := transferStatusDone
	if sc.pos < len(sc.stream) {
		status = transferStatusPartial
	}

	out := c.out
	out.Uint8(RopFastTransferSourceGetBuffer)
	out.Uint8(hindex)
	out.Uint32(ecSuccess)
	out.Uint16(status)
	out.Uint16(0) // in_progress_count: progress reporting not used
	out.Uint16(0) // total_step_count
	out.Uint8(0)  // reserved
	out.Uint16(uint16(chunkLen))
	out.Raw(chunk)
}
