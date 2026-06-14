package activesync

import (
	"strconv"

	"github.com/umailserver/umailserver/internal/activesync/wbxml"
)

// marshalSync builds a Sync response for one collection: SyncKey, CollectionId
// and Status, an optional Responses block reporting the per-item status of the
// client's up-sync commands (failures only), an optional MoreAvailable flag, and
// a Commands block holding the encoded server-side Add/Change/Delete operations.
// A non-success status resets the returned SyncKey to "0", which tells the client
// to restart this collection.
func marshalSync(collectionID, status, syncKey string, responses []clientResponse, cmds []syncCommand, more bool) ([]byte, error) {
	if status != syncStatusSuccess {
		syncKey = "0"
	}
	collection := &wbxml.Element{Page: wbxml.PageAirSync, Name: "Collection", Children: []*wbxml.Element{
		{Page: wbxml.PageAirSync, Name: "SyncKey", Text: syncKey},
		{Page: wbxml.PageAirSync, Name: "CollectionId", Text: collectionID},
		{Page: wbxml.PageAirSync, Name: "Status", Text: status},
	}}
	if len(responses) > 0 {
		block := &wbxml.Element{Page: wbxml.PageAirSync, Name: "Responses"}
		for _, r := range responses {
			block.Children = append(block.Children, &wbxml.Element{Page: wbxml.PageAirSync, Name: r.op, Children: []*wbxml.Element{
				{Page: wbxml.PageAirSync, Name: "ServerId", Text: r.serverID},
				{Page: wbxml.PageAirSync, Name: "Status", Text: r.status},
			}})
		}
		collection.Children = append(collection.Children, block)
	}
	if more {
		collection.Children = append(collection.Children, &wbxml.Element{Page: wbxml.PageAirSync, Name: "MoreAvailable"})
	}
	if len(cmds) > 0 {
		commands := &wbxml.Element{Page: wbxml.PageAirSync, Name: "Commands"}
		for _, c := range cmds {
			commands.Children = append(commands.Children, encodeCommand(c))
		}
		collection.Children = append(collection.Children, commands)
	}
	root := &wbxml.Element{Page: wbxml.PageAirSync, Name: "Sync", Children: []*wbxml.Element{
		{Page: wbxml.PageAirSync, Name: "Collections", Children: []*wbxml.Element{collection}},
	}}
	return wbxml.Marshal(root)
}

// encodeCommand encodes one Sync change: a Delete carries only the ServerId,
// while Add/Change carry the ServerId and the message's ApplicationData.
func encodeCommand(c syncCommand) *wbxml.Element {
	if c.op == "Delete" {
		return &wbxml.Element{Page: wbxml.PageAirSync, Name: "Delete", Children: []*wbxml.Element{
			{Page: wbxml.PageAirSync, Name: "ServerId", Text: c.id},
		}}
	}
	return &wbxml.Element{Page: wbxml.PageAirSync, Name: c.op, Children: []*wbxml.Element{
		{Page: wbxml.PageAirSync, Name: "ServerId", Text: c.msg.ServerID},
		{Page: wbxml.PageAirSync, Name: "ApplicationData", Children: applicationData(c.msg)},
	}}
}

// applicationData projects a mail message into its EAS ApplicationData elements:
// the Email-class headers (code page 2) and the AirSyncBase Body (code page 17),
// which carries the message body for protocol version 12.0+.
func applicationData(m SyncMessage) []*wbxml.Element {
	email := func(name, text string) *wbxml.Element {
		return &wbxml.Element{Page: wbxml.PageEmail, Name: name, Text: text}
	}
	read := "0"
	if m.Read {
		read = "1"
	}
	importance := m.Importance
	if importance == "" {
		importance = "1"
	}
	bodyType := m.BodyType
	if bodyType == "" {
		bodyType = "1"
	}
	truncated := "0"
	if m.Truncated {
		truncated = "1"
	}
	return []*wbxml.Element{
		email("Subject", m.Subject),
		email("From", m.From),
		email("To", m.To),
		email("DateReceived", m.DateReceived),
		email("Read", read),
		email("Importance", importance),
		email("MessageClass", "IPM.Note"),
		{Page: wbxml.PageAirSyncBase, Name: "Body", Children: []*wbxml.Element{
			{Page: wbxml.PageAirSyncBase, Name: "Type", Text: bodyType},
			{Page: wbxml.PageAirSyncBase, Name: "EstimatedDataSize", Text: strconv.Itoa(len(m.Body))},
			{Page: wbxml.PageAirSyncBase, Name: "Truncated", Text: truncated},
			{Page: wbxml.PageAirSyncBase, Name: "Data", Text: m.Body},
		}},
	}
}
