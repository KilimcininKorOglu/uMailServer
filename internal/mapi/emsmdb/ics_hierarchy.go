package emsmdb

import (
	"math/bits"
	"slices"

	"github.com/umailserver/umailserver/internal/mapi/wire"
	"github.com/umailserver/umailserver/internal/semcore"
)

// ipmSubtreeGC is the global counter of the IPM subtree, the parent of the mail
// folders this hierarchy sync reports.
const ipmSubtreeGC uint64 = 0x09

// folderGC recovers a folder's 48-bit global counter from its folder id, inverting
// makeFID (the id is the byte-reversed gc OR'd with the replica id), so the gc can
// form the folder's source-key GLOBCNT.
func folderGC(fid uint64) uint64 {
	return bits.ReverseBytes64(fid &^ 0xFFFF)
}

// coalesceGCs sorts global counters and merges contiguous ones into the minimal set
// of inclusive ranges a GLOBSET expects, dropping duplicates.
func coalesceGCs(gcs []uint64) []wire.GlobcntRange {
	if len(gcs) == 0 {
		return nil
	}
	sorted := append([]uint64(nil), gcs...)
	slices.Sort(sorted)
	ranges := []wire.GlobcntRange{{Lo: sorted[0], Hi: sorted[0]}}
	for _, g := range sorted[1:] {
		last := &ranges[len(ranges)-1]
		switch g {
		case last.Hi:
			// duplicate; skip
		case last.Hi + 1:
			last.Hi = g
		default:
			ranges = append(ranges, wire.GlobcntRange{Lo: g, Hi: g})
		}
	}
	return ranges
}

// buildHierarchySyncStream serializes a hierarchy-download FastTransfer stream
// (MS-OXCFXICS 2.2.4.1): a folder-change block (INCRSYNCCHG + a folder proplist with
// the folder's source key, its parent's source key, a change key and predecessor
// list, the display name, and the folder id) for each folder, then the ICS state
// block (IdsetGiven over the folder ids) and INCRSYNCEND. Folders carry no native
// per-folder change number, so the whole hierarchy is sent each sync (a full,
// non-incremental folder list) with a minimal CnsetSeen; incremental folder deltas
// are a later refinement.
func buildHierarchySyncStream(folders []folderEntry, replicaGUID wire.GUID) ([]byte, error) {
	p := wire.NewPush(0)
	parentKey := wire.SerializeXID(replicaGUID, ipmSubtreeGC)
	gcs := make([]uint64, 0, len(folders))
	for _, f := range folders {
		gc := folderGC(f.fid)
		gcs = append(gcs, gc)
		sourceKey := wire.SerializeXID(replicaGUID, gc)
		changeKey := wire.SerializeXID(replicaGUID, gc)
		pcl := append([]byte{byte(len(changeKey))}, changeKey...)
		p.Uint32(markerIncrSyncChg)
		props := []wire.TaggedPropertyValue{
			{Tag: wire.PidTagSourceKey, Value: sourceKey},
			{Tag: wire.PidTagParentSourceKey, Value: parentKey},
			{Tag: wire.PidTagChangeKey, Value: changeKey},
			{Tag: wire.PidTagPredecessorChangeList, Value: pcl},
			{Tag: wire.PidTagDisplayName, Value: semcore.DisplayNameFromStorageName(f.name)},
			{Tag: wire.PidTagFolderId, Value: f.fid},
		}
		for _, pv := range props {
			if err := wire.PushFastTransferPropval(p, pv.Tag, pv.Value); err != nil {
				return nil, err
			}
		}
	}

	p.Uint32(markerIncrSyncStateBegin)
	idsetGiven := wire.SerializeIDSET(replicaGUID, coalesceGCs(gcs))
	if err := wire.PushFastTransferPropval(p, metaTagIdsetGiven1, idsetGiven); err != nil {
		return nil, err
	}
	cnsetSeen := wire.SerializeIDSET(replicaGUID, []wire.GlobcntRange{{Lo: 1, Hi: 1}})
	if err := wire.PushFastTransferPropval(p, metaTagCnsetSeen, cnsetSeen); err != nil {
		return nil, err
	}
	p.Uint32(markerIncrSyncStateEnd)
	p.Uint32(markerIncrSyncEnd)
	return p.Bytes(), nil
}

// buildHierarchyDownload enumerates the mailbox's mail folders (the same set the
// hierarchy table exposes — every non-hidden mailbox under the IPM subtree) and
// serializes them as a hierarchy-download stream.
func (c *ropCtx) buildHierarchyDownload(sc *syncContextObject) ([]byte, error) {
	names, err := c.store.ListMailboxes(c.email)
	if err != nil {
		return nil, err
	}
	folders := make([]folderEntry, 0, len(names))
	for _, name := range names {
		if semcore.IsClientHiddenFolderName(name) {
			continue
		}
		folders = append(folders, folderEntry{name: name, fid: sc.logon.folderIDForName(name)})
	}
	return buildHierarchySyncStream(folders, sc.replicaGUID)
}
