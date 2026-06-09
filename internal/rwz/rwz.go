// Package rwz reads and writes Microsoft Outlook "Rules Wizard" export files
// (.rwz) and maps them to the canonical inbox-rule model (semcore.Rule), so the
// webmail filter UI can import rules exported from Outlook and export its own
// filters in a form Outlook can import.
//
// # Format
//
// The byte layout is the Outlook 2016/2019/2021 (and Microsoft 365) rules
// stream. It is grounded in two independent reverse-engineered sources, which
// agree byte-for-byte for this version:
//
//   - a published Outlook rule-format specification (specification)
//   - the reference parser (a working TypeScript parser, with byte-level
//     test constructors in the documented Outlook rule layout)
//
// The writer mirrors the reference parser's layout exactly, so its output round-trips
// through this package's reader and parses with the independent the reference parser.
//
// # Verification limitation (read this)
//
// This package is verified by (a) round-trip (Write then Parse yields equal
// rules) and (b) cross-compatibility (our output parses with the reference parser). It is
// NOT verified against a real Microsoft Outlook: no Outlook is available in this
// environment and there is no genuine .rwz fixture (both reference sources are
// themselves reverse-engineered). Real-Outlook interoperability is therefore
// best-effort for Outlook 2016+ and not guaranteed.
//
// # Scope
//
// Only the Outlook 2016+ stream is written. On import, every element shape in
// the reference vocabulary is consumed (the format has no per-element length
// prefix, so an unrecognized element would desync the stream); a common subset
// is mapped to canonical conditions/actions and the rest is counted in Report.
package rwz

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"

	"github.com/umailserver/umailserver/internal/semcore"
)

// MAPI property type tags (MS-OXCDATA), used inside a PropertyValueArray.
const (
	ptypInteger32 = 0x0003
	ptypErrorCode = 0x000a
	ptypBoolean   = 0x000b
	ptypString8   = 0x001e
	ptypString    = 0x001f
)

// MAPI property ids relevant to a recipient entry.
const (
	pidRecipientType = 0x0c15
	pidDisplayName   = 0x3001
	pidAddressType   = 0x3002
	pidEmailAddress  = 0x3003
	pidSmtpAddress   = 0x39fe
)

// recipientTypeTo is PR_RECIPIENT_TYPE == MAPI_TO (1).
const recipientTypeTo = 1

// File-level magic for the Outlook 2016+ rules stream.
const (
	rwSignature2019 = 0x00140000
	rwFlags2019     = 0x06140000
	ruleClassName   = "CRuleElement"
)

// Element ids we map to/from the canonical model (Exchange condition/action codes).
const (
	elemUnknown64           = 0x64  // mandatory, no UI meaning
	elemApplyRule           = 0x190 // mandatory: receive vs send marker
	elemFrom                = 0xcb
	elemTo                  = 0xcc
	elemSubject             = 0xcd
	elemBody                = 0xce
	elemSubjectOrBody       = 0xcf
	elemSize                = 0xe0
	elemMove                = 0x12c
	elemDeleteSoft          = 0x12d
	elemForward             = 0x12e
	elemSetImportance       = 0x137
	elemCopy                = 0x139
	elemStop                = 0x142
	elemRedirect            = 0x144
	elemForwardAsAttachment = 0x147
	elemDeleteHard          = 0x14a
	elemMarkRead            = 0x14c
)

// applyFlagReceive is the 0x190 flag value meaning "after the message arrives".
const applyFlagReceive = 0x1

// importanceHigh is the 0x137 importance enum value for High.
const importanceHigh = 2

// Report records lossy outcomes of a conversion so the caller can surface them
// (Rule 10: never drop data silently).
type Report struct {
	// SkippedRules counts whole rules that could not be represented.
	SkippedRules int
	// SkippedElements counts individual conditions/actions that could not be
	// represented (on export) or mapped to the canonical model (on import).
	SkippedElements int
	// Notes holds human-readable degradation messages (e.g. match-type downgrades).
	Notes []string
}

func (rep *Report) note(format string, args ...any) {
	rep.Notes = append(rep.Notes, fmt.Sprintf(format, args...))
}

// ---------------------------------------------------------------------------
// Parse: .rwz bytes -> canonical rules
// ---------------------------------------------------------------------------

