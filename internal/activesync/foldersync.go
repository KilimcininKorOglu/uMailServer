package activesync

import (
	"errors"
	"strconv"

	"github.com/umailserver/umailserver/internal/activesync/wbxml"
)

// EAS FolderHierarchy folder types (MS-ASCMD 2.2.3.170.3 Type): the well-known
// default folders plus the generic user-folder type.
const (
	FolderTypeGeneric  = "1"
	FolderTypeInbox    = "2"
	FolderTypeDrafts   = "3"
	FolderTypeDeleted  = "4"
	FolderTypeSent     = "5"
	FolderTypeOutbox   = "6"
	FolderTypeTasks    = "7"
	FolderTypeCalendar = "8"
	FolderTypeContacts = "9"
	FolderTypeNotes    = "10"
	FolderTypeUserMail = "12"
)

// FolderSync status codes (MS-ASCMD 2.2.3.167.2): 1 = success, 9 = the client's
// SyncKey is invalid and it must restart the hierarchy sync from 0.
const (
	folderStatusSuccess        = "1"
	folderStatusInvalidSyncKey = "9"
)

// hierarchyCollection is the collection id under which a device's FolderSync
// (hierarchy) watermark is stored — the mailbox level, distinct from a per-
// folder item-sync collection.
const hierarchyCollection = ""

// Folder is one mailbox folder projected into the EAS hierarchy: a stable
// ServerID, the ParentID ("0" for a top-level folder), the display name and the
// EAS folder Type.
type Folder struct {
	ServerID    string
	ParentID    string
	DisplayName string
	Type        string
}

// FolderSource lists a mailbox's folders for FolderSync.
type FolderSource interface {
	Folders(email string) ([]Folder, error)
}

// SyncState persists a per-(email, collection, device) opaque sync watermark.
// FolderSync uses the hierarchy collection; per-folder item syncs use the folder
// id. The API layer adapts this onto semcore's canonical SyncStateStore.
type SyncState interface {
	GetSyncState(email, collection, deviceID string) (watermark string, err error)
	PutSyncState(email, collection, deviceID, watermark string) error
}

// handleFolderSync answers the FolderSync command (MS-ASCMD): SyncKey 0 returns
// the full folder hierarchy and assigns key "1"; a request echoing the current
// key reports no changes (the default hierarchy is static until folder create/
// delete lands); any other key is rejected with Status 9 so the client restarts.
func (s *Server) handleFolderSync(ctx *Context) ([]byte, error) {
	if s.folders == nil || s.sync == nil {
		return nil, errors.New("activesync: folder source or sync state not configured")
	}
	deviceID := ctx.Request.URL.Query().Get("DeviceId")

	root, err := wbxml.Unmarshal(ctx.Body)
	if err != nil {
		return nil, err
	}
	reqKey := ""
	if sk := root.Sub("SyncKey"); sk != nil {
		reqKey = sk.Text
	}

	if reqKey == "0" {
		folders, err := s.folders.Folders(ctx.Email)
		if err != nil {
			return nil, err
		}
		const initialKey = "1"
		if err := s.sync.PutSyncState(ctx.Email, hierarchyCollection, deviceID, initialKey); err != nil {
			return nil, err
		}
		return marshalFolderSync(folderStatusSuccess, initialKey, folders)
	}

	stored, err := s.sync.GetSyncState(ctx.Email, hierarchyCollection, deviceID)
	if err != nil || stored == "" || reqKey != stored {
		return marshalFolderSync(folderStatusInvalidSyncKey, "", nil)
	}
	// Key matches: the hierarchy is unchanged, so report zero changes.
	return marshalFolderSync(folderStatusSuccess, stored, nil)
}

// marshalFolderSync builds a FolderSync response. On success it carries the
// SyncKey and a Changes block (Count plus an Add per folder); a non-success
// status is returned as a bare Status, which tells the client to resync from 0.
func marshalFolderSync(status, syncKey string, adds []Folder) ([]byte, error) {
	root := &wbxml.Element{Page: wbxml.PageFolderHierarchy, Name: "FolderSync", Children: []*wbxml.Element{
		{Page: wbxml.PageFolderHierarchy, Name: "Status", Text: status},
	}}
	if status == folderStatusSuccess {
		changes := &wbxml.Element{Page: wbxml.PageFolderHierarchy, Name: "Changes", Children: []*wbxml.Element{
			{Page: wbxml.PageFolderHierarchy, Name: "Count", Text: strconv.Itoa(len(adds))},
		}}
		for _, f := range adds {
			changes.Children = append(changes.Children, &wbxml.Element{Page: wbxml.PageFolderHierarchy, Name: "Add", Children: []*wbxml.Element{
				{Page: wbxml.PageFolderHierarchy, Name: "ServerId", Text: f.ServerID},
				{Page: wbxml.PageFolderHierarchy, Name: "ParentId", Text: f.ParentID},
				{Page: wbxml.PageFolderHierarchy, Name: "DisplayName", Text: f.DisplayName},
				{Page: wbxml.PageFolderHierarchy, Name: "Type", Text: f.Type},
			}})
		}
		root.Children = append(root.Children,
			&wbxml.Element{Page: wbxml.PageFolderHierarchy, Name: "SyncKey", Text: syncKey},
			changes,
		)
	}
	return wbxml.Marshal(root)
}
