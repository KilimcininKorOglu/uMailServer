package emsmdb

import (
	"time"

	"github.com/umailserver/umailserver/internal/mailappend"
	"github.com/umailserver/umailserver/internal/mapi/wire"
	"github.com/umailserver/umailserver/internal/msg"
	"github.com/umailserver/umailserver/internal/semcore"
)

// msgFlagUnsent marks a message that has not been submitted for delivery
// (MS-OXCMSG 2.2.1.6, PidTagMessageFlags MSGFLAG_UNSENT). A message created
// through the write ROPs defaults to unsent — a draft — until it is submitted.
const msgFlagUnsent uint32 = 0x00000008

// messageWriteState is the in-flight property buffer of a message opened for
// creation by RopCreateMessage. The write ROPs accumulate properties here until
// RopSaveChangesMessage converts them to an RFC 5322 message and commits it to
// the canonical store through the shared append core. A message opened for
// reading (RopOpenMessage) has a nil write state, so the write ROPs reject it.
type messageWriteState struct {
	props map[wire.PropTag]any
}

// ropCreateMessage handles RopCreateMessage (MS-OXCMSG 2.2.3.2): it opens a new,
// empty message in the folder named by the request and binds it to the output
// handle index for the property and save ROPs that follow. The message has no id
// until RopSaveChangesMessage commits it, so the response reports HasMessageId=0.
func ropCreateMessage(c *ropCtx, _ uint8, hindex uint8) {
	ohindex := c.in.Uint8()
	_ = c.in.Uint16() // code page id; the store is unicode
	folderID := c.in.Uint64()
	associated := c.in.Uint8()
	if c.in.Err() != nil {
		writeRopError(c.out, RopCreateMessage, ohindex, ecError)
		return
	}
	if c.appender == nil {
		writeRopError(c.out, RopCreateMessage, ohindex, ecNotImplemented)
		return
	}
	// Folder-associated (FAI) messages are hidden configuration items, not mail.
	// The canonical store carries only mail, so they are outside the write scope.
	if associated != 0 {
		writeRopError(c.out, RopCreateMessage, ohindex, ecNotImplemented)
		return
	}
	lo := logonFromHandle(c.objectAt(hindex))
	if lo == nil {
		writeRopError(c.out, RopCreateMessage, ohindex, ecNullObject)
		return
	}
	mailbox, _, ok := lo.resolveFolder(folderID)
	if !ok || mailbox == "" {
		// An unresolvable id, or a structural folder that holds no mail (Root, the
		// IPM subtree, the Outbox), cannot hold a created message.
		writeRopError(c.out, RopCreateMessage, ohindex, ecNotFound)
		return
	}
	c.setHandle(ohindex, c.state.alloc(&messageObject{
		mailbox: mailbox,
		write:   &messageWriteState{props: map[wire.PropTag]any{}},
	}))

	out := c.out
	out.Uint8(RopCreateMessage)
	out.Uint8(ohindex)
	out.Uint32(ecSuccess)
	out.Uint8(0) // HasMessageId: the message has no id until it is saved
}

// ropSetProperties handles RopSetProperties (MS-OXCPRPT 2.2.5.1): it merges the
// request's tagged property values into the in-flight message's property buffer.
// Every property is accepted, so the response reports no property problems.
func ropSetProperties(c *ropCtx, _ uint8, hindex uint8) {
	size := int(c.in.Uint16()) // byte count of the property-value array that follows
	end := c.in.Offset() + size
	vals, err := wire.PullTPropValArray(c.in)
	if err != nil || c.in.Err() != nil {
		writeRopError(c.out, RopSetProperties, hindex, ecError)
		return
	}
	// Honor the declared array size, tolerating any trailing padding, so a
	// following chained ROP stays byte-aligned (MS-OXCPRPT 2.2.5.1.1).
	if c.in.Offset() < end {
		c.in.Skip(end - c.in.Offset())
	}
	mo, ok := c.objectAt(hindex).(*messageObject)
	if !ok || mo.write == nil {
		writeRopError(c.out, RopSetProperties, hindex, ecNullObject)
		return
	}
	for _, v := range vals {
		mo.write.props[v.Tag] = v.Value
	}

	out := c.out
	out.Uint8(RopSetProperties)
	out.Uint8(hindex)
	out.Uint32(ecSuccess)
	out.Uint16(0) // PropertyProblemCount: every property was set
}

