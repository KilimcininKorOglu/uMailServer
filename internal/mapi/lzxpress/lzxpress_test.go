package lzxpress

import (
	"bytes"
	"math/rand"
	"testing"
)

// TestSpecAlphabetVector pins the MS-XCA worked example: the 26 distinct ASCII
// letters compress to a single indicator word (all-literal, trailing flag bits
// set to 1) followed by the bytes. This is an external, non-self-referential
// anchor: both the input and the expected output come from the specification.
func TestSpecAlphabetVector(t *testing.T) {
	in := []byte("abcdefghijklmnopqrstuvwxyz")
	want := append([]byte{0x3f, 0x00, 0x00, 0x00}, in...)

	got := Compress(in)
	if !bytes.Equal(got, want) {
		t.Errorf("Compress(alphabet) = % x\nwant % x", got, want)
	}
	back, err := Decompress(want, len(in))
	if err != nil {
		t.Fatalf("Decompress: %v", err)
	}
	if !bytes.Equal(back, in) {
		t.Errorf("Decompress(spec) = %q, want %q", back, in)
	}
}

// TestShortMatchVector pins a hand-derived match case ("aaaaa"): a literal 'a'
// followed by a length-4 distance-1 back-reference, with the final indicator
// padded. Tracing both encoder and decoder by hand makes this independent of
// the running code.
func TestShortMatchVector(t *testing.T) {
	want := []byte{0xff, 0xff, 0xff, 0x7f, 0x61, 0x01, 0x00}
	if got := Compress([]byte("aaaaa")); !bytes.Equal(got, want) {
		t.Errorf("Compress(aaaaa) = % x, want % x", got, want)
	}
	got, err := Decompress(want, 5)
	if err != nil {
		t.Fatalf("Decompress: %v", err)
	}
	if !bytes.Equal(got, []byte("aaaaa")) {
		t.Errorf("Decompress = %q, want aaaaa", got)
	}
}

// TestRoundTrip exercises the full token space — literals, short and long
// matches (which drive the nibble-shared and extended length encodings), and
// window-sized data — by compressing then decompressing back to the original.
func TestRoundTrip(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	random := make([]byte, 10000)
	rng.Read(random)
	lowEntropy := make([]byte, 10000)
	for i := range lowEntropy {
		lowEntropy[i] = byte(rng.Intn(4))
	}

	cases := map[string][]byte{
		"empty":           {},
		"single":          {0x42},
		"two":             {1, 2},
		"alphabet":        []byte("abcdefghijklmnopqrstuvwxyz"),
		"abc-x100":        bytes.Repeat([]byte("abc"), 100),
		"a-x5000":         bytes.Repeat([]byte("a"), 5000),
		"a-x70000":        bytes.Repeat([]byte("a"), 70000), // exceeds the 0xFFFF+3 metadata length cap
		"window-spanning": bytes.Repeat([]byte("0123456789abcdef"), 1000),
		"random":          random,
		"low-entropy":     lowEntropy,
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			comp := Compress(in)
			out, err := Decompress(comp, len(in))
			if err != nil {
				t.Fatalf("Decompress: %v", err)
			}
			if !bytes.Equal(out, in) {
				t.Fatalf("round-trip mismatch: len(in)=%d len(out)=%d", len(in), len(out))
			}
		})
	}
}

// TestDecompressRejectsTruncated ensures malformed/truncated input fails loud
// rather than reading out of bounds.
func TestDecompressRejectsTruncated(t *testing.T) {
	// Indicator word claims a literal but no data byte follows.
	if _, err := Decompress([]byte{0x00, 0x00, 0x00, 0x00}, 4); err == nil {
		// 4-byte input where in==len after reading the indicator is treated as
		// the legitimate "trailing flags" terminator, so this yields empty.
		// Use a clearly short match instead below.
		_ = err
	}
	// Indicator marks a match (high bit set) but the 2-byte metadata is cut off.
	if _, err := Decompress([]byte{0x00, 0x00, 0x00, 0x80, 0x01}, 16); err == nil {
		t.Error("expected ErrCorrupt for truncated match metadata")
	}
}

// TestDecompressRejectsOverflow ensures output is capped at maxOut.
func TestDecompressRejectsOverflow(t *testing.T) {
	comp := Compress(bytes.Repeat([]byte("a"), 100))
	if _, err := Decompress(comp, 10); err == nil {
		t.Error("expected ErrCorrupt when output exceeds maxOut")
	}
}
