package tnef

import (
	"bytes"
	"testing"
)

// TestEncodeProducesTNEF: the encoded stream carries the TNEF signature.
func TestEncodeProducesTNEF(t *testing.T) {
	raw, err := Encode(&Message{BodyText: "hi"})
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if !IsTNEF(raw) {
		t.Error("Encode output is not recognized as TNEF")
	}
}

// TestEncodeRoundTripBody round-trips the plain-text and HTML body carriers.
func TestEncodeRoundTripBody(t *testing.T) {
	in := &Message{BodyText: "Merhaba dunya", BodyHTML: "<p>hello world</p>"}
	raw, err := Encode(in)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	out, _, err := Parse(raw)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if out.BodyText != in.BodyText {
		t.Errorf("BodyText = %q, want %q", out.BodyText, in.BodyText)
	}
	if out.BodyHTML != in.BodyHTML {
		t.Errorf("BodyHTML = %q, want %q", out.BodyHTML, in.BodyHTML)
	}
}

// TestEncodeRoundTripRTF round-trips PR_RTF_COMPRESSED through the verbatim
// ("MELA") form: the raw RTF bytes survive Encode -> Parse unchanged.
func TestEncodeRoundTripRTF(t *testing.T) {
	rtf := []byte("uncompressed-rtf-payload-bytes")
	raw, err := Encode(&Message{RTF: rtf})
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	out, _, err := Parse(raw)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if !bytes.Equal(out.RTF, rtf) {
		t.Errorf("RTF = %q, want %q", out.RTF, rtf)
	}
}

// TestEncodeRoundTripAttachments round-trips multiple attachments in order, with
// their filename, content type, and bytes preserved.
func TestEncodeRoundTripAttachments(t *testing.T) {
	in := &Message{
		BodyText: "see attached",
		Attachments: []Attachment{
			{Filename: "invoice.pdf", ContentType: "application/pdf", Data: []byte("%PDF-1.7 body")},
			{Filename: "Q3 rapor.txt", ContentType: "text/plain", Data: []byte("line one")},
		},
	}
	raw, err := Encode(in)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	out, _, err := Parse(raw)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(out.Attachments) != 2 {
		t.Fatalf("attachments = %d, want 2", len(out.Attachments))
	}
	for i := range in.Attachments {
		got, want := out.Attachments[i], in.Attachments[i]
		if got.Filename != want.Filename {
			t.Errorf("attachment %d filename = %q, want %q", i, got.Filename, want.Filename)
		}
		if got.ContentType != want.ContentType {
			t.Errorf("attachment %d contentType = %q, want %q", i, got.ContentType, want.ContentType)
		}
		if !bytes.Equal(got.Data, want.Data) {
			t.Errorf("attachment %d data = %q, want %q", i, got.Data, want.Data)
		}
	}
}

// TestEncodeRoundTripCombined exercises body + HTML + RTF + an attachment in one
// stream, the realistic export shape.
func TestEncodeRoundTripCombined(t *testing.T) {
	in := &Message{
		BodyText: "plain",
		BodyHTML: "<html><body>rich</body></html>",
		RTF:      []byte("rtf-bytes"),
		Attachments: []Attachment{
			{Filename: "a.bin", ContentType: "application/octet-stream", Data: []byte{0x00, 0x01, 0x02, 0xFF}},
		},
	}
	raw, err := Encode(in)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	out, _, err := Parse(raw)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if out.BodyText != in.BodyText || out.BodyHTML != in.BodyHTML {
		t.Errorf("body mismatch: text=%q html=%q", out.BodyText, out.BodyHTML)
	}
	if !bytes.Equal(out.RTF, in.RTF) {
		t.Errorf("RTF = %q, want %q", out.RTF, in.RTF)
	}
	if len(out.Attachments) != 1 || !bytes.Equal(out.Attachments[0].Data, in.Attachments[0].Data) {
		t.Errorf("attachment round-trip failed: %+v", out.Attachments)
	}
}

// TestEncodeNilMessage: a nil message is a usage error, not a panic.
func TestEncodeNilMessage(t *testing.T) {
	if _, err := Encode(nil); err == nil {
		t.Error("Encode(nil) should return an error")
	}
}