// Parse decodes an Outlook 2016+ .rwz byte stream into canonical rules. Each
// returned rule has only Name/Enabled/MatchAll/Conditions/Actions populated
// (identity fields are left zero for the caller to assign).
func Parse(b []byte) ([]*semcore.Rule, Report, error) {
	var rep Report
	r := newReader(b)
	nRules, err := readHeader(r)
	if err != nil {
		return nil, rep, err
	}
	rules := make([]*semcore.Rule, 0, nRules)
	for i := 0; i < nRules; i++ {
		rule, err := readRule(r, &rep)
		if err != nil {
			return nil, rep, err
		}
		rules = append(rules, rule)
		if i != nRules-1 {
			if sep := r.u16(); sep != 0 {
				return nil, rep, fmt.Errorf("rwz: bad inter-rule separator 0x%x", sep)
			}
		}
	}
	if r.err != nil {
		return nil, rep, r.err
	}
	// The footer (template dir + timestamp) is advisory; ignore parse errors.
	return rules, rep, nil
}

// readHeader parses the 48-byte Outlook 2016+ file header and returns the rule
// count. Only this version's signature is accepted.
func readHeader(r *reader) (int, error) {
	sig := r.u32()
	if sig != rwSignature2019 {
		return 0, fmt.Errorf("rwz: unsupported version (signature 0x%x; only Outlook 2016+ is supported)", sig)
	}
	_ = r.u32() // flags
	for i := 0; i < 8; i++ {
		_ = r.u32() // unknown[1..8]
	}
	_ = r.u32() // unknown[9] (Outlook 2002+)
	n := r.u16()
	_ = r.u16() // extra
	if r.err != nil {
		return 0, r.err
	}
	return int(n), nil
}

// readRule parses one rule header plus its elements.
func readRule(r *reader, rep *Report) (*semcore.Rule, error) {
	_ = r.u16() // signature 0x0001
	name := r.stringObject()
	enabled := r.u32() != 0
	for i := 0; i < 4; i++ {
		_ = r.u32() // unknown[0..3]
	}
	_ = r.u32() // dataSize (recomputed on write; not validated here)
	nElems := int(r.u16())
	sep := r.u16()
	switch sep {
	case 0xffff:
		_ = r.u16()               // padding
		clsLen := int(r.u16())    // class name length
		_ = r.asciiString(clsLen) // "CRuleElement"
	case 0x8001, 0x0000:
		// 0x8001 for non-first rules; 0 tolerated (matches the reference parser).
	default:
		return nil, fmt.Errorf("rwz: bad rule separator 0x%x", sep)
	}

	rule := &semcore.Rule{Name: name, Enabled: enabled, MatchAll: true}
	for i := 0; i < nElems && r.err == nil; i++ {
		id := r.u32()
		cond, action, status := decodeElement(r, id, rep)
		switch status {
		case statusUnknown:
			return nil, fmt.Errorf("rwz: unsupported rule element 0x%x", id)
		case statusMapped:
			if cond != nil {
				rule.Conditions = append(rule.Conditions, *cond)
			}
			if action != nil {
				rule.Actions = append(rule.Actions, *action)
			}
		case statusSkipped:
			rep.SkippedElements++
		case statusMarker:
			// 0x190 / 0x64: structural, nothing to map.
		}
		if i != nElems-1 {
			if esep := r.u16(); esep != 0x8001 {
				return nil, fmt.Errorf("rwz: bad element separator 0x%x at element %d", esep, i)
			}
		}
	}
	if r.err != nil {
		return nil, r.err
	}
	return rule, nil
}

// elemStatus classifies how a parsed element was handled.
type elemStatus int

const (
	statusUnknown elemStatus = iota // id not in the known vocabulary: cannot advance
	statusMarker                    // structural marker (0x190/0x64)
	statusMapped                    // mapped to a canonical condition/action
	statusSkipped                   // recognized shape, no canonical equivalent
)

