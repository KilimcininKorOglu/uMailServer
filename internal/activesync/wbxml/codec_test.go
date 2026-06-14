package wbxml

import (
	"bytes"
	"errors"
	"reflect"
	"testing"
)

// TestMultiByteUint anchors the mb_u_int32 codec on the canonical WBXML 1.3
// encodings (WBXML 1.3 §5.1.1): 7 bits per byte, most-significant group first,
// continuation bit on every byte but the last. The expected bytes are the
// spec's own worked values, not produced by our encoder.
func TestMultiByteUint(t *testing.T) {
	cases := []struct {
		v    uint32
		want []byte
	}{
		{0, []byte{0x00}},
		{127, []byte{0x7F}},
		{128, []byte{0x81, 0x00}},
		{16383, []byte{0xFF, 0x7F}},
		{16384, []byte{0x81, 0x80, 0x00}},
		{0xA0, []byte{0x81, 0x20}}, // 160, the classic WBXML spec example
	}
	for _, c := range cases {
		got := appendMultiByteUint(nil, c.v)
		if !bytes.Equal(got, c.want) {
			t.Errorf("appendMultiByteUint(%d) = % x, want % x", c.v, got, c.want)
		}
		d := &decoder{b: c.want}
		back, err := d.multiByteUint()
		if err != nil {
			t.Errorf("multiByteUint(% x): %v", c.want, err)
		}
		if back != c.v {
			t.Errorf("multiByteUint(% x) = %d, want %d", c.want, back, c.v)
		}
		if d.off != len(c.want) {
			t.Errorf("multiByteUint(% x) consumed %d bytes, want %d", c.want, d.off, len(c.want))
		}
	}
}

// TestMultiByteUintOverflow rejects an integer wider than 32 bits rather than
// silently wrapping (a malformed length must not become a valid one).
func TestMultiByteUintOverflow(t *testing.T) {
	d := &decoder{b: []byte{0x8F, 0xFF, 0xFF, 0xFF, 0xFF, 0x7F}}
	if _, err := d.multiByteUint(); !errors.Is(err, ErrIntOverflow) {
		t.Fatalf("expected ErrIntOverflow, got %v", err)
	}
}

// TestMarshalByteExact pins the full wire encoding of a Sync envelope carrying
// an Email Subject. The expected bytes are derived from the published MS-ASWBXML
// tokens (Email Subject = 0x14, cross-checked against the spec) and the WBXML
// header/global tokens, so a regression in the engine — header, content flag,
// SWITCH_PAGE, STR_I framing — fails here.
func TestMarshalByteExact(t *testing.T) {
	doc := &Element{Page: PageAirSync, Name: "Sync", Children: []*Element{
		{Page: PageEmail, Name: "Subject", Text: "Hi"},
	}}
	got, err := Marshal(doc)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	want := []byte{
		0x03, 0x01, 0x6A, 0x00, // WBXML 1.3, public id 1, UTF-8, empty string table
		0x45,             // Sync (0x05) | content flag (0x40)
		0x00, 0x02,       // SWITCH_PAGE to Email (page 2)
		0x54,             // Subject (0x14) | content flag
		0x03, 'H', 'i', 0x00, // STR_I "Hi"
		0x01, // END Subject
		0x01, // END Sync
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("Marshal bytes:\n got % x\nwant % x", got, want)
	}
}

// TestRoundTrip verifies that a realistic, nested Sync document survives
// Marshal -> Unmarshal unchanged, including a page switch into Email, text,
// nested elements and an empty element.
func TestRoundTrip(t *testing.T) {
	doc := &Element{Page: PageAirSync, Name: "Sync", Children: []*Element{
		{Page: PageAirSync, Name: "Collections", Children: []*Element{
			{Page: PageAirSync, Name: "Collection", Children: []*Element{
				{Page: PageAirSync, Name: "SyncKey", Text: "1"},
				{Page: PageAirSync, Name: "CollectionId", Text: "inbox"},
				{Page: PageAirSync, Name: "Status", Text: "1"},
				{Page: PageAirSync, Name: "Commands", Children: []*Element{
					{Page: PageAirSync, Name: "Add", Children: []*Element{
						{Page: PageAirSync, Name: "ServerId", Text: "1:42"},
						{Page: PageAirSync, Name: "ApplicationData", Children: []*Element{
							{Page: PageEmail, Name: "Subject", Text: "Quarterly report"},
							{Page: PageEmail, Name: "From", Text: "alice@example.test"},
							{Page: PageEmail, Name: "Read", Text: "0"},
							{Page: PageEmail, Name: "DisallowNewTimeProposal"}, // empty element
						}},
					}},
				}},
			}},
		}},
	}}

	encoded, err := Marshal(doc)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	decoded, err := Unmarshal(encoded)
	if err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if !reflect.DeepEqual(doc, decoded) {
		t.Fatalf("round trip mismatch:\n in: %+v\nout: %+v", doc, decoded)
	}
}

