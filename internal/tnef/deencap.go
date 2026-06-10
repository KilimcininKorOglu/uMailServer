package tnef

import (
	"bytes"
)

// deEncapsulateHTML extracts the original HTML document encapsulated in an RTF
// stream, per MS-OXRTFEX. It returns ok=false when the RTF is not
// HTML-encapsulated (no \fromhtml among the leading tokens, a \fromtext stream,
// or ordinary RTF), so the caller can fall back to another body carrier.
//
// The HTML is reconstructed from two carriers interleaved in document order: the
// literal markup inside \*\htmltag destination groups, and the visible text
// outside them that is not suppressed by the \htmlrtf toggle. RTF-only regions
// (\htmlrtf ... \htmlrtf0), non-visible destinations (\fonttbl, \colortbl,
// \stylesheet, ...) and ignorable \* destinations other than \htmltag are
// skipped. RTF escapes and Unicode control words are converted back to text.
func deEncapsulateHTML(rtf []byte) (string, bool) {
	if !isEncapsulatedHTML(rtf) {
		return "", false
	}

	// group holds the de-encapsulation state that is saved on "{" and restored on
	// "}". A child group inherits its parent's suppression/destination state.
	type group struct {
		htmlrtf  bool // \htmlrtf suppression active
		inHTML   bool // inside an \*\htmltag destination (emit its content)
		ignore   bool // inside a skipped (non-visible / ignorable) destination
		ucskip   int  // \uc skip count for \u fallback characters
		pendStar bool // a \* was seen; the next destination word decides keep/skip
	}

	var out bytes.Buffer
	cur := group{ucskip: 1}
	var stack []group
	skip := 0 // remaining \u fallback characters to drop

	emit := func() bool { return !cur.ignore && (cur.inHTML || !cur.htmlrtf) }

	i, n := 0, len(rtf)
	for i < n {
		switch c := rtf[i]; c {
		case '{':
			stack = append(stack, cur)
			cur.pendStar = false
			skip = 0
			i++
		case '}':
			if len(stack) > 0 {
				cur = stack[len(stack)-1]
				stack = stack[:len(stack)-1]
			}
			skip = 0
			i++
		case '\r', '\n':
			// Raw line breaks are RTF framing, not content.
			i++
		case '\\':
			i++
			if i >= n {
				break
			}
			if d := rtf[i]; isRTFAlpha(d) {
				// Control word: letters, optional signed integer, optional one
				// space delimiter.
				j := i
				for j < n && isRTFAlpha(rtf[j]) {
					j++
				}
				word := string(rtf[i:j])
				k := j
				neg := false
				if k < n && rtf[k] == '-' {
					neg = true
					k++
				}
				ds := k
				param := 0
				for k < n && rtf[k] >= '0' && rtf[k] <= '9' {
					param = param*10 + int(rtf[k]-'0')
					k++
				}
				hasParam := k > ds
				if neg {
					param = -param
				}
				if k < n && rtf[k] == ' ' {
					k++
				}
				i = k
				skip = 0 // a control word ends any \u fallback run
				switch word {
				case "htmlrtf":
					cur.htmlrtf = !hasParam || param != 0
					cur.pendStar = false
				case "htmltag":
					cur.inHTML = true
					cur.pendStar = false
				case "mhtmltag":
					cur.ignore = true
					cur.pendStar = false
				case "fonttbl", "colortbl", "stylesheet", "listtable",
					"listoverridetable", "revtbl", "rsidtbl", "mmathPr", "info",
					"generator", "themedata", "colorschememapping", "datastore",
					"latentstyles", "pntext", "pntxta", "pntxtb":
					cur.ignore = true
					cur.pendStar = false
				case "par":
					if emit() {
						out.WriteString("\r\n")
					}
				case "tab":
					if emit() {
						out.WriteByte('\t')
					}
				case "lquote":
					writeRuneIf(&out, emit(), '‘')
				case "rquote":
					writeRuneIf(&out, emit(), '’')
				case "ldblquote":
					writeRuneIf(&out, emit(), '“')
				case "rdblquote":
					writeRuneIf(&out, emit(), '”')
				case "bullet":
					writeRuneIf(&out, emit(), '•')
				case "endash":
					writeRuneIf(&out, emit(), '–')
				case "emdash":
					writeRuneIf(&out, emit(), '—')
				case "uc":
					if hasParam && param >= 0 {
						cur.ucskip = param
					}
				case "u":
					r := param
					if r < 0 {
						r += 65536
					}
					if emit() && r > 0 {
						out.WriteRune(rune(r))
					}
					skip = cur.ucskip
				default:
					// An ignorable \* destination other than \htmltag is skipped;
					// any other control word is RTF formatting with no output.
					if cur.pendStar {
						cur.ignore = true
						cur.pendStar = false
					}
				}
			} else {
				// Control symbol.
				i++
				switch d {
				case '\'':
					if i+1 < n {
						hi, lo := hexVal(rtf[i]), hexVal(rtf[i+1])
						i += 2
						if hi >= 0 && lo >= 0 {
							if skip > 0 {
								skip--
							} else if emit() {
								out.WriteRune(cp1252Rune(byte(hi<<4 | lo)))
							}
						}
					} else {
						i = n
					}
				case '\\', '{', '}':
					if skip > 0 {
						skip--
					} else if emit() {
						out.WriteByte(d)
					}
				case '*':
					cur.pendStar = true
				case '~':
					writeRuneIf(&out, skip == 0 && emit(), ' ')
					if skip > 0 {
						skip--
					}
				case '_':
					writeRuneIf(&out, skip == 0 && emit(), '‑')
					if skip > 0 {
						skip--
					}
				}
				// '-' (optional hyphen), CR/LF and any other control symbol carry
				// no HTML output.
			}
		default:
			if skip > 0 {
				skip--
			} else if emit() {
				out.WriteByte(c)
			}
			i++
		}
	}

	html := out.String()
	if len(bytes.TrimSpace([]byte(html))) == 0 {
		return "", false
	}
	return html, true
}

