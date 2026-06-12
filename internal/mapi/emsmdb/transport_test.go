package emsmdb

import (
	"bytes"
	"testing"

	"github.com/umailserver/umailserver/internal/mapi/wire"
)

// encodeConnectRequest builds a Connect request body the way the client would,
// so the test can exercise DecodeConnectRequest against real wire bytes.
func encodeConnectRequest(r ConnectRequest) []byte {
	p := wire.NewPush(0)
	p.Str(r.UserDN)
	p.Uint32(r.Flags)
	p.Uint32(r.DefaultCodePage)
	p.Uint32(r.LcidSort)
	p.Uint32(r.LcidString)
	writeCounted(p, r.AuxIn)
	return p.Bytes()
}

// decodeConnectResponse parses a Connect response body (inverse of Encode).
func decodeConnectResponse(b []byte) ConnectResponse {
	p := wire.NewPull(b, wire.FlagUTF16)
	var r ConnectResponse
	r.StatusCode = p.Uint32()
	r.ErrorCode = p.Uint32()
	r.PollsMax = p.Uint32()
	r.RetryCount = p.Uint32()
	r.RetryDelay = p.Uint32()
	r.DnPrefix = p.Str()
	r.DisplayName = p.WStr()
	r.AuxOut = readCounted(p)
	return r
}

// TestConnectRequestRoundTrip checks the Connect request body parses back to the
// same fields, including the auxiliary buffer.
func TestConnectRequestRoundTrip(t *testing.T) {
	in := ConnectRequest{
		UserDN:          "/o=uMailServer/ou=Exchange Administrative Group (FYDIBOHF23SPDLT)/cn=Recipients/cn=qa.bob",
		Flags:           0,
		DefaultCodePage: 1252,
		LcidSort:        0x0409,
		LcidString:      0x0409,
		AuxIn:           []byte{0x01, 0x02, 0x03},
	}
	got, err := DecodeConnectRequest(encodeConnectRequest(in))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.UserDN != in.UserDN || got.DefaultCodePage != in.DefaultCodePage ||
		got.LcidSort != in.LcidSort || got.LcidString != in.LcidString ||
		!bytes.Equal(got.AuxIn, in.AuxIn) {
		t.Errorf("connect req round-trip = %+v, want %+v", got, in)
	}
}

// TestConnectResponseRoundTrip checks the Connect response body, including the
// UTF-16 DisplayName.
func TestConnectResponseRoundTrip(t *testing.T) {
	in := ConnectResponse{
		PollsMax:    60000,
		RetryCount:  6,
		RetryDelay:  10000,
		DnPrefix:    "/o=uMailServer/ou=Exchange Administrative Group (FYDIBOHF23SPDLT)",
		DisplayName: "QA Böb",
		AuxOut:      nil,
	}
	got := decodeConnectResponse(in.Encode())
	if got.StatusCode != 0 || got.PollsMax != in.PollsMax || got.RetryCount != in.RetryCount ||
		got.DnPrefix != in.DnPrefix || got.DisplayName != in.DisplayName {
		t.Errorf("connect rsp round-trip = %+v, want %+v", got, in)
	}
}

// TestExecuteRequestRoundTrip checks the Execute request body carries the ROP
// buffer and the output cap.
func TestExecuteRequestRoundTrip(t *testing.T) {
	rop := []byte{0x02, 0x00, 0xDE, 0xAD}
	p := wire.NewPush(0)
	p.Uint32(0)                   // flags
	writeCounted(p, rop)          // cb_in + ROP buffer
	p.Uint32(0x40000)             // cb_out
	writeCounted(p, []byte{0xAA}) // aux

	got, err := DecodeExecuteRequest(p.Bytes())
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !bytes.Equal(got.RopBuffer, rop) || got.MaxRopOut != 0x40000 || len(got.AuxIn) != 1 {
		t.Errorf("execute req = %+v, want rop=% x cap=0x40000", got, rop)
	}
}

// TestExecuteResponseEncode pins the Execute response field order and confirms
// the ROP buffer is length-prefixed.
func TestExecuteResponseEncode(t *testing.T) {
	rop := []byte{0x01, 0x02, 0x03}
	body := ExecuteResponse{RopBuffer: rop}.Encode()

	p := wire.NewPull(body, wire.FlagUTF16)
	status, errCode, flags := p.Uint32(), p.Uint32(), p.Uint32()
	if status != 0 || errCode != 0 || flags != 0 {
		t.Fatalf("execute rsp header = %d/%d/%d, want three zero uint32s", status, errCode, flags)
	}
	if got := readCounted(p); !bytes.Equal(got, rop) {
		t.Errorf("rop buffer = % x, want % x", got, rop)
	}
	if readCounted(p) != nil {
		t.Error("expected empty aux buffer")
	}
	if p.Err() != nil || p.Remaining() != 0 {
		t.Errorf("trailing bytes or error: %v, remaining=%d", p.Err(), p.Remaining())
	}
}

// TestDisconnectResponseEncode confirms the trailing zero aux-size.
func TestDisconnectResponseEncode(t *testing.T) {
	body := DisconnectResponse{}.Encode()
	if len(body) != 12 {
		t.Fatalf("disconnect rsp = %d bytes, want 12 (status,error,auxsize)", len(body))
	}
}
