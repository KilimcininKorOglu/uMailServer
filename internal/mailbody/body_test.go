package mailbody

import "testing"

// TestParsePlainText proves a simple text/plain message yields its text as Text
// (trimmed) and no HTML, so Display renders text and SearchText matches the words.
func TestParsePlainText(t *testing.T) {
	raw := []byte("Subject: hi\r\nContent-Type: text/plain\r\n\r\nHello world\r\n")
	b := Parse(raw)
	if b.Text != "Hello world" {
		t.Errorf("Text = %q, want %q", b.Text, "Hello world")
	}
	if b.HTML != "" {
		t.Errorf("HTML = %q, want empty", b.HTML)
	}
	if got, isHTML := b.Display(); got != "Hello world" || isHTML {
		t.Errorf("Display() = (%q, %v), want (%q, false)", got, isHTML, "Hello world")
	}
	if got := b.SearchText(); got != "Hello world" {
		t.Errorf("SearchText() = %q, want %q", got, "Hello world")
	}
}

// TestParseBase64HTMLOnly is the load-bearing case behind unifying the surfaces:
// an HTML-only body with base64 transfer-encoding. A raw header-strip would leave
// the base64 string unsearchable and unrenderable; decoding makes Display carry
// the real HTML and SearchText carry the visible words (tags stripped), so a
// browser, a phone, and a search query all see the same content.
func TestParseBase64HTMLOnly(t *testing.T) {
	// base64("<p>Quarterly <b>report</b> ready</p>")
	raw := []byte("Content-Type: text/html\r\nContent-Transfer-Encoding: base64\r\n\r\n" +
		"PHA+UXVhcnRlcmx5IDxiPnJlcG9ydDwvYj4gcmVhZHk8L3A+\r\n")
	b := Parse(raw)
	if b.Text != "" {
		t.Errorf("Text = %q, want empty (HTML-only message)", b.Text)
	}
	if b.HTML != "<p>Quarterly <b>report</b> ready</p>" {
		t.Errorf("HTML = %q, want decoded markup", b.HTML)
	}
	if got, isHTML := b.Display(); !isHTML || got != "<p>Quarterly <b>report</b> ready</p>" {
		t.Errorf("Display() = (%q, %v), want decoded HTML", got, isHTML)
	}
	if got := b.SearchText(); got != "Quarterly report ready" {
		t.Errorf("SearchText() = %q, want tag-stripped %q", got, "Quarterly report ready")
	}
}

// TestParseMultipartAlternative proves a multipart/alternative (text/plain +
// text/html, quoted-printable) is descended and both parts decoded: Text holds
// the plain part (so search and the EAS plain-body projection prefer it), HTML
// holds the rich part (so Display renders it).
func TestParseMultipartAlternative(t *testing.T) {
	raw := []byte("Content-Type: multipart/alternative; boundary=BB\r\n\r\n" +
		"--BB\r\nContent-Type: text/plain\r\n\r\nPlain body here\r\n" +
		"--BB\r\nContent-Type: text/html\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\n" +
		"<p>Rich =E2=82=AC body</p>\r\n" +
		"--BB--\r\n")
	b := Parse(raw)
	if b.Text != "Plain body here" {
		t.Errorf("Text = %q, want %q", b.Text, "Plain body here")
	}
	if b.HTML != "<p>Rich € body</p>" {
		t.Errorf("HTML = %q, want quoted-printable decoded", b.HTML)
	}
	if got, isHTML := b.Display(); !isHTML || got != "<p>Rich € body</p>" {
		t.Errorf("Display() = (%q, %v), want the HTML part", got, isHTML)
	}
	if got := b.SearchText(); got != "Plain body here" {
		t.Errorf("SearchText() = %q, want the plain part %q", got, "Plain body here")
	}
}

// TestHTMLToText proves the search reduction drops script/style content and tags,
// unescapes entities, and collapses whitespace, so a query never matches markup
// or scripts the reader cannot see.
func TestHTMLToText(t *testing.T) {
	in := "<style>.x{color:red}</style><div>Hello&nbsp;<b>there</b>" +
		"<script>alert('x')</script>\n  world</div>"
	got := HTMLToText(in)
	want := "Hello there world"
	if got != want {
		t.Errorf("HTMLToText() = %q, want %q", got, want)
	}
}

// TestExtractPartCalendar proves the generic extractor reaches a non-body media
// type (text/calendar) nested in a multipart container, which the EAS calendar
// projection relies on.
func TestExtractPartCalendar(t *testing.T) {
	raw := []byte("Content-Type: multipart/mixed; boundary=CC\r\n\r\n" +
		"--CC\r\nContent-Type: text/plain\r\n\r\nignored\r\n" +
		"--CC\r\nContent-Type: text/calendar; method=REQUEST\r\n\r\nBEGIN:VCALENDAR\r\nEND:VCALENDAR\r\n" +
		"--CC--\r\n")
	got := ExtractPart(raw, "text/calendar")
	if got == nil {
		t.Fatal("ExtractPart(text/calendar) = nil, want the iCalendar part")
	}
	if want := "BEGIN:VCALENDAR"; !contains(string(got), want) {
		t.Errorf("ExtractPart = %q, want it to contain %q", got, want)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
