// Package ntlmssp implements the server half of NTLM (NTLMSSP) authentication
// as specified in MS-NLMP: parsing the client NEGOTIATE (type 1) and
// AUTHENTICATE (type 3) messages, producing the server CHALLENGE (type 2)
// message, and verifying an NTLMv2 response against a stored NT hash.
//
// It is transport-agnostic. For Outlook Anywhere (RPC-over-HTTP) the three
// messages ride in the HTTP WWW-Authenticate and Authorization headers of the
// RPC-proxy requests (the "NTLM" scheme, RFC 4559).
//
// NTLMv2 is keyed solely on the NT hash (MD4 of the UTF-16LE password, MS-NLMP
// 3.3.2), so that one secret is all the server must hold to verify a
// challenge-response. It cannot be derived from a bcrypt/argon2 hash, which is
// why NTLM requires the NT hash to be captured and stored at password-set time.
package ntlmssp

import (
	"crypto/hmac"
	"crypto/md5" // #nosec G501 -- HMAC-MD5/MD4 are mandated by NTLMv2 (MS-NLMP)
	"encoding/binary"
	"encoding/hex"
	"errors"
	"strings"
	"unicode/utf16"
)

// ErrFormat is returned when a message is too short or not a well-formed
// NTLMSSP message of the expected type.
var ErrFormat = errors.New("ntlmssp: malformed message")

// signature is the 8-byte NTLMSSP message prefix ("NTLMSSP\0").
var signature = []byte{'N', 'T', 'L', 'M', 'S', 'S', 'P', 0}

// NEGOTIATE flag bits (MS-NLMP 2.2.2.5) used when building the CHALLENGE.
const (
	negotiateUnicode            uint32 = 0x00000001
	requestTarget               uint32 = 0x00000004
	negotiateNTLM               uint32 = 0x00000200
	negotiateExtSessionSecurity uint32 = 0x00080000
	targetTypeServer            uint32 = 0x00020000
	negotiateTargetInfo         uint32 = 0x00800000
)

// AV_PAIR identifiers (MS-NLMP 2.2.2.1) emitted in the CHALLENGE target info.
const (
	avEOL        uint16 = 0x0000
	avHostname   uint16 = 0x0001
	avDomainName uint16 = 0x0002
	avTimestamp  uint16 = 0x0007
)

// challengeFixedLen is the size of the fixed CHALLENGE header preceding the
// payload (no VERSION field, since NTLMSSP_NEGOTIATE_VERSION is not set).
const challengeFixedLen = 48

// NTHash returns the NT hash of a password: MD4 of its UTF-16LE encoding
// (MS-NLMP 3.3.2). This is the value stored to enable NTLM.
func NTHash(password string) [16]byte {
	return md4Sum(utf16LE(password))
}

// NTHashForStorage returns the hex-encoded NT hash to persist on an account, or
// the empty string when NTLM is disabled. Callers store the result unconditionally
// so that disabling NTLM clears any previously captured hash on the next
// password set, never leaving a stale credential behind.
func NTHashForStorage(enabled bool, password string) string {
	if !enabled {
		return ""
	}
	h := NTHash(password)
	return hex.EncodeToString(h[:])
}

// MessageType returns the NTLMSSP message type of b (1=NEGOTIATE, 2=CHALLENGE,
// 3=AUTHENTICATE) after validating the "NTLMSSP\0" signature. ok is false when b
// is too short or lacks the signature. It lets a caller dispatch on the message
// type before choosing the matching parser.
func MessageType(b []byte) (uint32, bool) {
	if len(b) < 12 || !hasSignature(b) {
		return 0, false
	}
	return binary.LittleEndian.Uint32(b[8:12]), true
}

// Negotiate is the decoded client NEGOTIATE (type 1) message.
type Negotiate struct {
	Flags uint32
}

// ParseNegotiate decodes a NEGOTIATE message, validating the signature and
// message type.
func ParseNegotiate(b []byte) (*Negotiate, error) {
	if len(b) < 12 || !hasSignature(b) || binary.LittleEndian.Uint32(b[8:12]) != 1 {
		return nil, ErrFormat
	}
	n := &Negotiate{}
	if len(b) >= 16 {
		n.Flags = binary.LittleEndian.Uint32(b[12:16])
	}
	return n, nil
}

// BuildChallenge builds a CHALLENGE (type 2) message carrying the 8-byte server
// challenge and a target-info block naming targetName (MS-NLMP 2.2.1.2). The
// caller must retain serverChallenge to later verify the AUTHENTICATE response.
// timestamp is a Windows FILETIME placed in the target info.
func BuildChallenge(serverChallenge [8]byte, targetName string, timestamp uint64) []byte {
	target := utf16LE(targetName)
	av := buildAVPairs(target, timestamp)
	flags := negotiateUnicode | requestTarget | negotiateNTLM |
		negotiateExtSessionSecurity | targetTypeServer | negotiateTargetInfo

	msg := make([]byte, challengeFixedLen+len(target)+len(av))
	copy(msg[0:8], signature)
	binary.LittleEndian.PutUint32(msg[8:12], 2) // MessageType = CHALLENGE
	binary.LittleEndian.PutUint16(msg[12:14], uint16(len(target)))
	binary.LittleEndian.PutUint16(msg[14:16], uint16(len(target)))
	binary.LittleEndian.PutUint32(msg[16:20], challengeFixedLen)
	binary.LittleEndian.PutUint32(msg[20:24], flags)
	copy(msg[24:32], serverChallenge[:])
	// msg[32:40] reserved (zero)
	binary.LittleEndian.PutUint16(msg[40:42], uint16(len(av)))
	binary.LittleEndian.PutUint16(msg[42:44], uint16(len(av)))
	binary.LittleEndian.PutUint32(msg[44:48], uint32(challengeFixedLen+len(target)))
	copy(msg[challengeFixedLen:], target)
	copy(msg[challengeFixedLen+len(target):], av)
	return msg
}

