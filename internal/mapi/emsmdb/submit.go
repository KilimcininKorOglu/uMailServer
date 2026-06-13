package emsmdb

// SubmitFunc submits a message for delivery through the server's canonical
// submission path: from is the envelope sender, to the explicit envelope
// recipients (To, Cc, and Bcc), and raw the RFC 5322 message. emsmdb binds the
// server's shared submission function — the one SMTP submission, EWS SendItem, and
// JMAP EmailSubmission already route through — to it, so a message sent over
// MAPI/HTTP is delivered, Sieve-filtered, and send-policy-gated identically to
// every other surface.
type SubmitFunc func(from string, to []string, raw []byte) error

// ropSubmitMessage handles RopSubmitMessage (MS-OXOMSG; MS-OXCROPS 2.2.7.1): it
// submits a saved message for delivery through the shared canonical submission
// path. The delivery envelope is the full recipient set captured when the message
// was saved — including the Bcc recipients, which RopSaveChangesMessage keeps out
// of the stored headers — so a blind-carbon recipient is delivered to without
// appearing in the copy the To and Cc recipients receive. The body is read back
// from the canonical blob store, so the wire message is the exact (Bcc-free)
// content that was persisted.
//
// Filing a Sent copy is not part of the submission core: on the MAPI path it is
// driven by the client and store configuration, just as it is the client's
// SendAndSaveCopy choice on EWS and an IMAP APPEND on SMTP submission. The shared
// submission function delivers only, so the saved message is left in place. The
// response carries no body (MS-OXCROPS 2.2.7.1).
func ropSubmitMessage(c *ropCtx, _ uint8, hindex uint8) {
	_ = c.in.Uint8() // submit flags (MS-OXOMSG SubmitFlags: PreProcess/NeedsSpooler); online submission is immediate
	if c.in.Err() != nil {
		writeRopError(c.out, RopSubmitMessage, hindex, ecError)
		return
	}
	if c.submitter == nil || c.body == nil {
		writeRopError(c.out, RopSubmitMessage, hindex, ecNotImplemented)
		return
	}
	mo, ok := c.objectAt(hindex).(*messageObject)
	if !ok {
		writeRopError(c.out, RopSubmitMessage, hindex, ecNullObject)
		return
	}
	// A message must be saved — RopSaveChangesMessage assigns its blob key and
	// captures its delivery envelope — and must address at least one recipient
	// before it can be submitted. A reopened draft carries neither the key nor the
	// envelope, so submitting one is not yet supported; a recipientless message has
	// nothing to submit to.
	if mo.messageID == "" || len(mo.submitEnvelope) == 0 {
		writeRopError(c.out, RopSubmitMessage, hindex, ecError)
		return
	}
	raw, err := c.body.ReadMessage(c.email, mo.messageID)
	if err != nil {
		writeRopError(c.out, RopSubmitMessage, hindex, ecNotFound)
		return
	}
	if err := c.submitter(c.email, mo.submitEnvelope, raw); err != nil {
		writeRopError(c.out, RopSubmitMessage, hindex, ecError)
		return
	}

	out := c.out
	out.Uint8(RopSubmitMessage)
	out.Uint8(hindex)
	out.Uint32(ecSuccess)
}
