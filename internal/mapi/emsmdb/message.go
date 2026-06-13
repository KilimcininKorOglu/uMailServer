package emsmdb

import (
	"math/bits"
	"slices"

	"github.com/umailserver/umailserver/internal/mapi/wire"
	"github.com/umailserver/umailserver/internal/storage"
)

// msgFlagRead marks a message the user has read (MS-OXCMSG 2.2.1.6,
// PidTagMessageFlags). Only the read bit is derived on the online read path.
const msgFlagRead uint32 = 0x00000001

// stringTypeNone is a TypedString carrying no string data (MS-OXCDATA 2.11.1,
// STRING_TYPE_NONE): the client reads the real value through RopGetProperties.
const stringTypeNone uint8 = 0x00

// messageObject is a server object opened on a single message. When opened for
// reading (RopOpenMessage) it carries the mailbox and uid that locate the message
// in the canonical store for the property ROPs that read it. When opened for
// creation (RopCreateMessage) it carries a non-nil write state instead, holding
// the in-flight properties until RopSaveChangesMessage commits them and assigns
// the uid.
type messageObject struct {
	mailbox string
	uid     uint32
	write   *messageWriteState
}

// messageID builds a message id (MID) from a message uid, reusing the replica id
// and 48-bit-counter layout of a folder id (MS-OXCDATA 2.2.1.2). The uid is
// stable per mailbox, so the MID round-trips back to the uid.
func messageID(uid uint32) uint64 {
	return makeFID(fidReplID, uint64(uid))
}

// messageUID recovers the message uid from a message id, inverting messageID.
func messageUID(mid uint64) uint32 {
	return uint32(bits.ReverseBytes64(mid &^ 0xFFFF))
}

// logonFromHandle returns the logon a handle leads to, whether the handle holds
// the logon itself or a folder opened under it.
func logonFromHandle(obj any) *logonObject {
	switch o := obj.(type) {
	case *logonObject:
		return o
	case *folderObject:
		return o.logon
	default:
		return nil
	}
}

// ropOpenMessage handles RopOpenMessage (MS-OXCMSG 2.2.3.1): it opens a message
// by folder id and message id under the logon and binds a message object to the
// output handle index. The response leaves the subject fields empty and carries
// no recipients; the client reads those through the property ROPs.
func ropOpenMessage(c *ropCtx, _ uint8, hindex uint8) {
	ohindex := c.in.Uint8()
	_ = c.in.Uint16() // code page id; the store is unicode
	folderID := c.in.Uint64()
	_ = c.in.Uint8() // open mode flags
	messageID := c.in.Uint64()
	if c.in.Err() != nil {
		writeRopError(c.out, RopOpenMessage, ohindex, ecError)
		return
	}
	lo := logonFromHandle(c.objectAt(hindex))
	if lo == nil {
		writeRopError(c.out, RopOpenMessage, ohindex, ecNullObject)
		return
	}
	mailbox, _, ok := lo.resolveFolder(folderID)
	if !ok || mailbox == "" {
		writeRopError(c.out, RopOpenMessage, ohindex, ecNotFound)
		return
	}
	uid := messageUID(messageID)
	if _, err := c.store.GetMessageMetadata(c.email, mailbox, uid); err != nil {
		writeRopError(c.out, RopOpenMessage, ohindex, ecNotFound)
		return
	}
	c.setHandle(ohindex, c.state.alloc(&messageObject{mailbox: mailbox, uid: uid}))

	out := c.out
	out.Uint8(RopOpenMessage)
	out.Uint8(ohindex)
	out.Uint32(ecSuccess)
	out.Uint8(0)                        // HasNamedProperties: none
	out.Uint8(stringTypeNone)           // SubjectPrefix
	out.Uint8(stringTypeNone)           // NormalizedSubject
	out.Uint16(0)                       // RecipientCount
	wire.PushPropertyTagArray(out, nil) // RecipientColumns: none
	out.Uint8(0)                        // RowCount: no recipient rows
}

// ropGetPropertiesSpecific handles RopGetPropertiesSpecific (MS-OXCPRPT 2.2.7):
// it returns the requested properties of an opened message as a single property
// row, marking any property the online path does not serve as an error.
func ropGetPropertiesSpecific(c *ropCtx, _ uint8, hindex uint8) {
	_ = c.in.Uint16() // property size limit; not enforced
	_ = c.in.Uint16() // want-unicode flag; the store is unicode
	cols := wire.PullPropertyTagArray(c.in)
	if c.in.Err() != nil {
		writeRopError(c.out, RopGetPropertiesSpecific, hindex, ecError)
		return
	}
	mo, ok := c.objectAt(hindex).(*messageObject)
	if !ok {
		writeRopError(c.out, RopGetPropertiesSpecific, hindex, ecNullObject)
		return
	}
	m, err := c.store.GetMessageMetadata(c.email, mo.mailbox, mo.uid)
	if err != nil {
		writeRopError(c.out, RopGetPropertiesSpecific, hindex, ecNotFound)
		return
	}
	// Bodies live in Maildir, not the metadata store; read the raw message once
	// when a plain or HTML body column is requested and a body store is set.
	var plain, html any
	havePlain, haveHTML := false, false
	needPlain := slices.Contains(cols, wire.PidTagBody)
	needHTML := slices.Contains(cols, wire.PidTagHtml)
	if c.body != nil && (needPlain || needHTML) {
		if raw, rerr := c.body.ReadMessage(c.email, m.MessageID); rerr == nil {
			if needPlain {
				plain, havePlain = extractMessageBody(raw), true
			}
			if needHTML {
				if h := extractHTMLBody(raw); h != nil {
					html, haveHTML = h, true
				}
			}
		}
	}
	row := wire.NewPush(wire.FlagUTF16)
	if err := pushRow(row, cols, func(t wire.PropTag) (any, bool) {
		switch t {
		case wire.PidTagBody:
			return plain, havePlain
		case wire.PidTagHtml:
			return html, haveHTML
		default:
			return messageProperty(t, m)
		}
	}); err != nil {
		writeRopError(c.out, RopGetPropertiesSpecific, hindex, ecError)
		return
	}

	out := c.out
	out.Uint8(RopGetPropertiesSpecific)
	out.Uint8(hindex)
	out.Uint32(ecSuccess)
	out.Raw(row.Bytes())
}

