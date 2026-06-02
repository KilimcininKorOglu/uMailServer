package api

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestParseAvatarDataURL(t *testing.T) {
	// A 1x1 transparent PNG, base64-encoded, as a browser would produce.
	pngB64 := "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg=="
	good := "data:image/png;base64," + pngB64

	mime, raw, err := parseAvatarDataURL(good)
	if err != nil {
		t.Fatalf("valid PNG data URL rejected: %v", err)
	}
	if mime != "image/png" {
		t.Errorf("mime = %q, want image/png", mime)
	}
	if len(raw) == 0 {
		t.Error("decoded bytes are empty")
	}

	cases := []struct {
		name string
		in   string
	}{
		{"not a data URL", "https://example.com/a.png"},
		{"missing base64 marker", "data:image/png,plaintext"},
		{"unsupported type", "data:image/svg+xml;base64," + pngB64},
		{"invalid base64", "data:image/png;base64,@@@notbase64@@@"},
		{"empty payload", "data:image/png;base64,"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := parseAvatarDataURL(tc.in); err == nil {
				t.Errorf("expected rejection for %s", tc.name)
			}
		})
	}

	// Oversized payload must be rejected by the size cap, not silently stored.
	big := make([]byte, maxAvatarBytes+1)
	oversized := "data:image/png;base64," + base64.StdEncoding.EncodeToString(big)
	if _, _, err := parseAvatarDataURL(oversized); err == nil || !strings.Contains(err.Error(), "limit") {
		t.Errorf("oversized image not rejected by size cap: err=%v", err)
	}
}