// decodeElement consumes one element's payload (advancing r past it) and, for
// the supported subset, returns the canonical condition/action it maps to.
// Every element shape in the reference vocabulary is consumed so the stream
// never desyncs; ids outside the vocabulary return statusUnknown.
func decodeElement(r *reader, id uint32, rep *Report) (*semcore.RuleCondition, *semcore.RuleAction, elemStatus) {
	switch id {
	// --- mandatory markers ---
	case elemUnknown64, elemApplyRule:
		consumeExtResU32(r)
		return nil, nil, statusMarker

	// --- mapped conditions ---
	case elemSubject, elemBody, elemSubjectOrBody:
		words := consumeStringsList(r)
		kind := semcore.RuleConditionKindSubject
		if id == elemBody {
			kind = semcore.RuleConditionKindBody
		}
		if len(words) == 0 {
			return nil, nil, statusSkipped
		}
		if len(words) > 1 {
			rep.note("condition kept only the first of %d words", len(words))
		}
		return &semcore.RuleCondition{Kind: kind, MatchType: semcore.RuleMatchTypeContains, Value: words[0]}, nil, statusMapped
	case elemFrom, elemTo:
		addrs := consumePeopleList(r)
		kind := semcore.RuleConditionKindFrom
		if id == elemTo {
			kind = semcore.RuleConditionKindTo
		}
		if len(addrs) == 0 || addrs[0] == "" {
			return nil, nil, statusSkipped
		}
		if len(addrs) > 1 {
			rep.note("condition kept only the first of %d addresses", len(addrs))
		}
		return &semcore.RuleCondition{Kind: kind, MatchType: semcore.RuleMatchTypeContains, Value: addrs[0]}, nil, statusMapped
	case elemSize:
		minKB, maxKB := consumeSize(r)
		val := minMaxToSizeValue(minKB, maxKB)
		if val == "" {
			return nil, nil, statusSkipped
		}
		if minKB > 0 && maxKB > 0 {
			rep.note("size condition kept the lower bound only")
		}
		return &semcore.RuleCondition{Kind: semcore.RuleConditionKindSize, MatchType: semcore.RuleMatchTypeContains, Value: val}, nil, statusMapped

	// --- mapped actions ---
	case elemMove, elemCopy:
		folder := consumeMoveToFolder(r)
		kind := semcore.RuleActionKindMoveToFolder
		if id == elemCopy {
			kind = semcore.RuleActionKindCopyToFolder
		}
		return nil, &semcore.RuleAction{Kind: kind, Target: folder}, statusMapped
	case elemDeleteSoft, elemDeleteHard:
		consumeSimple(r)
		return nil, &semcore.RuleAction{Kind: semcore.RuleActionKindDelete}, statusMapped
	case elemMarkRead:
		consumeSimple(r)
		return nil, &semcore.RuleAction{Kind: semcore.RuleActionKindMarkRead}, statusMapped
	case elemStop:
		consumeSimple(r)
		return nil, &semcore.RuleAction{Kind: semcore.RuleActionKindStop}, statusMapped
	case elemSetImportance:
		consumeExtResU32(r) // importance enum ignored: canonical only flags importance
		return nil, &semcore.RuleAction{Kind: semcore.RuleActionKindMarkImportant}, statusMapped
	case elemForward, elemForwardAsAttachment, elemRedirect:
		addrs := consumePeopleList(r)
		kind := semcore.RuleActionKindForward
		switch id {
		case elemForwardAsAttachment:
			kind = semcore.RuleActionKindForwardAsAttachment
		case elemRedirect:
			kind = semcore.RuleActionKindRedirect
		}
		if len(addrs) == 0 || addrs[0] == "" {
			return nil, nil, statusSkipped
		}
		if len(addrs) > 1 {
			rep.note("action kept only the first of %d addresses", len(addrs))
		}
		return nil, &semcore.RuleAction{Kind: kind, ForwardTo: addrs[0]}, statusMapped

	// --- recognized but unmapped: consume by shape ---
	case 0xc8, 0xc9, 0xca, 0xdc, 0xde, 0xe2, 0xe3, 0xe9, 0xeb, 0xec, 0xf1, 0xf2, 0xf3, 0xf6, 0xf7,
		0x132, 0x13a, 0x13b, 0x143, 0x145, 0x148, 0x14f, 0x150, 0x152,
		0x1f4, 0x1f5, 0x1f6, 0x208, 0x20a, 0x20e, 0x20f, 0x216, 0x217, 0x21a, 0x21b:
		consumeSimple(r)
		return nil, nil, statusSkipped
	case 0xd2, 0xd3, 0x138, 0x13e, 0x1fe, 0x1ff:
		consumeExtResU32(r)
		return nil, nil, statusSkipped
	case 0xed, 0x20c:
		consumeSize(r)
		return nil, nil, statusSkipped
	case 0xd0, 0xd7, 0x133, 0x203, 0xee, 0x214, 0xf0, 0x215, 0x12f, 0x136, 0x149, 0x130, 0x1fc:
		consumeExtResString(r)
		return nil, nil, statusSkipped
	case 0xe5, 0xe6, 0xe8, 0xf5, 0x1f9, 0x1fa, 0x1fb, 0x211, 0x212, 0x213, 0x219:
		consumeStringsList(r)
		return nil, nil, statusSkipped
	case 0x13c, 0x1f7, 0x1f8:
		consumePeopleList(r)
		return nil, nil, statusSkipped
	case 0xe4, 0xf4, 0x210, 0x218:
		consumeFormType(r)
		return nil, nil, statusSkipped
	case 0xdf, 0x20b:
		consumeWithSelectedProps(r)
		return nil, nil, statusSkipped
	case 0xe1, 0x20d:
		consumeReceivedDateSpan(r)
		return nil, nil, statusSkipped
	case 0xef:
		consumeOnThisComputer(r)
		return nil, nil, statusSkipped
	case 0x131:
		consumeFlag(r)
		return nil, nil, statusSkipped
	case 0x13f:
		consumePerformCustomAction(r)
		return nil, nil, statusSkipped
	case 0x146:
		consumeAutomaticReply(r)
		return nil, nil, statusSkipped
	case 0x14b:
		consumeRunScript(r)
		return nil, nil, statusSkipped
	case 0x151:
		consumeFlagForFollowUp(r)
		return nil, nil, statusSkipped
	case 0x153:
		consumeApplyRetention(r)
		return nil, nil, statusSkipped
	default:
		return nil, nil, statusUnknown
	}
}