// ropSaveChangesMessage handles RopSaveChangesMessage (MS-OXCMSG 2.2.3.3): it
// converts the in-flight message's properties to an RFC 5322 message and commits
// it through the shared canonical-append core, then returns the message id the
// client uses to reopen it. The id is derived from the IMAP-index uid the append
// assigns, so a uid of 0 (the append's best-effort index step did not run) is
// fatal here: a message with no uid cannot be reopened or listed by the contents
// table. This is why MAPI treats both the semantic and index steps as fatal,
// unlike SMTP delivery's best-effort path.
func ropSaveChangesMessage(c *ropCtx, _ uint8, hindex uint8) {
	ihindex2 := c.in.Uint8()
	_ = c.in.Uint8() // save flags: KeepOpen variants; the object stays open regardless
	if c.in.Err() != nil {
		writeRopError(c.out, RopSaveChangesMessage, hindex, ecError)
		return
	}
	if c.appender == nil {
		writeRopError(c.out, RopSaveChangesMessage, hindex, ecNotImplemented)
		return
	}
	mo, ok := c.objectAt(hindex).(*messageObject)
	if !ok || mo.write == nil {
		writeRopError(c.out, RopSaveChangesMessage, hindex, ecNullObject)
		return
	}
	now := time.Now()
	raw, err := buildMIMEFromProps(mo.write.props, c.email, now)
	if err != nil {
		writeRopError(c.out, RopSaveChangesMessage, hindex, ecError)
		return
	}
	isRead, flags := draftFlags(mo.write.props)
	res, aerr := c.appender.Append(mailappend.Input{
		Email:        c.email,
		Folder:       mo.mailbox,
		Raw:          raw,
		InternalDate: now,
		Actor:        c.email,
		Source:       semcore.MutationSourceMAPI,
		IsRead:       isRead,
		ExtraFlags:   flags,
	})
	if aerr != nil || res.SemcoreErr != nil || res.UID == 0 {
		writeRopError(c.out, RopSaveChangesMessage, hindex, ecError)
		return
	}
	mo.uid = res.UID
	mo.write = nil // the message is persisted; further reads resolve from the store

	out := c.out
	out.Uint8(RopSaveChangesMessage)
	out.Uint8(hindex)
	out.Uint32(ecSuccess)
	out.Uint8(ihindex2)
	out.Uint64(messageID(res.UID))
}

// buildMIMEFromProps converts a message's MAPI property buffer to an RFC 5322
// message. It reuses the OXMSG property-bag-to-MIME builder (internal/msg), since
// the write ROPs accumulate the same MAPI properties an .msg file carries. The
// authenticated mailbox owner authored the message, so it supplies the From
// header and the authoring time supplies the Date header; both keep the message
// well-formed even for a sparse draft carrying only a subject and body.
func buildMIMEFromProps(props map[wire.PropTag]any, owner string, date time.Time) ([]byte, error) {
	m := &msg.Message{
		Subject:  stringProp(props, wire.PidTagSubject),
		BodyText: stringProp(props, wire.PidTagBody),
		BodyHTML: binaryProp(props, wire.PidTagHtml),
		From:     owner,
		Date:     date,
	}
	return m.MIME()
}

// stringProp returns a PtypString/PtypString8 property value, or "" when absent.
func stringProp(props map[wire.PropTag]any, tag wire.PropTag) string {
	if v, ok := props[tag].(string); ok {
		return v
	}
	return ""
}

// binaryProp returns a PtypBinary property value, or nil when absent.
func binaryProp(props map[wire.PropTag]any, tag wire.PropTag) []byte {
	if v, ok := props[tag].([]byte); ok {
		return v
	}
	return nil
}

// draftFlags derives the canonical read state and the IMAP flags stored on the
// index entry from PidTagMessageFlags (MS-OXCMSG 2.2.1.6). A newly created
// message defaults to unsent (a draft) and read (its author has seen it); a
// client that clears those bits (e.g. importing an already-sent message) drops
// the corresponding flag.
func draftFlags(props map[wire.PropTag]any) (isRead bool, flags []string) {
	unsent, read := true, true
	if v, ok := props[wire.PidTagMessageFlags].(uint32); ok {
		unsent = v&msgFlagUnsent != 0
		read = v&msgFlagRead != 0
	}
	if unsent {
		flags = append(flags, "\\Draft")
	}
	if read {
		flags = append(flags, "\\Seen")
	}
	return read, flags
}
