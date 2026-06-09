package ews

import (
	"strings"
	"testing"
)

// TestOWASignatureBridge verifies the OWA.UserOptions signature dictionary
// round-trips the canonical signature, drives auto-add from emptiness, and
// preserves unrelated OWA option entries while overriding the signature.
func TestOWASignatureBridge(t *testing.T) {
	// Emit then parse must round-trip the (HTML, escape-sensitive) signature.
	dict := emitOWADictionaryWithSignature(nil, "<b>Jane &amp; Co</b>")
	sig, ok := owaSignatureFromDict(dict)
	if !ok || sig != "<b>Jane &amp; Co</b>" {
		t.Errorf("round-trip signature = %q ok=%v, want %q true", sig, ok, "<b>Jane &amp; Co</b>")
	}
	// A non-empty signature turns auto-add on.
	if !strings.Contains(dict, owaAutoAddKey) || !strings.Contains(dict, "<t:Value>true</t:Value>") {
		t.Errorf("auto-add should be true for a non-empty signature: %s", dict)
	}
	// An empty signature turns auto-add off.
	if empty := emitOWADictionaryWithSignature(nil, ""); !strings.Contains(empty, "<t:Value>false</t:Value>") {
		t.Errorf("auto-add should be false for an empty signature: %s", empty)
	}

	// Other OWA option entries are preserved while signaturehtml is overridden.
	in := `<t:DictionaryEntry><t:DictionaryKey><t:Type>String</t:Type><t:Value>timezone</t:Value></t:DictionaryKey>` +
		`<t:DictionaryValue><t:Type>String</t:Type><t:Value>Europe/Istanbul</t:Value></t:DictionaryValue></t:DictionaryEntry>` +
		`<t:DictionaryEntry><t:DictionaryKey><t:Type>String</t:Type><t:Value>signaturehtml</t:Value></t:DictionaryKey>` +
		`<t:DictionaryValue><t:Type>String</t:Type><t:Value>old sig</t:Value></t:DictionaryValue></t:DictionaryEntry>`
	out := emitOWADictionaryWithSignature(parseOWADictionary(in), "new sig")
	if !strings.Contains(out, "Europe/Istanbul") {
		t.Errorf("unrelated timezone entry should be preserved: %s", out)
	}
	if got, _ := owaSignatureFromDict(out); got != "new sig" {
		t.Errorf("signature should be overridden to %q, got %q", "new sig", got)
	}
}