// buildAVPairs assembles the CHALLENGE target info: domain, hostname, timestamp
// and a terminating EOL (MS-NLMP 2.2.2.1). A hostname pair is always present
// because NTLMv2 clients fold it into the NtChallengeResponse.
func buildAVPairs(name []byte, timestamp uint64) []byte {
	var ts [8]byte
	binary.LittleEndian.PutUint64(ts[:], timestamp)

	out := make([]byte, 0, 4+len(name)+4+len(name)+4+8+4)
	appendAV := func(id uint16, val []byte) {
		var h [4]byte
		binary.LittleEndian.PutUint16(h[0:2], id)
		binary.LittleEndian.PutUint16(h[2:4], uint16(len(val)))
		out = append(out, h[:]...)
		out = append(out, val...)
	}
	appendAV(avDomainName, name)
	appendAV(avHostname, name)
	appendAV(avTimestamp, ts[:])
	appendAV(avEOL, nil) // terminator: type avEOL, length 0
	return out
}

// Authenticate is the decoded client AUTHENTICATE (type 3) message, holding the
// fields needed to verify an NTLMv2 response.
type Authenticate struct {
	Flags      uint32
	User       string // decoded from UTF-16LE
	Domain     []byte // raw UTF-16LE bytes, used verbatim in NTOWFv2
	NTResponse []byte // NtChallengeResponse: NTProofStr(16) + temp
}

// DomainName returns the AUTHENTICATE domain decoded from UTF-16LE, for account
// lookup. The raw Domain bytes are kept verbatim for the NTOWFv2 computation.
func (a *Authenticate) DomainName() string {
	return decodeUTF16LE(a.Domain)
}

// ParseAuthenticate decodes an AUTHENTICATE message, following the message's own
// field offsets to extract the NtChallengeResponse, domain and user (MS-NLMP
// 2.2.1.3).
func ParseAuthenticate(b []byte) (*Authenticate, error) {
	if len(b) < 64 || !hasSignature(b) || binary.LittleEndian.Uint32(b[8:12]) != 3 {
		return nil, ErrFormat
	}
	field := func(off int) ([]byte, bool) {
		l := int(binary.LittleEndian.Uint16(b[off : off+2]))
		o := int(binary.LittleEndian.Uint32(b[off+4 : off+8]))
		if l == 0 {
			return nil, true
		}
		if o < 0 || o > len(b) || o+l > len(b) {
			return nil, false
		}
		return b[o : o+l], true
	}
	nt, okNT := field(20)  // NtChallengeResponseFields
	dom, okDom := field(28) // DomainNameFields
	usr, okUsr := field(36) // UserNameFields
	if !okNT || !okDom || !okUsr {
		return nil, ErrFormat
	}
	return &Authenticate{
		Flags:      binary.LittleEndian.Uint32(b[60:64]),
		User:       decodeUTF16LE(usr),
		Domain:     dom,
		NTResponse: nt,
	}, nil
}

// VerifyNTLMv2 reports whether the AUTHENTICATE response proves possession of
// the password whose NT hash is ntHash, given the serverChallenge that was
// issued in the CHALLENGE. It recomputes the NTProofStr from the client-supplied
// temp (MS-NLMP 3.3.2) and compares it in constant time, so the server never has
// to reconstruct the target info, timestamp or client challenge the client used.
func VerifyNTLMv2(a *Authenticate, serverChallenge [8]byte, ntHash [16]byte) bool {
	if a == nil || len(a.NTResponse) < 16 {
		return false
	}
	ntProofStr := a.NTResponse[:16]
	temp := a.NTResponse[16:]

	responseKeyNT := ntowfv2(ntHash, a.User, a.Domain)
	expected := hmacMD5(responseKeyNT, concat(serverChallenge[:], temp))
	return hmac.Equal(expected, ntProofStr)
}

// ntowfv2 computes the NTLMv2 response key: HMAC-MD5 over the NT hash of the
// uppercased UTF-16LE user name concatenated with the (raw UTF-16LE) domain
// (MS-NLMP 3.3.2 NTOWFv2). domain is the verbatim bytes from the AUTHENTICATE.
func ntowfv2(ntHash [16]byte, user string, domain []byte) []byte {
	return hmacMD5(ntHash[:], concat(utf16LE(strings.ToUpper(user)), domain))
}

func hmacMD5(key, data []byte) []byte {
	m := hmac.New(md5.New, key)
	m.Write(data)
	return m.Sum(nil)
}

func hasSignature(b []byte) bool {
	return len(b) >= 8 &&
		b[0] == 'N' && b[1] == 'T' && b[2] == 'L' && b[3] == 'M' &&
		b[4] == 'S' && b[5] == 'S' && b[6] == 'P' && b[7] == 0
}

func concat(a, b []byte) []byte {
	out := make([]byte, 0, len(a)+len(b))
	out = append(out, a...)
	out = append(out, b...)
	return out
}

func utf16LE(s string) []byte {
	u := utf16.Encode([]rune(s))
	b := make([]byte, len(u)*2)
	for i, v := range u {
		binary.LittleEndian.PutUint16(b[i*2:], v)
	}
	return b
}

func decodeUTF16LE(b []byte) string {
	if len(b)%2 != 0 {
		b = b[:len(b)-1]
	}
	u := make([]uint16, len(b)/2)
	for i := range u {
		u[i] = binary.LittleEndian.Uint16(b[i*2:])
	}
	return string(utf16.Decode(u))
}
