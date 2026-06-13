package emsmdb

import "github.com/umailserver/umailserver/internal/mapi/wire"

// ropSyncUploadStateStreamBegin handles RopSynchronizationUploadStateStreamBegin
// (MS-OXCFXICS 2.2.3.2.4.1; MS-OXCROPS 2.2.13.9.1): it starts uploading one ICS state
// property (which the client has from a prior sync) into the sync context, recording
// which property is incoming and resetting the accumulator. The byte-count argument
// is a size hint; the actual bytes arrive via the Continue ROP. The response carries
// no body.
func ropSyncUploadStateStreamBegin(c *ropCtx, _ uint8, hindex uint8) {
	proptag := wire.PropTag(c.in.Uint32())
	_ = c.in.Uint32() // buffer_size: total size hint
	if c.in.Err() != nil {
		writeRopError(c.out, RopSyncUploadStateStreamBegin, hindex, ecError)
		return
	}
	sc, ok := c.objectAt(hindex).(*syncContextObject)
	if !ok {
		writeRopError(c.out, RopSyncUploadStateStreamBegin, hindex, ecNullObject)
		return
	}
	sc.uploadProp = proptag
	sc.uploadBuf = nil

	out := c.out
	out.Uint8(RopSyncUploadStateStreamBegin)
	out.Uint8(hindex)
	out.Uint32(ecSuccess)
}

// ropSyncUploadStateStreamContinue handles RopSynchronizationUploadStateStreamContinue
// (MS-OXCFXICS 2.2.3.2.4.2): it appends a chunk of the state property's bytes (a
// u32-counted binary) to the accumulator. The response carries no body.
func ropSyncUploadStateStreamContinue(c *ropCtx, _ uint8, hindex uint8) {
	n := c.in.Uint32() // g_bin_ex: a u32 byte count then the bytes
	chunk := c.in.Bytes(int(n))
	if c.in.Err() != nil {
		writeRopError(c.out, RopSyncUploadStateStreamContinue, hindex, ecError)
		return
	}
	sc, ok := c.objectAt(hindex).(*syncContextObject)
	if !ok {
		writeRopError(c.out, RopSyncUploadStateStreamContinue, hindex, ecNullObject)
		return
	}
	sc.uploadBuf = append(sc.uploadBuf, chunk...)

	out := c.out
	out.Uint8(RopSyncUploadStateStreamContinue)
	out.Uint8(hindex)
	out.Uint32(ecSuccess)
}

// ropSyncUploadStateStreamEnd handles RopSynchronizationUploadStateStreamEnd
// (MS-OXCFXICS 2.2.3.2.4.3): it finalizes the uploaded state property. A CnsetSeen
// upload sets the delta baseline — the highest change number the client reports
// seeing — so the next download streams only messages whose ModSeq is higher. Other
// state properties (IdsetGiven and so on) are accepted so the upload chain completes
// but do not change the baseline. The response carries no body.
func ropSyncUploadStateStreamEnd(c *ropCtx, _ uint8, hindex uint8) {
	if c.in.Err() != nil {
		writeRopError(c.out, RopSyncUploadStateStreamEnd, hindex, ecError)
		return
	}
	sc, ok := c.objectAt(hindex).(*syncContextObject)
	if !ok {
		writeRopError(c.out, RopSyncUploadStateStreamEnd, hindex, ecNullObject)
		return
	}
	if sc.uploadProp.ID() == metaTagCnsetSeen.ID() {
		if _, ranges, err := wire.ParseIDSET(sc.uploadBuf); err == nil {
			for _, r := range ranges {
				if r.Hi > sc.seenModSeq {
					sc.seenModSeq = r.Hi
				}
			}
		}
	}
	sc.uploadBuf = nil

	out := c.out
	out.Uint8(RopSyncUploadStateStreamEnd)
	out.Uint8(hindex)
	out.Uint32(ecSuccess)
}