// messageAllTags is the full set of scalar message properties the online path
// serves from RopGetPropertiesAll, before bodies are appended.
var messageAllTags = []wire.PropTag{
	wire.PidTagMid, wire.PidTagInstanceKey, wire.PidTagSubject,
	wire.PidTagMessageClass, wire.PidTagMessageDeliveryTime,
	wire.PidTagMessageSize, wire.PidTagMessageFlags,
}

// ropGetPropertiesAll handles RopGetPropertiesAll (MS-OXCPRPT 2.2.8): it returns
// every property of an opened message the online path serves as a tagged-value
// array, including the plain and HTML bodies when a body store is set.
func ropGetPropertiesAll(c *ropCtx, _ uint8, hindex uint8) {
	_ = c.in.Uint16() // property size limit; not enforced
	_ = c.in.Uint16() // want-unicode flag; the store is unicode
	if c.in.Err() != nil {
		writeRopError(c.out, RopGetPropertiesAll, hindex, ecError)
		return
	}
	mo, ok := c.objectAt(hindex).(*messageObject)
	if !ok {
		writeRopError(c.out, RopGetPropertiesAll, hindex, ecNullObject)
		return
	}
	m, err := c.store.GetMessageMetadata(c.email, mo.mailbox, mo.uid)
	if err != nil {
		writeRopError(c.out, RopGetPropertiesAll, hindex, ecNotFound)
		return
	}
	vals := make([]wire.TaggedPropertyValue, 0, len(messageAllTags)+2)
	for _, t := range messageAllTags {
		if v, ok := messageProperty(t, m); ok {
			vals = append(vals, wire.TaggedPropertyValue{Tag: t, Value: v})
		}
	}
	if c.body != nil {
		if raw, rerr := c.body.ReadMessage(c.email, m.MessageID); rerr == nil {
			vals = append(vals, wire.TaggedPropertyValue{Tag: wire.PidTagBody, Value: extractMessageBody(raw)})
			if h := extractHTMLBody(raw); h != nil {
				vals = append(vals, wire.TaggedPropertyValue{Tag: wire.PidTagHtml, Value: h})
			}
		}
	}
	body := wire.NewPush(wire.FlagUTF16)
	if err := wire.PushTPropValArray(body, vals); err != nil {
		writeRopError(c.out, RopGetPropertiesAll, hindex, ecError)
		return
	}

	out := c.out
	out.Uint8(RopGetPropertiesAll)
	out.Uint8(hindex)
	out.Uint32(ecSuccess)
	out.Raw(body.Bytes())
}

// instanceKey is a table row's unique key (PidTagInstanceKey): the 4-byte
// little-endian uid, unique within the folder snapshot.
func instanceKey(uid uint32) []byte {
	return []byte{byte(uid), byte(uid >> 8), byte(uid >> 16), byte(uid >> 24)}
}

// lessByTag reports whether message a sorts before b under the given property.
// Only the columns a contents table is commonly sorted by are ordered; any other
// tag leaves the order unchanged.
func lessByTag(tag wire.PropTag, a, b *storage.MessageMetadata) bool {
	switch tag {
	case wire.PidTagMessageDeliveryTime, wire.PidTagLastModificationTime:
		return a.InternalDate.Before(b.InternalDate)
	case wire.PidTagMessageSize:
		return a.Size < b.Size
	case wire.PidTagSubject:
		return a.Subject < b.Subject
	case wire.PidTagMid:
		return a.UID < b.UID
	default:
		return false
	}
}

// messageFlags derives PidTagMessageFlags from the stored IMAP flags.
func messageFlags(m *storage.MessageMetadata) uint32 {
	var f uint32
	for _, fl := range m.Flags {
		if fl == "\\Seen" {
			f |= msgFlagRead
		}
	}
	return f
}

// messageProperty returns the value of one property for a message and whether it
// is available. Unmapped columns report unavailable so the row can flag them.
// The value's Go type matches the column's property type (MS-OXCDATA 2.11.2).
func messageProperty(tag wire.PropTag, m *storage.MessageMetadata) (any, bool) {
	switch tag {
	case wire.PidTagMid:
		return messageID(m.UID), true
	case wire.PidTagInstanceKey:
		return instanceKey(m.UID), true
	case wire.PidTagSubject:
		return m.Subject, true
	case wire.PidTagMessageClass:
		return "IPM.Note", true
	case wire.PidTagMessageDeliveryTime:
		return wire.FileTimeFromTime(m.InternalDate), true
	case wire.PidTagMessageSize:
		return uint32(m.Size), true
	case wire.PidTagMessageFlags:
		return messageFlags(m), true
	default:
		return nil, false
	}
}