// ---------------------------------------------------------------------------
// Element payload consumers (shapes ported from the reference parser the Outlook rule-element layout)
// ---------------------------------------------------------------------------

func consumeSimple(r *reader)    { _ = r.u32() }                         // ext
func consumeExtResU32(r *reader) { _, _, _ = r.u32(), r.u32(), r.u32() } // ext, reserved, value
func consumeExtResString(r *reader) {
	_, _ = r.u32(), r.u32()
	_ = r.stringObject()
}

func consumeSize(r *reader) (minKB, maxKB uint32) {
	_, _ = r.u32(), r.u32() // ext, reserved
	return r.u32(), r.u32()
}

func consumeStringsList(r *reader) []string {
	n := r.u32()
	out := make([]string, 0, n)
	for i := uint32(0); i < n && r.err == nil; i++ {
		_ = r.u32() // per-entry flags
		out = append(out, r.stringObject())
	}
	return out
}

func consumePeopleList(r *reader) []string {
	_, _ = r.u32(), r.u32() // ext, reserved
	n := r.u32()
	out := make([]string, 0, n)
	for i := uint32(0); i < n && r.err == nil; i++ {
		out = append(out, addressFromProps(r.propValArray()))
	}
	_, _ = r.u32(), r.u32() // trailing 1, 0
	return out
}

func consumeMoveToFolder(r *reader) string {
	_, _ = r.u32(), r.u32() // ext, reserved
	r.skipEntryID()         // folder entry id
	r.skipEntryID()         // store entry id
	name := r.stringObject()
	_ = r.u32() // secondary user store
	return name
}

func consumeFormType(r *reader) {
	count := r.u32()
	_ = r.u32() // reserved
	for i := uint32(0); i < count && r.err == nil; i++ {
		_ = r.stringObject()           // display name
		_ = r.asciiString(int(r.u8())) // message class
		// Consume trailing NUL padding up to the next element/separator (which
		// begins with a non-zero byte: 0x8001 -> 0x01, element id -> non-zero).
		for r.err == nil && r.off < len(r.b) && r.b[r.off] == 0 {
			r.off++
		}
	}
}

