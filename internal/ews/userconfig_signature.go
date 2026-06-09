package ews

import (
	"encoding/xml"
	"strings"
)

// OWA.UserOptions is the well-known mailbox-options UserConfiguration through
// which Outlook/OWA roam the user's signature (Exchange surfaces it as
// Get-MailboxMessageConfiguration's SignatureHtml / AutoAddSignature). Bridging
// its signature entry to the canonical webmail signature store
// (db.GetSignature/PutSignature, the same value /api/v1/signature serves) keeps
// ONE signature across webmail and every EWS/Outlook client, rather than two
// disconnected copies. The remaining OWA option entries are passed through
// verbatim so Outlook keeps its other roamed settings.
const (
	owaUserOptionsName = "OWA.UserOptions"
	owaSignatureKey    = "signaturehtml"
	owaAutoAddKey      = "autoaddsignature"
)

// owaDictTypedValue is one typed value (key or value) inside an EWS Dictionary
// entry. Namespaceless tags match the t:-prefixed elements by local name.
type owaDictTypedValue struct {
	Type  string `xml:"Type"`
	Value string `xml:"Value"`
}

type owaDictEntry struct {
	Key   owaDictTypedValue `xml:"DictionaryKey"`
	Value owaDictTypedValue `xml:"DictionaryValue"`
}

type owaDict struct {
	Entries []owaDictEntry `xml:"DictionaryEntry"`
}

// parseOWADictionary unmarshals the stored Dictionary inner XML into its entries.
func parseOWADictionary(inner string) []owaDictEntry {
	if strings.TrimSpace(inner) == "" {
		return nil
	}
	var d owaDict
	if err := xml.Unmarshal([]byte("<Dictionary>"+inner+"</Dictionary>"), &d); err != nil {
		return nil
	}
	return d.Entries
}

// owaSignatureFromDict returns the signaturehtml value from a parsed OWA options
// dictionary, if present.
func owaSignatureFromDict(inner string) (sig string, found bool) {
	for _, e := range parseOWADictionary(inner) {
		if strings.EqualFold(e.Key.Value, owaSignatureKey) {
			return e.Value.Value, true
		}
	}
	return "", false
}

// emitOWADictionaryWithSignature renders the OWA options dictionary inner XML,
// overriding signaturehtml/autoaddsignature with the canonical signature and
// passing every other entry through verbatim. Empty signature → auto-add false.
func emitOWADictionaryWithSignature(entries []owaDictEntry, sig string) string {
	autoAdd := "false"
	if sig != "" {
		autoAdd = "true"
	}
	var b strings.Builder
	wroteSig, wroteAuto := false, false
	entry := func(keyType, key, valType, val string) {
		b.WriteString(`<t:DictionaryEntry><t:DictionaryKey><t:Type>` + keyType + `</t:Type><t:Value>` + xmlEscape(key) + `</t:Value></t:DictionaryKey>`)
		b.WriteString(`<t:DictionaryValue><t:Type>` + valType + `</t:Type><t:Value>` + xmlEscape(val) + `</t:Value></t:DictionaryValue></t:DictionaryEntry>`)
	}
	for _, e := range entries {
		switch strings.ToLower(e.Key.Value) {
		case owaSignatureKey:
			entry("String", owaSignatureKey, "String", sig)
			wroteSig = true
		case owaAutoAddKey:
			entry("String", owaAutoAddKey, "Boolean", autoAdd)
			wroteAuto = true
		default:
			entry(e.Key.Type, e.Key.Value, e.Value.Type, e.Value.Value)
		}
	}
	if !wroteSig {
		entry("String", owaSignatureKey, "String", sig)
	}
	if !wroteAuto {
		entry("String", owaAutoAddKey, "Boolean", autoAdd)
	}
	return b.String()
}
