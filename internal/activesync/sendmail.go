package activesync

import (
	"errors"

	"github.com/umailserver/umailserver/internal/activesync/wbxml"
)

// Submitter sends a composed message (a complete RFC 822 message) on behalf of
// the authenticated mailbox: it queues the message for outbound delivery and,
// when saveToSent is set, files a copy in the Sent folder. The mailbox is the
// envelope sender; the recipients are taken from the message headers.
type Submitter func(email string, mime []byte, saveToSent bool) error

// handleSendMail answers the SendMail/SmartForward/SmartReply commands (MS-ASCMD,
// ComposeMail code page). The request carries the composed message in a Mime
// element (opaque, byte-exact) and an optional SaveInSentItems flag. On success
// the response is an empty body (HTTP 200) per MS-ASHTTP; a submission failure
// surfaces as an error.
//
// SmartForward and SmartReply route here too: they submit the composed Mime the
// client supplies. Server-side stitching of the referenced original (the Source
// element) is a later refinement; the client's composed message is sent as given.
func (s *Server) handleSendMail(ctx *Context) ([]byte, error) {
	if s.submitter == nil {
		return nil, errors.New("activesync: submitter not configured")
	}
	root, err := wbxml.Unmarshal(ctx.Body)
	if err != nil {
		return nil, err
	}
	mime := mimeOf(root)
	if len(mime) == 0 {
		return nil, errors.New("activesync sendmail: request carries no Mime")
	}
	saveToSent := root.Sub("SaveInSentItems") != nil
	if err := s.submitter(ctx.Email, mime, saveToSent); err != nil {
		return nil, err
	}
	return nil, nil // success is an empty 200 response (MS-ASCMD SendMail)
}

// mimeOf extracts the raw composed message from a ComposeMail request: the Mime
// element, carried as opaque bytes (byte-exact, the usual encoding) or, failing
// that, as inline text.
func mimeOf(root *wbxml.Element) []byte {
	m := root.Sub("Mime")
	if m == nil {
		return nil
	}
	if len(m.Opaque) > 0 {
		return m.Opaque
	}
	return []byte(m.Text)
}
