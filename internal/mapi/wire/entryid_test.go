package wire

import (
	"bytes"
	"testing"
)

// TestMuidEMSABLayout pins the address-book provider GUID's on-wire byte order,
// which a client compares byte-for-byte when validating a permanent entry ID.
func TestMuidEMSABLayout(t *testing.T) {
	p := NewPush(0)
	p.GUID(MuidEMSAB)
	want := []byte{0xDC, 0xA7, 0x40, 0xC8, 0xC0, 0x42, 0x10, 0x1A, 0xB4, 0xB9, 0x08, 0x00, 0x2B, 0x2F, 0xE1, 0x82}
	if !bytes.Equal(p.Bytes(), want) {
		t.Errorf("muidEMSAB = % x, want % x", p.Bytes(), want)
	}
}

// TestPermanentEntryID checks the documented layout (ID type 0x00, provider
// muidEMSAB, constant 1, display type, then the ASCII DN) and round-trips.
func TestPermanentEntryID(t *testing.T) {
	e := PermanentEntryID{DisplayType: 0, X500DN: BuildESSDN("alice")}
	b := e.Bytes()
	// First 4 bytes: ID type 0x00 + 3 reserved.
	if !bytes.Equal(b[:4], []byte{0, 0, 0, 0}) {
		t.Errorf("permanent ID-type prefix = % x, want 00 00 00 00", b[:4])
	}
	// Bytes 4..20 are the provider GUID.
	if !bytes.Equal(b[4:20], []byte{0xDC, 0xA7, 0x40, 0xC8, 0xC0, 0x42, 0x10, 0x1A, 0xB4, 0xB9, 0x08, 0x00, 0x2B, 0x2F, 0xE1, 0x82}) {
		t.Errorf("permanent provider GUID = % x", b[4:20])
	}
	// Bytes 20..24 are the constant R4 = 1.
	if !bytes.Equal(b[20:24], []byte{0x01, 0x00, 0x00, 0x00}) {
		t.Errorf("permanent R4 = % x, want 01 00 00 00", b[20:24])
	}
	got, err := PullPermanentEntryID(NewPull(b, 0))
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	if got.DisplayType != e.DisplayType || got.X500DN != e.X500DN {
		t.Errorf("round-trip = %+v, want %+v", got, e)
	}
}

// TestPermanentEntryIDRejectsWrongProvider ensures a foreign provider GUID is
// not silently accepted.
func TestPermanentEntryIDRejectsWrongProvider(t *testing.T) {
	b := PermanentEntryID{X500DN: "x"}.Bytes()
	b[4] ^= 0xFF // corrupt the provider GUID
	if _, err := PullPermanentEntryID(NewPull(b, 0)); err == nil {
		t.Error("expected error for corrupted provider GUID")
	}
}

// TestEphemeralEntryID checks the 0x87 ID-type prefix and round-trips the MId.
func TestEphemeralEntryID(t *testing.T) {
	srv := GUID{TimeLow: 0x11223344, TimeMid: 0x5566, TimeHiAndVersion: 0x7788, ClockSeq: [2]byte{0x99, 0xAA}, Node: [6]byte{0xBB, 0xCC, 0xDD, 0xEE, 0xFF, 0x00}}
	e := EphemeralEntryID{ProviderUID: srv, DisplayType: 0, Mid: 0x1234}
	b := e.Bytes()
	if len(b) != 32 {
		t.Fatalf("ephemeral length = %d, want 32", len(b))
	}
	if !bytes.Equal(b[:4], []byte{0x87, 0, 0, 0}) {
		t.Errorf("ephemeral ID-type prefix = % x, want 87 00 00 00", b[:4])
	}
	got, err := PullEphemeralEntryID(NewPull(b, 0))
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	if got != e {
		t.Errorf("round-trip = %+v, want %+v", got, e)
	}
}

// TestFolderEntryIDLength pins the 46-byte folder entry ID and round-trips it.
func TestFolderEntryIDLength(t *testing.T) {
	e := FolderEntryID{
		FolderType:    EIDTypePrivateFolder,
		ProviderUID:   GUID{TimeLow: 1},
		DatabaseGUID:  GUID{TimeLow: 2},
		GlobalCounter: [6]byte{1, 2, 3, 4, 5, 6},
	}
	b := e.Bytes()
	if len(b) != 46 {
		t.Fatalf("folder entry ID length = %d, want 46", len(b))
	}
	if got := PullFolderEntryID(NewPull(b, 0)); got != e {
		t.Errorf("round-trip = %+v, want %+v", got, e)
	}
}

// TestMessageEntryIDLength pins the 70-byte message entry ID and round-trips it.
func TestMessageEntryIDLength(t *testing.T) {
	e := MessageEntryID{
		MessageType:          EIDTypePrivateMessage,
		FolderDatabaseGUID:   GUID{TimeLow: 3},
		MessageDatabaseGUID:  GUID{TimeLow: 4},
		FolderGlobalCounter:  [6]byte{1, 1, 1, 1, 1, 1},
		MessageGlobalCounter: [6]byte{2, 2, 2, 2, 2, 2},
	}
	b := e.Bytes()
	if len(b) != 70 {
		t.Fatalf("message entry ID length = %d, want 70", len(b))
	}
	if got := PullMessageEntryID(NewPull(b, 0)); got != e {
		t.Errorf("round-trip = %+v, want %+v", got, e)
	}
}

// TestESSDN verifies the legacy-DN build/parse pair, including the
// case-insensitive match Exchange clients rely on and rejection of a foreign DN.
func TestESSDN(t *testing.T) {
	dn := BuildESSDN("qa.bob")
	if local, ok := ParseESSDN(dn); !ok || local != "qa.bob" {
		t.Errorf("ParseESSDN(%q) = %q, %v; want qa.bob, true", dn, local, ok)
	}
	// Uppercased path segment must still resolve.
	upper := "/o=uMailServer/ou=Exchange Administrative Group (FYDIBOHF23SPDLT)/CN=Recipients/CN=qa.alice"
	if local, ok := ParseESSDN(upper); !ok || local != "qa.alice" {
		t.Errorf("ParseESSDN(upper) = %q, %v; want qa.alice, true", local, ok)
	}
	if _, ok := ParseESSDN("/o=Other/cn=nope"); ok {
		t.Error("ParseESSDN should reject a DN without the recipients segment")
	}
}
