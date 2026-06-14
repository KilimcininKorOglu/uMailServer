// Package wbxml implements the WBXML (WAP Binary XML) encoding that Exchange
// ActiveSync uses on the wire, as specified in MS-ASWBXML over the WBXML 1.3
// token format (WAP-192). ActiveSync bodies are WBXML-encoded XML: a document
// is a tree of elements drawn from numbered code pages (one code page per
// ActiveSync XML namespace, e.g. AirSync, Email, FolderHierarchy), where each
// element name maps to a single-byte token within its code page.
//
// The codec is deliberately small: ActiveSync uses only a fixed header, tag
// tokens (with an optional content flag), inline NUL-terminated strings
// (STR_I), opaque length-prefixed binary (OPAQUE), code-page switches
// (SWITCH_PAGE) and the END terminator. ActiveSync does not use WBXML
// attributes, the string table, processing instructions or entities, so those
// are decoded defensively but never emitted.
//
// The element/name <-> token mapping lives in codepages.go; this file is the
// transport engine that turns an Element tree into bytes and back.
package wbxml

import (
	"errors"
	"fmt"
)

// WBXML header bytes ActiveSync always uses (MS-ASWBXML 2.1.2.1.2):
// version 1.3, public identifier 1 ("unknown"), charset UTF-8 (IANA MIBenum
// 106 = 0x6A), and an empty string table.
const (
	wbxmlVersion13 byte = 0x03
	publicIDUnknown byte = 0x01
	charsetUTF8     byte = 0x6A // IANA MIBenum 106
)

// Global WBXML tokens (WBXML 1.3 §5.8.4.1), shared across every code page.
const (
	gtSwitchPage byte = 0x00 // followed by one code-page byte
	gtEnd        byte = 0x01 // closes an element opened with the content flag
	gtEntity     byte = 0x02 // ENTITY: mb_u_int32 character code
	gtStrI       byte = 0x03 // inline string, NUL-terminated UTF-8
	gtStrT       byte = 0x83 // string-table reference: mb_u_int32 offset
	gtOpaque     byte = 0xC3 // OPAQUE: mb_u_int32 length + raw bytes
)

// Tag token flag bits (WBXML 1.3 §5.8.4.2). The low 6 bits identify the tag
// within the active code page; ActiveSync never sets the attribute flag.
const (
	tagContentFlag byte = 0x40 // element has children, text or opaque content
	tagAttrFlag    byte = 0x80 // element has attributes (unused by ActiveSync)
	tagCodeMask    byte = 0x3F // tag identity within the code page
)

// Errors returned by Unmarshal for malformed input.
var (
	ErrTruncated     = errors.New("wbxml: truncated input")
	ErrUnknownToken  = errors.New("wbxml: unknown tag token")
	ErrUnknownPage   = errors.New("wbxml: unknown code page")
	ErrAttrsPresent  = errors.New("wbxml: attributes are not supported by ActiveSync")
	ErrIntOverflow   = errors.New("wbxml: multi-byte integer overflows uint32")
	ErrEmptyDocument = errors.New("wbxml: document has no root element")
)

// Element is one node of an ActiveSync WBXML document. Page is the code-page
// number the element belongs to (its XML namespace); Name is the element name
// resolved within that page. An element carries at most one of Text (encoded as
// an inline STR_I) or Opaque (encoded as OPAQUE); Children holds nested
// elements. A leaf with no Text, Opaque or Children is an empty element.
type Element struct {
	Page     byte
	Name     string
	Text     string
	Opaque   []byte
	Children []*Element
}

// Sub returns the first direct child named name (in any code page), or nil.
// It is a small convenience for handlers walking a decoded request.
func (e *Element) Sub(name string) *Element {
	for _, c := range e.Children {
		if c.Name == name {
			return c
		}
	}
	return nil
}