func consumeWithSelectedProps(r *reader) {
	_, _ = r.u32(), r.u32() // ext, reserved
	_ = r.stringObject()    // forms (semicolon-separated)
	nProps := r.u16()
	for i := uint16(0); i < nProps && r.err == nil; i++ {
		_ = r.stringObject()                // field
		_ = r.u32()                         // tag
		_ = r.u32()                         // string match type
		_ = r.stringObject()                // string value
		_ = r.u32()                         // number match type
		_ = r.u32()                         // number value 1
		_ = r.u32()                         // number value 2
		_ = r.u32()                         // bool match type
		_ = r.u32()                         // unknown1
		_ = r.u32()                         // date match type
		_, _, _ = r.u32(), r.u32(), r.f64() // OleDateTime: status, pad, timestamp
		_ = r.u32()                         // unknown2
	}
	nClasses := r.u32()
	for i := uint32(0); i < nClasses && r.err == nil; i++ {
		_ = r.asciiString(int(r.u8()))
	}
}

func consumeReceivedDateSpan(r *reader) {
	_, _ = r.u32(), r.u32()             // ext, reserved
	_, _, _ = r.u32(), r.u32(), r.f64() // start: status, pad, timestamp
	_, _, _ = r.u32(), r.u32(), r.f64() // end:   status, pad, timestamp
}

func consumeOnThisComputer(r *reader) {
	_, _ = r.u32(), r.u32() // ext, reserved
	r.skip(16)              // uuid
}

func consumeFlag(r *reader) {
	_, _ = r.u32(), r.u32() // ext, reserved
	_ = r.u32()             // days
	_ = r.stringObject()    // action name
	_ = r.u32()             // unknown
}

func consumePerformCustomAction(r *reader) {
	_, _ = r.u32(), r.u32() // ext, reserved
	_ = r.stringObject()    // location
	_ = r.stringObject()    // name
	_ = r.stringObject()    // options
	_ = r.stringObject()    // value
}

func consumeAutomaticReply(r *reader) {
	_, _ = r.u32(), r.u32() // ext, reserved
	r.skip(int(r.u32()))    // message entry id (flat: size + bytes)
	_ = r.stringObject()    // name
}

func consumeRunScript(r *reader) {
	_, _ = r.u32(), r.u32() // ext, reserved
	_ = r.stringObject()    // name
	_ = r.stringObject()    // function name
}

func consumeFlagForFollowUp(r *reader) {
	_, _ = r.u32(), r.u32() // ext, reserved
	_ = r.u32()             // follow-up enum
	_ = r.stringObject()    // action name
}

func consumeApplyRetention(r *reader) {
	_, _ = r.u32(), r.u32() // ext, reserved
	_ = r.u32()             // follow-up enum
	r.skip(16)              // guid
	_ = r.stringObject()    // name
}

// addressFromProps extracts a single address (preferring SMTP) from a recipient
// property-value array, falling back to the display name.
func addressFromProps(props []propVal) string {
	var smtp, email, name string
	for _, p := range props {
		switch p.id {
		case pidSmtpAddress:
			if p.str != "" {
				smtp = p.str
			}
		case pidEmailAddress:
			if p.str != "" {
				email = p.str
			}
		case pidDisplayName:
			if p.str != "" {
				name = p.str
			}
		}
	}
	switch {
	case smtp != "":
		return smtp
	case email != "":
		return email
	default:
		return name
	}
}

// ---------------------------------------------------------------------------
// Write: canonical rules -> .rwz bytes
// ---------------------------------------------------------------------------

// Write encodes canonical rules into an Outlook 2016+ .rwz byte stream. Rules
// whose conditions and actions are all unrepresentable are dropped and counted
// in Report.SkippedRules; partially-representable rules are emitted with the
// unrepresentable parts counted in Report.SkippedElements.
func Write(rules []*semcore.Rule) ([]byte, Report, error) {
	var rep Report
	// Build each rule's elements first so empty rules can be dropped before the
	// header rule count is written.
	type pending struct {
		rule *semcore.Rule
		els  []builtElement
	}
	out := make([]pending, 0, len(rules))
	for _, rule := range rules {
		els, mapped := ruleToElements(rule, &rep)
		if mapped == 0 {
			rep.SkippedRules++
			rep.note("rule %q had no representable conditions or actions", rule.Name)
			continue
		}
		out = append(out, pending{rule: rule, els: els})
	}

	w := &writer{}
	writeHeader(w, len(out))
	for i, p := range out {
		writeRule(w, p.rule, p.els, i == 0)
		if i != len(out)-1 {
			w.u16(0) // inter-rule separator
		}
	}
	writeFooter(w)
	return w.bytesOut(), rep, nil
}

