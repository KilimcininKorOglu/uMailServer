package activesync

import (
	"errors"

	"github.com/umailserver/umailserver/internal/activesync/wbxml"
)

// ItemOperations status codes (MS-ASCMD 2.2.3.177.8): 1 = success; 3 = a server
// error fetching the item (used here when the item is gone or unreadable).
const (
	itemOpStatusSuccess = "1"
	itemOpStatusError   = "3"
)

// handleItemOperations answers the ItemOperations command's Fetch operations
// (MS-ASCMD): each Fetch names a collection and item, and the response returns the
// item's full, untruncated content under Properties. The container Status is 1;
// each Fetch carries its own status so a missing item degrades per-item rather
// than failing the whole batch.
func (s *Server) handleItemOperations(ctx *Context) ([]byte, error) {
	if s.mail == nil {
		return nil, errors.New("activesync: mail source not configured")
	}
	root, err := wbxml.Unmarshal(ctx.Body)
	if err != nil {
		return nil, err
	}
	resp := &wbxml.Element{Page: wbxml.PageItemOperations, Name: "ItemOperations", Children: []*wbxml.Element{
		{Page: wbxml.PageItemOperations, Name: "Status", Text: itemOpStatusSuccess},
	}}
	block := &wbxml.Element{Page: wbxml.PageItemOperations, Name: "Response"}
	for _, f := range root.Children {
		if f.Name != "Fetch" {
			continue
		}
		// A mailbox search hit is named by its opaque LongId (Store=Mailbox); a
		// Sync item by CollectionId+ServerId. Both resolve to the same blob read.
		if longID := textOf(f.Sub("LongId")); longID != "" {
			block.Children = append(block.Children, s.fetchByLongID(ctx.Email, longID))
			continue
		}
		block.Children = append(block.Children, s.fetchItem(ctx.Email, textOf(f.Sub("CollectionId")), textOf(f.Sub("ServerId"))))
	}
	resp.Children = append(resp.Children, block)
	return wbxml.Marshal(resp)
}

// fetchItem builds one Fetch response: the item's full content under Properties
// on success, or just the server id with an error status when it cannot be read.
// CollectionId/ServerId/Class are AirSync (page 0) elements nested in the
// ItemOperations structure.
func (s *Server) fetchItem(email, collectionID, serverID string) *wbxml.Element {
	fetch := &wbxml.Element{Page: wbxml.PageItemOperations, Name: "Fetch"}
	msg, err := s.mail.Fetch(email, collectionID, serverID)
	if err != nil || msg == nil {
		fetch.Children = []*wbxml.Element{
			{Page: wbxml.PageItemOperations, Name: "Status", Text: itemOpStatusError},
			{Page: wbxml.PageAirSync, Name: "ServerId", Text: serverID},
		}
		return fetch
	}
	fetch.Children = []*wbxml.Element{
		{Page: wbxml.PageItemOperations, Name: "Status", Text: itemOpStatusSuccess},
		{Page: wbxml.PageAirSync, Name: "ServerId", Text: serverID},
		{Page: wbxml.PageAirSync, Name: "CollectionId", Text: collectionID},
		{Page: wbxml.PageAirSync, Name: "Class", Text: "Email"},
		{Page: wbxml.PageItemOperations, Name: "Properties", Children: applicationData(*msg)},
	}
	return fetch
}

// fetchByLongID answers an ItemOperations Fetch that names a mailbox search hit
// by its opaque LongId (Store=Mailbox). It decodes the LongId back to the mail
// identity the Search command packed into it and reuses the same blob read as a
// ServerId fetch; the response echoes the LongId so the client matches it to its
// request. A LongId this server never issued, or a message gone since the search,
// degrades to the per-item error status.
func (s *Server) fetchByLongID(email, longID string) *wbxml.Element {
	fetch := &wbxml.Element{Page: wbxml.PageItemOperations, Name: "Fetch"}
	collectionID, serverID, ok := decodeLongID(longID)
	var msg *SyncMessage
	if ok {
		var err error
		if msg, err = s.mail.Fetch(email, collectionID, serverID); err != nil {
			msg = nil
		}
	}
	if msg == nil {
		fetch.Children = []*wbxml.Element{
			{Page: wbxml.PageItemOperations, Name: "Status", Text: itemOpStatusError},
			{Page: wbxml.PageSearch, Name: "LongId", Text: longID},
		}
		return fetch
	}
	fetch.Children = []*wbxml.Element{
		{Page: wbxml.PageItemOperations, Name: "Status", Text: itemOpStatusSuccess},
		{Page: wbxml.PageSearch, Name: "LongId", Text: longID},
		{Page: wbxml.PageAirSync, Name: "Class", Text: "Email"},
		{Page: wbxml.PageItemOperations, Name: "Properties", Children: applicationData(*msg)},
	}
	return fetch
}
