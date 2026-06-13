package emsmdb

import (
	"net/mail"
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

// Recipient types (MS-OXCMSG 2.2.3.5.2, the MODIFYRECIPIENT_ROW RecipientType
// byte, == PidTagRecipientType MAPI_TO/CC/BCC).
const (
	recipientTo  uint8 = 1
	recipientCc  uint8 = 2
	recipientBcc uint8 = 3
)

// RECIPIENT_ROW flags and address types (MS-OXCDATA 2.8.3.1). Only the bits the
// row parser branches on are named.
const (
	recipFlagEmail         uint16 = 0x0008
	recipFlagDisplay       uint16 = 0x0010
	recipFlagTransmittable uint16 = 0x0020
	recipFlagSimple        uint16 = 0x0400
	recipFlagUnicode       uint16 = 0x0200
	recipFlagOutOfStandard uint16 = 0x8000

	recipTypeNone   uint16 = 0x0
	recipTypeX500DN uint16 = 0x1
	recipTypeDList1 uint16 = 0x6
	recipTypeDList2 uint16 = 0x7
)

// recipient is one resolved message recipient: its kind (To/Cc/Bcc) and the
// address/name extracted from a MODIFYRECIPIENT_ROW.
type recipient struct {
	kind  uint8
	email string
	name  string
}

// messageWriteState is the in-flight buffer of a message opened for creation by
// RopCreateMessage. The write ROPs accumulate properties (RopSetProperties) and
// recipients (RopModifyRecipients) here until RopSaveChangesMessage converts
// them to an RFC 5322 message and commits it to the canonical store through the
// shared append core. A message opened for reading (RopOpenMessage) has a nil
// write state, so the write ROPs reject it.
type messageWriteState struct {
	props         map[wire.PropTag]any
	recipients    []recipient
	attachments   []msg.Attachment
	nextAttachNum uint32
}

// propWriter is a server object that holds an in-flight MAPI property bag the
// property and stream write ROPs target. A message opened for creation and an
// attachment being built both expose one. writeProps returns nil when the object
// is not in a writable state (a saved or read-opened message), so a write ROP that
// lands on it is rejected.
type propWriter interface {
	writeProps() map[wire.PropTag]any
}

// writeProps exposes the in-flight property buffer of a message opened for
// creation, or nil for a read-opened or already-saved message.
func (mo *messageObject) writeProps() map[wire.PropTag]any {
	if mo.write == nil {
		return nil
	}
	return mo.write.props
}

// writablePropsAt returns the in-flight property bag of the writable object at the
// handle index, or nil when the handle holds no object, an object with no property
// bag, or one not in a writable state.
func (c *ropCtx) writablePropsAt(hindex uint8) map[wire.PropTag]any {
	if pw, ok := c.objectAt(hindex).(propWriter); ok {
		return pw.writeProps()
	}
	return nil
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
	props := c.writablePropsAt(hindex)
	if props == nil {
		writeRopError(c.out, RopSetProperties, hindex, ecNullObject)
		return
	}
	for _, v := range vals {
		props[v.Tag] = v.Value
	}

	out := c.out
	out.Uint8(RopSetProperties)
	out.Uint8(hindex)
	out.Uint32(ecSuccess)
	out.Uint16(0) // PropertyProblemCount: every property was set
}

// ropDeleteProperties handles RopDeleteProperties (MS-OXCPRPT 2.2.6.4 / MS-OXCROPS
// 2.2.8.7): it removes the named properties from the in-flight message's property
// buffer. Tags are matched by property id (so a delete tolerates a String8/Unicode
// type variant of the value that was set). Every tag is accepted, so the response
// reports no property problems.
func ropDeleteProperties(c *ropCtx, _ uint8, hindex uint8) {
	tags := wire.PullPropertyTagArray(c.in)
	if c.in.Err() != nil {
		writeRopError(c.out, RopDeleteProperties, hindex, ecError)
		return
	}
	props := c.writablePropsAt(hindex)
	if props == nil {
		writeRopError(c.out, RopDeleteProperties, hindex, ecNullObject)
		return
	}
	for _, t := range tags {
		for k := range props {
			if k.ID() == t.ID() {
				delete(props, k)
			}
		}
	}

	out := c.out
	out.Uint8(RopDeleteProperties)
	out.Uint8(hindex)
	out.Uint32(ecSuccess)
	out.Uint16(0) // PropertyProblemCount: every property was deleted
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
	raw, err := buildMIMEFromProps(mo.write.props, mo.write.recipients, mo.write.attachments, c.email, now)
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
	// Capture what RopSubmitMessage needs to send this message later: the blob key to
	// read the stored (Bcc-free) MIME back, and the full recipient set — including the
	// Bcc recipients dropped from the headers — as the delivery envelope.
	mo.messageID = res.MessageID
	mo.submitEnvelope = envelopeAddresses(mo.write.recipients)
	mo.write = nil // the message is persisted; further reads resolve from the store

	out := c.out
	out.Uint8(RopSaveChangesMessage)
	out.Uint8(hindex)
	out.Uint32(ecSuccess)
	out.Uint8(ihindex2)
	out.Uint64(messageID(res.UID))
}

// ropModifyRecipients handles RopModifyRecipients (MS-OXCMSG 2.2.3.5): it parses
// the recipient rows into the in-flight message's recipient list, which
// RopSaveChangesMessage renders into To/Cc headers. The leading column tags apply
// to every row's property values. The recipient set accumulates (the ROP
// semantically adds rows); RowId-keyed modify/remove is a later refinement. The
// response carries no body (MS-OXCROPS 2.2.6.5).
func ropModifyRecipients(c *ropCtx, _ uint8, hindex uint8) {
	cols := wire.PullPropertyTagArray(c.in)
	count := int(c.in.Uint16())
	if c.in.Err() != nil {
		writeRopError(c.out, RopModifyRecipients, hindex, ecError)
		return
	}
	mo, ok := c.objectAt(hindex).(*messageObject)
	if !ok || mo.write == nil {
		writeRopError(c.out, RopModifyRecipients, hindex, ecNullObject)
		return
	}
	for range count {
		r, present, rok := pullModifyRecipientRow(c.in, cols)
		if !rok {
			writeRopError(c.out, RopModifyRecipients, hindex, ecError)
			return
		}
		if present {
			mo.write.recipients = append(mo.write.recipients, r)
		}
	}

	out := c.out
	out.Uint8(RopModifyRecipients)
	out.Uint8(hindex)
	out.Uint32(ecSuccess)
}

// pullModifyRecipientRow parses one MODIFYRECIPIENT_ROW (MS-OXCMSG 2.2.3.5.2): a
// row id, a recipient type, then a size-bounded RECIPIENT_ROW. A zero size is a
// recipient removal (no row) and reports present=false. The cursor is advanced to
// the declared row end regardless of how much of the row was parsed, so the next
// row stays byte-aligned even if a field is left unparsed.
func pullModifyRecipientRow(p *wire.Pull, cols []wire.PropTag) (r recipient, present, ok bool) {
	_ = p.Uint32() // RowId; keyed modify/remove is a later refinement
	kind := p.Uint8()
	rowSize := int(p.Uint16())
	if p.Err() != nil {
		return recipient{}, false, false
	}
	if rowSize == 0 {
		return recipient{}, false, true // removal: nothing to add on the create path
	}
	end := p.Offset() + rowSize
	r = pullRecipientRow(p, cols)
	if p.Err() != nil || p.Offset() > end {
		return recipient{}, false, false
	}
	if p.Offset() < end {
		p.Skip(end - p.Offset()) // honor RowSize (matches the reference m_offset reset)
	}
	r.kind = kind
	return r, true, true
}

// pullRecipientRow parses a RECIPIENT_ROW (MS-OXCDATA 2.8.3.2) and extracts the
// recipient's address and display name. The address comes from the EMAIL fixed
// field when present, else the PidTagSmtpAddress/PidTagEmailAddress property
// column; the name from the DISPLAY (or transmittable) fixed field, else the
// PidTagDisplayName column. Fixed string fields are UTF-16 under the row's UNICODE
// flag and 8-bit otherwise; the property columns follow their own tag types.
func pullRecipientRow(p *wire.Pull, cols []wire.PropTag) recipient {
	flags := p.Uint16()
	addrType := flags & 0x0007
	unicode := flags&recipFlagUnicode != 0
	rstr := func() string {
		if unicode {
			return p.WStr()
		}
		return p.Str()
	}
	switch addrType {
	case recipTypeX500DN:
		_ = p.Uint8() // address prefix used
		_ = p.Uint8() // display type
		_ = p.Str()   // X500DN (always 8-bit)
	case recipTypeDList1, recipTypeDList2:
		_ = p.Bin() // distribution-list entry id
		_ = p.Bin() // distribution-list search key
	}
	if addrType == recipTypeNone && flags&recipFlagOutOfStandard != 0 {
		_ = p.Str() // address type (always 8-bit)
	}
	var email, name string
	if flags&recipFlagEmail != 0 {
		email = rstr()
	}
	if flags&recipFlagDisplay != 0 {
		name = rstr()
	}
	if flags&recipFlagSimple != 0 {
		_ = rstr() // simple display name
	}
	if flags&recipFlagTransmittable != 0 {
		if tn := rstr(); name == "" {
			name = tn
		}
	}
	rcCount := int(p.Uint16())
	if rcCount > len(cols) {
		return recipient{email: email, name: name} // malformed; rely on the fixed fields
	}
	row, err := wire.PullPropertyRow(p, cols[:rcCount])
	if err == nil {
		if email == "" {
			email = firstNonEmpty(propRowString(cols[:rcCount], row, wire.PidTagSmtpAddress),
				propRowString(cols[:rcCount], row, wire.PidTagEmailAddress))
		}
		if name == "" {
			name = propRowString(cols[:rcCount], row, wire.PidTagDisplayName)
		}
	}
	return recipient{email: email, name: name}
}

// propRowString returns the string value of the property tag (matched by id, so
// String8/Unicode variants both match) in a parsed property row, or "".
func propRowString(cols []wire.PropTag, row wire.PropertyRow, tag wire.PropTag) string {
	for i, c := range cols {
		if c.ID() != tag.ID() || i >= len(row.Values) {
			continue
		}
		v := row.Values[i]
		if fv, ok := v.(wire.FlaggedPropertyValue); ok {
			v = fv.Value
		}
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// firstNonEmpty returns the first non-empty argument, or "".
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// buildMIMEFromProps converts a message's MAPI property buffer and recipients to
// an RFC 5322 message. It reuses the OXMSG property-bag-to-MIME builder
// (internal/msg), since the write ROPs accumulate the same MAPI properties an
// .msg file carries. The authenticated mailbox owner authored the message, so it
// supplies the From header (the sender is set through PidTagSender* properties,
// not the recipient table) and the authoring time supplies the Date header; both
// keep the message well-formed even for a sparse draft carrying only a subject
// and body. To/Cc come from the recipients; Bcc is intentionally dropped from the
// headers (matching the OXMSG import), so a draft's Bcc is not yet preserved.
func buildMIMEFromProps(props map[wire.PropTag]any, recipients []recipient, attachments []msg.Attachment, owner string, date time.Time) ([]byte, error) {
	m := &msg.Message{
		Subject:     stringProp(props, wire.PidTagSubject),
		BodyText:    stringProp(props, wire.PidTagBody),
		BodyHTML:    binaryProp(props, wire.PidTagHtml),
		From:        owner,
		Date:        date,
		Attachments: attachments,
	}
	for _, r := range recipients {
		addr := formatRecipient(r)
		if addr == "" {
			continue
		}
		switch r.kind {
		case recipientTo:
			m.To = append(m.To, addr)
		case recipientCc:
			m.Cc = append(m.Cc, addr)
		case recipientBcc:
			// Bcc is intentionally not written to the headers (a draft's Bcc is
			// not yet preserved); see the function doc and the work-plan deferral.
		}
	}
	return m.MIME()
}

// formatRecipient renders a recipient as an RFC 5322 address, RFC 2047-encoding
// the display name when needed. An empty address yields "" so the caller skips it.
func formatRecipient(r recipient) string {
	if r.email == "" {
		return ""
	}
	return (&mail.Address{Name: r.name, Address: r.email}).String()
}

// envelopeAddresses returns the bare email addresses of every recipient — To, Cc,
// and Bcc — as the SMTP delivery envelope for RopSubmitMessage. The Bcc addresses
// are included even though buildMIMEFromProps keeps them out of the message
// headers, so a blind-carbon recipient is delivered to without appearing in the
// copy the other recipients receive. Recipients with no address are skipped.
func envelopeAddresses(recipients []recipient) []string {
	addrs := make([]string, 0, len(recipients))
	for _, r := range recipients {
		if r.email != "" {
			addrs = append(addrs, r.email)
		}
	}
	return addrs
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