type builtElement struct {
	id      uint32
	payload []byte
}

func payloadOf(fn func(*writer)) []byte {
	w := &writer{}
	fn(w)
	return w.bytesOut()
}

// ruleToElements maps a canonical rule to .rwz elements, returning the elements
// (always led by the 0x190/0x64 markers) and the count of mapped
// conditions+actions (0 means the rule is not worth emitting).
func ruleToElements(rule *semcore.Rule, rep *Report) ([]builtElement, int) {
	els := []builtElement{
		{id: elemApplyRule, payload: payloadOf(func(w *writer) { w.u32(1); w.u32(0); w.u32(applyFlagReceive) })},
		{id: elemUnknown64, payload: payloadOf(func(w *writer) { w.u32(1); w.u32(0); w.u32(1) })},
	}
	mapped := 0

	for _, c := range rule.Conditions {
		if c.MatchType != "" && c.MatchType != semcore.RuleMatchTypeContains &&
			(c.Kind == semcore.RuleConditionKindSubject || c.Kind == semcore.RuleConditionKindBody ||
				c.Kind == semcore.RuleConditionKindFrom || c.Kind == semcore.RuleConditionKindTo) {
			rep.note("condition match type %q downgraded to contains", c.MatchType)
		}
		switch c.Kind {
		case semcore.RuleConditionKindSubject:
			els = append(els, stringsElem(elemSubject, c.Value))
			mapped++
		case semcore.RuleConditionKindBody:
			els = append(els, stringsElem(elemBody, c.Value))
			mapped++
		case semcore.RuleConditionKindFrom:
			els = append(els, builtElement{elemFrom, payloadOf(func(w *writer) { w.peopleList([]string{c.Value}) })})
			mapped++
		case semcore.RuleConditionKindTo:
			els = append(els, builtElement{elemTo, payloadOf(func(w *writer) { w.peopleList([]string{c.Value}) })})
			mapped++
		case semcore.RuleConditionKindSize:
			minKB, maxKB, ok := sizeValueToMinMax(c.Value)
			if !ok {
				rep.SkippedElements++
				rep.note("size condition value %q not representable", c.Value)
				continue
			}
			els = append(els, builtElement{elemSize, payloadOf(func(w *writer) { w.u32(1); w.u32(0); w.u32(minKB); w.u32(maxKB) })})
			mapped++
		default:
			rep.SkippedElements++
			rep.note("condition kind %d not representable in .rwz", c.Kind)
		}
	}

	if !rule.MatchAll && len(rule.Conditions) > 1 {
		rep.note("match-any rule %q exported as match-all (.rwz conditions are AND)", rule.Name)
	}

	for _, a := range rule.Actions {
		switch a.Kind {
		case semcore.RuleActionKindMoveToFolder:
			els = append(els, builtElement{elemMove, payloadOf(func(w *writer) { w.moveToFolder(a.Target) })})
			mapped++
		case semcore.RuleActionKindCopyToFolder:
			els = append(els, builtElement{elemCopy, payloadOf(func(w *writer) { w.moveToFolder(a.Target) })})
			mapped++
		case semcore.RuleActionKindDelete:
			els = append(els, simpleElem(elemDeleteSoft))
			mapped++
		case semcore.RuleActionKindMarkRead:
			els = append(els, simpleElem(elemMarkRead))
			mapped++
		case semcore.RuleActionKindMarkImportant:
			els = append(els, builtElement{elemSetImportance, payloadOf(func(w *writer) { w.u32(1); w.u32(0); w.u32(importanceHigh) })})
			mapped++
		case semcore.RuleActionKindForward:
			els = append(els, builtElement{elemForward, payloadOf(func(w *writer) { w.peopleList([]string{a.ForwardTo}) })})
			mapped++
		case semcore.RuleActionKindForwardAsAttachment:
			els = append(els, builtElement{elemForwardAsAttachment, payloadOf(func(w *writer) { w.peopleList([]string{a.ForwardTo}) })})
			mapped++
		case semcore.RuleActionKindRedirect:
			els = append(els, builtElement{elemRedirect, payloadOf(func(w *writer) { w.peopleList([]string{a.ForwardTo}) })})
			mapped++
		case semcore.RuleActionKindStop:
			els = append(els, simpleElem(elemStop))
			mapped++
		default:
			rep.SkippedElements++
			rep.note("action kind %q not representable in .rwz", a.Kind)
		}
	}
	return els, mapped
}

