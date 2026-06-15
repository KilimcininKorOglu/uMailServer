package rfr

import (
	"encoding/binary"
	"strings"
	"testing"

	"github.com/umailserver/umailserver/internal/mapi/ndr"
)

// fixedFQDN serves a constant FQDN so a test reads back exactly what it set.
func fixedFQDN(s string) func() string { return func() string { return s } }

// respReader decodes an RFR response body with raw little-endian reads, kept
// deliberately independent of internal/mapi/ndr so a marshaling regression in
// the encoder under test cannot be masked by a matching bug in the decoder.
type respReader struct {
	b   []byte
	off int
	t   *testing.T
}

func (r *respReader) u32() uint32 {
	r.t.Helper()
	if r.off+4 > len(r.b) {
		r.t.Fatalf("response truncated reading uint32 at offset %d (len %d)", r.off, len(r.b))
	}
	v := binary.LittleEndian.Uint32(r.b[r.off:])
	r.off += 4
	return v
}

// str reads a conformant-varying string (max_count, offset, actual_count, then
// the characters) and returns it with the trailing NUL trimmed.
func (r *respReader) str() string {
	r.t.Helper()
	maxCount := r.u32()
	if off := r.u32(); off != 0 {
		r.t.Fatalf("conformant string offset = %d, want 0", off)
	}
	actual := r.u32()
	if actual != maxCount {
		r.t.Fatalf("conformant string actual_count = %d, max_count = %d; want equal", actual, maxCount)
	}
	if r.off+int(actual) > len(r.b) {
		r.t.Fatalf("response truncated reading %d string bytes at offset %d", actual, r.off)
	}
	s := string(r.b[r.off : r.off+int(actual)])
	r.off += int(actual)
	return strings.TrimRight(s, "\x00")
}

// align4 advances past the padding NDR inserts before the 4-aligned result code.
func (r *respReader) align4() {
	if rem := r.off % 4; rem != 0 {
		r.off += 4 - rem
	}
}

// newDSARequest builds a minimal well-formed RfrGetNewDSA request: ulFlags, a
// one-byte pUserDN, and two NULL [in, out, unique] hint pointers.
func newDSARequest() []byte {
	p := ndr.NewPush()
	p.Uint32(0) // ulFlags
	p.ULong(1)  // pUserDN max_count
	p.ULong(0)  // offset
	p.ULong(1)  // actual_count
	p.Uint8(0)  // "\x00"
	p.ULong(0)  // ppszUnused: NULL outer pointer
	p.ULong(0)  // ppszServer: NULL outer pointer
	return p.Bytes()
}

// fqdnRequest builds a well-formed RfrGetFQDNFromLegacyDN request carrying dn.
func fqdnRequest(dn string) []byte {
	p := ndr.NewPush()
	n := uint32(len(dn) + 1)
	p.Uint32(0) // ulFlags
	p.Uint32(n) // cbMailboxServerDN (range 10..1024)
	p.ULong(n)  // szMailboxServerDN max_count
	p.ULong(0)  // offset
	p.ULong(n)  // actual_count
	p.Raw([]byte(dn))
	p.Uint8(0)
	return p.Bytes()
}

func TestHandleRPCUnknownOpnumFaults(t *testing.T) {
	s := NewServer(fixedFQDN("mail.test.local"))
	if _, ok := s.HandleRPC(2, "", nil); ok {
		t.Fatal("opnum 2 reported ok; the tunnel must fault an unimplemented opnum")
	}
}

func TestGetNewDSAReturnsFQDN(t *testing.T) {
	const fqdn = "mail.test.local"
	s := NewServer(fixedFQDN(fqdn))
	resp, ok := s.HandleRPC(opRfrGetNewDSA, "user@test.local", newDSARequest())
	if !ok {
		t.Fatal("RfrGetNewDSA reported not-ok")
	}
	r := &respReader{b: resp, t: t}
	if p := r.u32(); p != 0 {
		t.Fatalf("ppszUnused referent = %#x, want 0 (NULL)", p)
	}
	// ppszServer is [in, out, unique, string] **: an outer and an inner referent
	// both precede the string.
	if p := r.u32(); p == 0 {
		t.Fatal("ppszServer outer referent is NULL; want a non-NULL pointer")
	}
	if p := r.u32(); p == 0 {
		t.Fatal("ppszServer inner referent is NULL; want a non-NULL pointer")
	}
	if got := r.str(); got != fqdn {
		t.Fatalf("ppszServer = %q, want %q", got, fqdn)
	}
	r.align4()
	if res := r.u32(); res != rfrSuccess {
		t.Fatalf("result = %#x, want rfrSuccess (%#x)", res, rfrSuccess)
	}
	if r.off != len(resp) {
		t.Fatalf("decoded %d bytes, response is %d; trailing bytes mean a layout mismatch", r.off, len(resp))
	}
}

func TestGetFQDNFromLegacyDNReturnsFQDN(t *testing.T) {
	const fqdn = "mail.test.local"
	s := NewServer(fixedFQDN(fqdn))
	resp, ok := s.HandleRPC(opRfrGetFQDNFromLegacyDN, "", fqdnRequest("/o=org/ou=g/cn=Servers/cn=s"))
	if !ok {
		t.Fatal("RfrGetFQDNFromLegacyDN reported not-ok")
	}
	r := &respReader{b: resp, t: t}
	// ppszServerFQDN is [out, ref, string] **: only the inner referent is on the
	// wire, so a single non-NULL pointer precedes the string.
	if p := r.u32(); p == 0 {
		t.Fatal("ppszServerFQDN referent is NULL; want a non-NULL pointer")
	}
	if got := r.str(); got != fqdn {
		t.Fatalf("ppszServerFQDN = %q, want %q", got, fqdn)
	}
	r.align4()
	if res := r.u32(); res != rfrSuccess {
		t.Fatalf("result = %#x, want rfrSuccess (%#x)", res, rfrSuccess)
	}
	if r.off != len(resp) {
		t.Fatalf("decoded %d bytes, response is %d; trailing bytes mean a layout mismatch", r.off, len(resp))
	}
}

// TestGetNewDSAUnconfiguredHostnameFaults asserts that with no FQDN configured
// the server returns a NULL server pointer and an error status rather than
// advertising an empty NSPI host.
func TestGetNewDSAUnconfiguredHostnameFaults(t *testing.T) {
	s := NewServer(fixedFQDN(""))
	resp, ok := s.HandleRPC(opRfrGetNewDSA, "", newDSARequest())
	if !ok {
		t.Fatal("RfrGetNewDSA reported not-ok")
	}
	r := &respReader{b: resp, t: t}
	if p := r.u32(); p != 0 {
		t.Fatalf("ppszUnused referent = %#x, want 0", p)
	}
	if p := r.u32(); p != 0 {
		t.Fatalf("ppszServer referent = %#x, want 0 (NULL) when no FQDN is configured", p)
	}
	if res := r.u32(); res != rfrError {
		t.Fatalf("result = %#x, want rfrError (%#x)", res, rfrError)
	}
}
