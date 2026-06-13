package emsmdb

import (
	"github.com/umailserver/umailserver/internal/mapi/wire"
	"github.com/umailserver/umailserver/internal/msg"
)

// attachmentObject is a server object opened on an attachment being built on an
// in-flight message (MS-OXCMSG 2.2.2). RopCreateAttachment binds it; the shared
// property and stream write ROPs fill its property bag (filename, MIME type, and
// the by-value content via PidTagAttachDataBinary); RopSaveChangesAttachment maps
// it to an RFC 5322 attachment part on the parent message, which
// RopSaveChangesMessage renders into a multipart/mixed body. Only by-value
// attachments are written: the content is the inline PidTagAttachDataBinary bytes.
// Embedded-message and by-reference attach methods (PidTagAttachMethod != by-value)
// are deferred, as are reopening a stored attachment (RopOpenAttachment) and
// deleting one (RopDeleteAttachment).
type attachmentObject struct {
	msg   *messageObject
	props map[wire.PropTag]any
	num   uint32
	saved bool
}

// writeProps exposes the attachment's in-flight property buffer for the shared
// property and stream write ROPs. It is non-nil while the parent message is
// in-flight; once that message is saved the attachment can no longer be written.
func (ao *attachmentObject) writeProps() map[wire.PropTag]any {
	if ao.msg.write == nil {
		return nil
	}
	return ao.props
}

// ropCreateAttachment handles RopCreateAttachment (MS-OXCMSG 2.2.2.7): it opens a
// new, empty attachment on the in-flight message named by the request handle and
// binds it to the output handle index for the property, stream, and save ROPs that
// follow. The response carries the attachment number the client uses to reference
// it; the number is a per-message monotonic counter, so two attachments created
// before either is saved still get distinct ids.
func ropCreateAttachment(c *ropCtx, _ uint8, hindex uint8) {
	ohindex := c.in.Uint8()
	if c.in.Err() != nil {
		writeRopError(c.out, RopCreateAttachment, ohindex, ecError)
		return
	}
	mo, ok := c.objectAt(hindex).(*messageObject)
	if !ok || mo.write == nil {
		writeRopError(c.out, RopCreateAttachment, ohindex, ecNullObject)
		return
	}
	num := mo.write.nextAttachNum
	mo.write.nextAttachNum++
	c.setHandle(ohindex, c.state.alloc(&attachmentObject{
		msg:   mo,
		props: map[wire.PropTag]any{},
		num:   num,
	}))

	out := c.out
	out.Uint8(RopCreateAttachment)
	out.Uint8(ohindex)
	out.Uint32(ecSuccess)
	out.Uint32(num) // AttachmentID
}

// ropSaveChangesAttachment handles RopSaveChangesAttachment (MS-OXCMSG 2.2.2.8): it
// maps the attachment's accumulated properties to an RFC 5322 attachment part and
// records it on the parent in-flight message, where RopSaveChangesMessage renders
// it into the message's multipart/mixed body. The response carries no body
// (MS-OXCROPS 2.2.6.16). A repeat save of the same attachment is a no-op so the
// part is not duplicated (modify-after-save is part of the deferred reopen path).
func ropSaveChangesAttachment(c *ropCtx, _ uint8, hindex uint8) {
	_ = c.in.Uint8() // response handle index (ihindex2); the object stays open
	_ = c.in.Uint8() // save flags
	if c.in.Err() != nil {
		writeRopError(c.out, RopSaveChangesAttachment, hindex, ecError)
		return
	}
	ao, ok := c.objectAt(hindex).(*attachmentObject)
	if !ok || ao.msg.write == nil {
		writeRopError(c.out, RopSaveChangesAttachment, hindex, ecNullObject)
		return
	}
	if !ao.saved {
		ao.msg.write.attachments = append(ao.msg.write.attachments, attachmentFromProps(ao.props))
		ao.saved = true
	}

	out := c.out
	out.Uint8(RopSaveChangesAttachment)
	out.Uint8(hindex)
	out.Uint32(ecSuccess)
}

// attachmentFromProps maps an attachment's MAPI property bag to an RFC 5322
// attachment part. The long filename is preferred over the 8.3 short name; the
// content is the by-value PidTagAttachDataBinary bytes.
func attachmentFromProps(props map[wire.PropTag]any) msg.Attachment {
	return msg.Attachment{
		Filename:    firstNonEmpty(stringProp(props, wire.PidTagAttachLongFilename), stringProp(props, wire.PidTagAttachFilename)),
		ContentType: stringProp(props, wire.PidTagAttachMimeTag),
		ContentID:   stringProp(props, wire.PidTagAttachContentID),
		Data:        binaryProp(props, wire.PidTagAttachDataBinary),
	}
}
