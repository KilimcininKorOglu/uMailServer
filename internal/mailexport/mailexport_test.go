package mailexport

import (
	"bytes"
	"strings"
	"testing"

	"github.com/umailserver/umailserver/internal/mailimport"
)

// trimNL drops trailing CR/LF so mbox round-trips can be compared by content
// (the mbox blank-line separator makes a single trailing newline ambiguous).
func trimNL(b []byte) string {
	return strings.TrimRight(string(b), "\r\n")
}

func TestWriteMboxRoundTripsThroughReadMbox(t *testing.T) {
	// Messages chosen to exercise the escaping: a body line starting with
	// "From " (would be a false separator if unescaped) and one already
	// resembling ">From " (must gain a second ">").
	msgs := [][]byte{
		[]byte("Subject: One\r\nFrom: a@example.com\r\nDate: Mon, 01 Jan 2024 10:00:00 +0000\r\n\r\nBody one.\r\nFrom here the body continues"),
		[]byte("Subject: Two\r\n\r\n>From an already-escaped-looking line\r\nlast line"),
	}

	var buf bytes.Buffer
	if err := WriteMbox(&buf, msgs); err != nil {
		t.Fatalf("WriteMbox: %v", err)
	}

	got, err := mailimport.ReadMbox(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("ReadMbox: %v", err)
	}
	if len(got) != len(msgs) {
		t.Fatalf("round-trip messages = %d, want %d\nmbox:\n%s", len(got), len(msgs), buf.String())
	}
	for i := range msgs {
		if trimNL(got[i].Raw) != trimNL(msgs[i]) {
			t.Errorf("message %d did not round-trip\n got: %q\nwant: %q", i, got[i].Raw, msgs[i])
		}
	}
}

func TestEscapeFromLines(t *testing.T) {
	cases := map[string]string{
		"From here":           ">From here",
		">From here":          ">>From here",
		">>From here":         ">>>From here",
		"Normal line":         "Normal line",
		"Fromage is not From": "Fromage is not From", // "From" without trailing space is not escaped
		"From here\r":         ">From here\r",        // trailing CR preserved
	}
	for in, want := range cases {
		if got := string(escapeFromLines([]byte(in))); got != want {
			t.Errorf("escapeFromLines(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestEnvelopeLineUsesHeaders(t *testing.T) {
	raw := []byte("From: sender@example.com\r\nDate: Mon, 01 Jan 2024 10:00:00 +0000\r\n\r\nbody")
	line := envelopeLine(raw)
	if !strings.HasPrefix(line, "From sender@example.com ") {
		t.Errorf("envelope line missing sender: %q", line)
	}
	if !strings.HasSuffix(line, "\r\n") {
		t.Errorf("envelope line not CRLF-terminated: %q", line)
	}
	// No From/Date headers -> deterministic fallbacks, never an error.
	fallback := envelopeLine([]byte("just a body, no headers"))
	if !strings.HasPrefix(fallback, "From MAILER-DAEMON ") {
		t.Errorf("fallback envelope line = %q", fallback)
	}
}

func TestWriteMboxEmpty(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteMbox(&buf, nil); err != nil {
		t.Fatalf("WriteMbox(nil): %v", err)
	}
	if buf.Len() != 0 {
		t.Errorf("empty export wrote %d bytes", buf.Len())
	}
}