// isEncapsulatedHTML reports whether rtf is an HTML-encapsulated RTF document per
// MS-OXRTFEX 2.2.3.1: it must begin with "{\rtf1" and carry the \fromhtml control
// word within the first 10 tokens (begin-group marks and control words), with no
// other token type appearing first.
func isEncapsulatedHTML(rtf []byte) bool {
	b := bytes.TrimLeft(rtf, " \t\r\n")
	if !bytes.HasPrefix(b, []byte(`{\rtf1`)) {
		return false
	}
	tokens, i, n := 0, 0, len(b)
	for i < n && tokens < 10 {
		switch c := b[i]; c {
		case ' ', '\t', '\r', '\n':
			i++
		case '{':
			tokens++
			i++
		case '\\':
			i++
			if i >= n || !isRTFAlpha(b[i]) {
				return false // a control symbol is not a control word
			}
			j := i
			for j < n && isRTFAlpha(b[j]) {
				j++
			}
			word := string(b[i:j])
			k := j
			if k < n && b[k] == '-' {
				k++
			}
			for k < n && b[k] >= '0' && b[k] <= '9' {
				k++
			}
			if k < n && b[k] == ' ' {
				k++
			}
			i = k
			tokens++
			switch word {
			case "fromhtml":
				return true
			case "fromtext":
				return false
			}
		default:
			return false // a token other than "{" or a control word
		}
	}
	return false
}

// writeRuneIf writes r to out only when cond is true.
func writeRuneIf(out *bytes.Buffer, cond bool, r rune) {
	if cond {
		out.WriteRune(r)
	}
}

// isRTFAlpha reports whether b is an ASCII letter (an RTF control-word character).
func isRTFAlpha(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

// hexVal returns the value of a hexadecimal digit, or -1 if b is not one.
func hexVal(b byte) int {
	switch {
	case b >= '0' && b <= '9':
		return int(b - '0')
	case b >= 'a' && b <= 'f':
		return int(b-'a') + 10
	case b >= 'A' && b <= 'F':
		return int(b-'A') + 10
	default:
		return -1
	}
}

// cp1252High maps the Windows-1252 bytes 0x80–0x9F to their Unicode runes; the
// five positions undefined in cp1252 map to their byte value (no data loss).
var cp1252High = [32]rune{
	0x20AC, 0x0081, 0x201A, 0x0192, 0x201E, 0x2026, 0x2020, 0x2021,
	0x02C6, 0x2030, 0x0160, 0x2039, 0x0152, 0x008D, 0x017D, 0x008F,
	0x0090, 0x2018, 0x2019, 0x201C, 0x201D, 0x2022, 0x2013, 0x2014,
	0x02DC, 0x2122, 0x0161, 0x203A, 0x0153, 0x009D, 0x017E, 0x0178,
}

// cp1252Rune decodes one Windows-1252 byte to a Unicode rune. A \'XX escape in
// encapsulated HTML carries a byte in the document code page, which is
// Windows-1252 in practice; true Unicode arrives via the \u control word.
func cp1252Rune(b byte) rune {
	if b >= 0x80 && b <= 0x9F {
		return cp1252High[b-0x80]
	}
	return rune(b)
}