func stringsElem(id uint32, value string) builtElement {
	return builtElement{id, payloadOf(func(w *writer) {
		w.u32(1) // one word
		w.u32(0) // flags
		w.stringObject(value)
	})}
}

func simpleElem(id uint32) builtElement {
	return builtElement{id, payloadOf(func(w *writer) { w.u32(0) })}
}

func writeHeader(w *writer, nRules int) {
	w.u32(rwSignature2019)
	w.u32(rwFlags2019)
	w.u32(0)
	w.u32(0)
	w.u32(0) // unknown1..3
	w.u32(0)
	w.u32(0)
	w.u32(0)
	w.u32(0) // unknown4..7
	w.u32(1) // unknown8
	w.u32(0) // unknown9
	w.u16(uint16(nRules))
	w.u16(0) // extra
}

func writeRule(w *writer, rule *semcore.Rule, els []builtElement, first bool) {
	w.u16(0x0001) // signature
	w.stringObject(rule.Name)
	if rule.Enabled {
		w.u32(1)
	} else {
		w.u32(0)
	}
	for i := 0; i < 4; i++ {
		w.u32(0) // unknown[0..3]
	}

	body := &writer{}
	body.u16(uint16(len(els)))
	if first {
		body.u16(0xffff)
		body.u16(0)
		body.u16(uint16(len(ruleClassName)))
		body.ascii(ruleClassName)
	} else {
		body.u16(0x8001)
	}
	for j, el := range els {
		body.u32(el.id)
		body.raw(el.payload)
		if j != len(els)-1 {
			body.u16(0x8001)
		}
	}

	w.u32(uint32(body.len())) // dataSize = byte count of everything that follows
	w.raw(body.bytesOut())
}

func writeFooter(w *writer) {
	w.u32(0) // template directory length
	w.u32(0) // OleDateTime status (Valid)
	w.f64(0) // OleDateTime timestamp
	w.u32(0) // trailing unknown
}

// ---------------------------------------------------------------------------
// Size value mapping
// ---------------------------------------------------------------------------

// sizeValueToMinMax converts a canonical size value ("1000K" = over 1000 KB,
// "-5M" = under 5 MB) into the .rwz min/max KB pair.
func sizeValueToMinMax(v string) (minKB, maxKB uint32, ok bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, 0, false
	}
	under := false
	if strings.HasPrefix(v, "-") {
		under = true
		v = v[1:]
	}
	kb, ok := parseSizeKB(v)
	if !ok {
		return 0, 0, false
	}
	if under {
		return 0, kb, true
	}
	return kb, 0, true
}

// parseSizeKB parses a number with an optional K/M/G suffix into kilobytes.
func parseSizeKB(v string) (uint32, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, false
	}
	mult := 1 // bare number is already KB
	last := unicode.ToUpper(rune(v[len(v)-1]))
	switch last {
	case 'K':
		v, mult = v[:len(v)-1], 1
	case 'M':
		v, mult = v[:len(v)-1], 1024
	case 'G':
		v, mult = v[:len(v)-1], 1024*1024
	}
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || n < 0 {
		return 0, false
	}
	return uint32(n * mult), true
}

// minMaxToSizeValue is the inverse of sizeValueToMinMax. When both bounds are
// set only the lower bound is kept (canonical holds a single threshold).
func minMaxToSizeValue(minKB, maxKB uint32) string {
	switch {
	case minKB > 0:
		return fmt.Sprintf("%dK", minKB)
	case maxKB > 0:
		return fmt.Sprintf("-%dK", maxKB)
	default:
		return ""
	}
}
