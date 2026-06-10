// Package mailexport serializes raw RFC 5322 messages read from the canonical
// store into the common interchange formats. It is the inverse of
// internal/mailimport: WriteMbox is the exact counterpart of mailimport.ReadMbox
// (mboxrd ">From " escaping and "From " envelope separators), so an mbox written
// here re-parses to the same messages. It is pure serialization with no storage
// dependencies, so it is unit-testable in isolation; the caller (the
// `umailserver export` CLI) supplies the message bytes and handles .eml/Maildir
// file layout.
package mailexport

import (
	"bytes"
	"fmt"
	"io"
	"net/mail"
	"time"
)

// epochAsctime is the deterministic fallback date for the mbox "From " envelope
// line when a message has no parseable Date (kept fixed so output is a pure
// function of its input).
var epochAsctime = time.Unix(0, 0).UTC().Format("Mon Jan _2 15:04:05 2006")

// WriteMbox serializes messages to w in mbox (mboxrd) form: each message is
// preceded by a "From <addr> <date>" envelope line, its body lines starting with
// "From " (or ">From ", ...) are escaped with a leading ">", and messages are
// separated by a blank line. mailimport.ReadMbox reverses this exactly (modulo a
// normalized trailing newline).
func WriteMbox(w io.Writer, messages [][]byte) error {
	for _, raw := range messages {
		if _, err := io.WriteString(w, envelopeLine(raw)); err != nil {
			return err
		}
		if _, err := w.Write(escapeFromLines(raw)); err != nil {
			return err
		}
		// Terminate the message's last line and add the blank separator.
		if _, err := io.WriteString(w, "\r\n\r\n"); err != nil {
			return err
		}
	}
	return nil
}

// envelopeLine builds the mbox "From " separator line for a message, using the
// sender address and date from its headers (deterministic fallbacks otherwise).
func envelopeLine(raw []byte) string {
	addr, date := "MAILER-DAEMON", epochAsctime
	if msg, err := mail.ReadMessage(bytes.NewReader(raw)); err == nil {
		if a, perr := mail.ParseAddress(msg.Header.Get("From")); perr == nil && a.Address != "" {
			addr = a.Address
		}
		if d, derr := mail.ParseDate(msg.Header.Get("Date")); derr == nil {
			date = d.UTC().Format("Mon Jan _2 15:04:05 2006")
		}
	}
	return fmt.Sprintf("From %s %s\r\n", addr, date)
}

// escapeFromLines applies mboxrd escaping: any line whose content (ignoring a
// trailing CR) is zero-or-more ">" followed by "From " gets one leading ">".
// This is the exact inverse of mailimport's unescapeFromLine.
func escapeFromLines(raw []byte) []byte {
	lines := bytes.Split(raw, []byte("\n"))
	for i, line := range lines {
		if needsFromEscape(line) {
			lines[i] = append([]byte{'>'}, line...)
		}
	}
	return bytes.Join(lines, []byte("\n"))
}

// needsFromEscape reports whether a line is a ">"*"From " line that must be
// escaped (a trailing CR is ignored).
func needsFromEscape(line []byte) bool {
	body := bytes.TrimSuffix(line, []byte("\r"))
	trimmed := bytes.TrimLeft(body, ">")
	return bytes.HasPrefix(trimmed, []byte("From "))
}