// TestOpaqueRoundTrip exercises OPAQUE-framed binary content (used by MIME and
// attachment payloads), which must survive the length-prefixed encoding byte
// for byte.
func TestOpaqueRoundTrip(t *testing.T) {
	raw := make([]byte, 300) // forces a 2-byte mb_u_int32 length
	for i := range raw {
		raw[i] = byte(i)
	}
	doc := &Element{Page: PageAirSync, Name: "Sync", Children: []*Element{
		{Page: PageEmail, Name: "MIMEData", Opaque: raw},
	}}
	encoded, err := Marshal(doc)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	decoded, err := Unmarshal(encoded)
	if err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	mime := decoded.Sub("MIMEData")
	if mime == nil || !bytes.Equal(mime.Opaque, raw) {
		t.Fatalf("opaque payload not preserved")
	}
}

// TestRejectAttributes ensures an element token carrying the attribute flag is
// rejected: ActiveSync never emits attributes, so seeing one is a malformed or
// hostile document, not something to silently accept.
func TestRejectAttributes(t *testing.T) {
	// Header + a Sync tag with the attribute flag (0x80) set.
	b := []byte{0x03, 0x01, 0x6A, 0x00, 0x05 | tagAttrFlag}
	if _, err := Unmarshal(b); !errors.Is(err, ErrAttrsPresent) {
		t.Fatalf("expected ErrAttrsPresent, got %v", err)
	}
}

// TestUnknownToken rejects a tag token that no code page defines, rather than
// inventing an element.
func TestUnknownToken(t *testing.T) {
	// 0x3A is unassigned in AirSync (page 0 tops out at 0x29).
	b := []byte{0x03, 0x01, 0x6A, 0x00, 0x3A}
	if _, err := Unmarshal(b); !errors.Is(err, ErrUnknownToken) {
		t.Fatalf("expected ErrUnknownToken, got %v", err)
	}
}

// TestTruncatedString rejects an inline string with no terminating NUL.
func TestTruncatedString(t *testing.T) {
	b := []byte{0x03, 0x01, 0x6A, 0x00, 0x45, 0x03, 'H', 'i'} // STR_I never terminated
	if _, err := Unmarshal(b); !errors.Is(err, ErrTruncated) {
		t.Fatalf("expected ErrTruncated, got %v", err)
	}
}

// TestCodePageTokens pins representative token values against the published
// MS-ASWBXML tables, and asserts the absence of the 2.5-era tokens an earlier
// hand-keyed table wrongly carried. This is the regression that a symmetric
// round-trip cannot catch: a wrong token value round-trips fine but fails real
// clients, so the tables are anchored on the spec's own assignments here.
func TestCodePageTokens(t *testing.T) {
	present := []struct {
		page byte
		name string
		tok  byte
	}{
		{PageAirSync, "Sync", 0x05},
		{PageAirSync, "Status", 0x0E},
		{PageAirSync, "CollectionId", 0x12},
		{PageAirSync, "HeartbeatInterval", 0x29},
		{PageEmail, "Subject", 0x14},
		{PageEmail, "From", 0x18},
		{PageEmail, "DisallowNewTimeProposal", 0x3F},
		{PageGetItemEstimate, "GetItemEstimate", 0x05},
		{PageGetItemEstimate, "Estimate", 0x0C},
		{PageFolderHierarchy, "FolderSync", 0x16}, // not 0x16=Count, a prior mis-key
		{PageFolderHierarchy, "Delete", 0x10},     // not "Remove"
		{PageFolderHierarchy, "Count", 0x17},
		{PageProvision, "Provision", 0x05},
		{PageProvision, "PolicyKey", 0x09},
		{PageProvision, "EASProvisionDoc", 0x0D},
		{PageAirSyncBase, "BodyPreference", 0x05},
		{PageAirSyncBase, "Body", 0x0A},
		{PageAirSyncBase, "Data", 0x0B},
	}
	for _, c := range present {
		cp, ok := codePage(c.page)
		if !ok {
			t.Errorf("code page %d not registered", c.page)
			continue
		}
		if got, ok := cp.token(c.name); !ok || got != c.tok {
			t.Errorf("page %d %q token = 0x%02x (ok=%v), want 0x%02x", c.page, c.name, got, ok, c.tok)
		}
		if got, ok := cp.name(c.tok); !ok || got != c.name {
			t.Errorf("page %d 0x%02x name = %q (ok=%v), want %q", c.page, c.tok, got, ok, c.name)
		}
	}

	// These tokens are NOT in the current MS-ASWBXML tables; an earlier table
	// wrongly included them. Their absence is the fix.
	absent := []struct {
		page byte
		name string
	}{
		{PageAirSync, "Version"},
		{PageAirSync, "RtfTruncation"},
		{PageAirSync, "NotifyGUID"},
		{PageEmail, "AttRemoved"},
	}
	for _, c := range absent {
		cp, _ := codePage(c.page)
		if _, ok := cp.token(c.name); ok {
			t.Errorf("page %d wrongly defines %q (spec omits it)", c.page, c.name)
		}
	}
}