// Marshal serializes an Element tree into an ActiveSync WBXML document,
// emitting the fixed header followed by the encoded root. It writes a
// SWITCH_PAGE only when the active code page actually changes, starting from
// the root element's page.
func Marshal(root *Element) ([]byte, error) {
	if root == nil {
		return nil, ErrEmptyDocument
	}
	buf := []byte{wbxmlVersion13, publicIDUnknown, charsetUTF8, 0x00}
	// 0x00 above is the empty string-table length.
	page := root.Page
	if _, ok := codePage(page); !ok {
		return nil, fmt.Errorf("%w: %d", ErrUnknownPage, page)
	}
	out, err := encodeElement(buf, root, &page, true)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// encodeElement appends the WBXML encoding of e (and its subtree) to buf,
// switching the active code page when needed. cur tracks the active page across
// the recursion; isRoot lets the root emit its own SWITCH_PAGE relative to the
// header's implicit page 0.
func encodeElement(buf []byte, e *Element, cur *byte, isRoot bool) ([]byte, error) {
	cp, ok := codePage(e.Page)
	if !ok {
		return nil, fmt.Errorf("%w: %d", ErrUnknownPage, e.Page)
	}
	token, ok := cp.token(e.Name)
	if !ok {
		return nil, fmt.Errorf("wbxml: element %q not in code page %d", e.Name, e.Page)
	}
	// Switch code page when the element's page differs from the active one, or
	// when the root sits on a non-zero page (the header leaves page 0 active).
	if e.Page != *cur || (isRoot && e.Page != 0) {
		buf = append(buf, gtSwitchPage, e.Page)
		*cur = e.Page
	}

	hasContent := e.Text != "" || len(e.Opaque) > 0 || len(e.Children) > 0
	if hasContent {
		buf = append(buf, token|tagContentFlag)
	} else {
		buf = append(buf, token)
		return buf, nil
	}

	switch {
	case len(e.Opaque) > 0:
		buf = append(buf, gtOpaque)
		buf = appendMultiByteUint(buf, uint32(len(e.Opaque)))
		buf = append(buf, e.Opaque...)
	case e.Text != "":
		buf = append(buf, gtStrI)
		buf = append(buf, []byte(e.Text)...)
		buf = append(buf, 0x00)
	}

	var err error
	for _, child := range e.Children {
		buf, err = encodeElement(buf, child, cur, false)
		if err != nil {
			return nil, err
		}
	}
	buf = append(buf, gtEnd)
	return buf, nil
}

// Unmarshal parses an ActiveSync WBXML document into its root Element. It skips
// the fixed header (tolerating any string-table length, which it then steps
// over) and decodes the single root element and its subtree.
func Unmarshal(b []byte) (*Element, error) {
	d := &decoder{b: b}
	if err := d.header(); err != nil {
		return nil, err
	}
	root, err := d.element(0)
	if err != nil {
		return nil, err
	}
	if root == nil {
		return nil, ErrEmptyDocument
	}
	return root, nil
}

// decoder holds the cursor and active code page while reading a WBXML document.
type decoder struct {
	b    []byte
	off  int
	page byte
}

// header consumes the WBXML version, public identifier, charset and string
// table at the start of the document. The public id and charset are read as
// multi-byte integers per WBXML 1.3; the string table is skipped wholesale.
func (d *decoder) header() error {
	if len(d.b) < 1 {
		return ErrTruncated
	}
	d.off++ // version
	if _, err := d.multiByteUint(); err != nil {
		return err // public identifier
	}
	if _, err := d.multiByteUint(); err != nil {
		return err // charset
	}
	strTblLen, err := d.multiByteUint()
	if err != nil {
		return err
	}
	if d.off+int(strTblLen) > len(d.b) {
		return ErrTruncated
	}
	d.off += int(strTblLen)
	return nil
}

// element decodes the next element on the given code page, recursing through
// SWITCH_PAGE and nested elements until the element's END (or, for the root,
// end of input). It returns nil only when there is no element to read.
func (d *decoder) element(page byte) (*Element, error) {
	d.page = page
	for {
		if d.off >= len(d.b) {
			return nil, nil
		}
		tok := d.b[d.off]
		if tok == gtSwitchPage {
			if d.off+1 >= len(d.b) {
				return nil, ErrTruncated
			}
			d.page = d.b[d.off+1]
			d.off += 2
			continue
		}
		break
	}

	tok := d.b[d.off]
	d.off++
	if tok&tagAttrFlag != 0 {
		return nil, ErrAttrsPresent
	}
	cp, ok := codePage(d.page)
	if !ok {
		return nil, fmt.Errorf("%w: %d", ErrUnknownPage, d.page)
	}
	name, ok := cp.name(tok & tagCodeMask)
	if !ok {
		return nil, fmt.Errorf("%w: 0x%02x on page %d", ErrUnknownToken, tok&tagCodeMask, d.page)
	}
	el := &Element{Page: d.page, Name: name}
	if tok&tagContentFlag == 0 {
		return el, nil // empty element
	}

	if err := d.content(el); err != nil {
		return nil, err
	}
	return el, nil
}

// content reads an opened element's body — inline string, opaque bytes and/or
// nested elements — up to and including the matching END token.
func (d *decoder) content(el *Element) error {
	page := d.page
	for {
		if d.off >= len(d.b) {
			return ErrTruncated
		}
		switch d.b[d.off] {
		case gtEnd:
			d.off++
			return nil
		case gtSwitchPage:
			if d.off+1 >= len(d.b) {
				return ErrTruncated
			}
			page = d.b[d.off+1]
			d.off += 2
		case gtStrI:
			d.off++
			s, err := d.cstring()
			if err != nil {
				return err
			}
			el.Text += s
		case gtOpaque:
			d.off++
			n, err := d.multiByteUint()
			if err != nil {
				return err
			}
			if d.off+int(n) > len(d.b) {
				return ErrTruncated
			}
			el.Opaque = append(el.Opaque, d.b[d.off:d.off+int(n)]...)
			d.off += int(n)
		case gtStrT:
			// String-table reference. ActiveSync uses an empty string table, so
			// a reference cannot resolve; consume the index and ignore it.
			d.off++
			if _, err := d.multiByteUint(); err != nil {
				return err
			}
		case gtEntity:
			d.off++
			if _, err := d.multiByteUint(); err != nil {
				return err
			}
		default:
			child, err := d.element(page)
			if err != nil {
				return err
			}
			if child == nil {
				return ErrTruncated
			}
			el.Children = append(el.Children, child)
			page = d.page
		}
	}
}

// cstring reads a NUL-terminated UTF-8 inline string starting at the cursor.
func (d *decoder) cstring() (string, error) {
	start := d.off
	for d.off < len(d.b) {
		if d.b[d.off] == 0x00 {
			s := string(d.b[start:d.off])
			d.off++
			return s, nil
		}
		d.off++
	}
	return "", ErrTruncated
}

// multiByteUint reads a WBXML multi-byte unsigned integer (mb_u_int32): 7 bits
// per byte, most-significant group first, continuation bit set on every byte
// except the last (WBXML 1.3 §5.1.1).
func (d *decoder) multiByteUint() (uint32, error) {
	var v uint32
	for n := 0; ; n++ {
		if d.off >= len(d.b) {
			return 0, ErrTruncated
		}
		if n == 5 {
			return 0, ErrIntOverflow
		}
		c := d.b[d.off]
		d.off++
		v = (v << 7) | uint32(c&0x7F)
		if c&0x80 == 0 {
			return v, nil
		}
	}
}

// appendMultiByteUint appends v as a WBXML multi-byte unsigned integer.
func appendMultiByteUint(buf []byte, v uint32) []byte {
	var tmp [5]byte
	i := len(tmp)
	i--
	tmp[i] = byte(v & 0x7F)
	for v >>= 7; v > 0; v >>= 7 {
		i--
		tmp[i] = byte(v&0x7F) | 0x80
	}
	return append(buf, tmp[i:]...)
}
