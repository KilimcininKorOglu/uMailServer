package rpch

import (
	"bytes"
	"encoding/hex"
	"testing"

	"github.com/umailserver/umailserver/internal/mapi/dcerpc"
)

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("bad hex vector: %v", err)
	}
	return b
}

// seqCookie builds a 16-byte cookie of consecutive bytes starting at start, so
// the impacket vectors below (cookies of 0x00.., 0x10.., 0x20..) are checkable
// by eye.
func seqCookie(start byte) cookie {
	var c cookie
	for i := range c {
		c[i] = start + byte(i)
	}
	return c
}

// impacket-generated CONN/A1 and CONN/B1 PDUs with virtual-connection cookie
// 00..0f, out-channel cookie 10..1f, in-channel cookie 20..2f. impacket is an
// independent MS-RPCH implementation, so matching its bytes is interop
// evidence, not a self-consistency check.
const (
	connA1Hex = "05001403100000004c0000000000000000000400060000000100000003000000000102030405060708090a0b0c0d0e0f03000000101112131415161718191a1b1c1d1e1f0000000000000400"
	connB1Hex = "0500140310000000680000000000000000000600060000000100000003000000000102030405060708090a0b0c0d0e0f03000000202122232425262728292a2b2c2d2e2f040000000000004005000000e09304000c000000303132333435363738393a3b3c3d3e3f"
)

func TestParseConnA1(t *testing.T) {
	got, err := parseConnSetup(mustHex(t, connA1Hex))
	if err != nil {
		t.Fatalf("parseConnSetup: %v", err)
	}
	if got.virtualConnection != seqCookie(0x00) {
		t.Fatalf("virtual connection cookie = % x", got.virtualConnection)
	}
	if got.channel != seqCookie(0x10) {
		t.Fatalf("out channel cookie = % x", got.channel)
	}
}

func TestParseConnB1(t *testing.T) {
	got, err := parseConnSetup(mustHex(t, connB1Hex))
	if err != nil {
		t.Fatalf("parseConnSetup: %v", err)
	}
	if got.virtualConnection != seqCookie(0x00) {
		t.Fatalf("virtual connection cookie = % x", got.virtualConnection)
	}
	if got.channel != seqCookie(0x20) {
		t.Fatalf("in channel cookie = % x", got.channel)
	}
}

// TestParseConnPairsChannels confirms both setup PDUs report the same virtual
// connection cookie — the value the tunnel uses to pair the IN and OUT channels.
func TestParseConnPairsChannels(t *testing.T) {
	a1, err := parseConnSetup(mustHex(t, connA1Hex))
	if err != nil {
		t.Fatalf("CONN/A1: %v", err)
	}
	b1, err := parseConnSetup(mustHex(t, connB1Hex))
	if err != nil {
		t.Fatalf("CONN/B1: %v", err)
	}
	if a1.virtualConnection != b1.virtualConnection {
		t.Fatalf("CONN/A1 and CONN/B1 must share the virtual connection cookie: %x vs %x", a1.virtualConnection, b1.virtualConnection)
	}
	if a1.channel == b1.channel {
		t.Fatal("the OUT and IN channel cookies must differ")
	}
}

func TestRTSFlags(t *testing.T) {
	if f, err := rtsFlags(mustHex(t, connA1Hex)); err != nil || f != rtsFlagNone {
		t.Fatalf("setup PDU flags = %#x, err %v; want 0", f, err)
	}
	ping := dcerpc.EncodeRTS(rtsFlagPing, 0, nil)
	if f, err := rtsFlags(ping); err != nil || f != rtsFlagPing {
		t.Fatalf("ping PDU flags = %#x, err %v; want %#x", f, err, rtsFlagPing)
	}
}

// TestBuildConnA3 pins the server's CONN/A3 reply against bytes verified to
// parse in the independent client implementation.
func TestBuildConnA3(t *testing.T) {
	want := mustHex(t, "0500140310000000"+"1c000000"+"00000000"+"0000"+"0100"+"02000000"+"c0d40100")
	if got := buildConnA3(); !bytes.Equal(got, want) {
		t.Fatalf("got  % x\nwant % x", got, want)
	}
}

func TestBuildConnC2(t *testing.T) {
	want := mustHex(t, "0500140310000000"+"2c000000"+"00000000"+"0000"+"0300"+"06000000"+"01000000"+"00000000"+"00000400"+"02000000"+"c0d40100")
	if got := buildConnC2(); !bytes.Equal(got, want) {
		t.Fatalf("got  % x\nwant % x", got, want)
	}
}

func TestParseConnRejectsNonRTS(t *testing.T) {
	// A RESPONSE PDU is well-formed DCERPC but not an RTS setup PDU.
	if _, err := parseConnSetup(dcerpc.EncodeResponse(1, 0, nil)); err == nil {
		t.Fatal("expected error for a non-RTS PDU")
	}
}

func TestParseConnRejectsTruncated(t *testing.T) {
	if _, err := parseConnSetup(mustHex(t, "05001403")); err == nil {
		t.Fatal("expected error for a truncated PDU")
	}
}
