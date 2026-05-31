// Package mailthread holds the canonical RFC 5322 conversation-rooting logic
// shared by the storage thread index (JMAP threadId / webmail threads) and the
// semcore identity store (EWS ConversationId). Both layers derive a thread from
// the same root Message-ID; keeping that derivation in one place ensures they
// can never drift apart and group the same conversation differently.
//
// This package has no dependencies on storage or semcore (it is a leaf), so both
// can import it without an import cycle.
package mailthread

import "strings"

// StripBrackets removes a single surrounding pair of angle brackets (and any
// surrounding whitespace) from a Message-ID. A value that is not wrapped in a
// matching <...> pair is returned trimmed but otherwise unchanged, so a stray
// single bracket never produces a different root than the header parser would.
func StripBrackets(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 && s[0] == '<' && s[len(s)-1] == '>' {
		return s[1 : len(s)-1]
	}
	return s
}

// Root returns the conversation-root Message-ID for a message using the
// canonical RFC 2822 precedence: the most recent References entry, then
// In-Reply-To, then the message's own Message-ID. Brackets are stripped from
// the chosen value.
//
// isRoot is true when the message roots on its own Message-ID (it has no
// parent in References/In-Reply-To). root is "" only when none of the three
// carries a usable Message-ID, in which case the caller supplies its own
// fallback (a random id for semcore, a subject hash for storage).
func Root(ownMessageID, inReplyTo string, references []string) (root string, isRoot bool) {
	if n := len(references); n > 0 {
		if id := StripBrackets(references[n-1]); id != "" {
			return id, false
		}
	}
	if id := StripBrackets(inReplyTo); id != "" {
		return id, false
	}
	if id := StripBrackets(ownMessageID); id != "" {
		return id, true
	}
	return "", true
}
