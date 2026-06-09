package smtp

import (
	"testing"
	"time"
)

// TestParseFutureRelease covers the RFC 4865 HOLDFOR/HOLDUNTIL MAIL FROM parameter.
func TestParseFutureRelease(t *testing.T) {
	now := time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC)
	maxSecs := 365 * 24 * 60 * 60 // one year

	t.Run("none", func(t *testing.T) {
		got, err := parseFutureRelease("FROM:<a@x.com> SIZE=10", now, maxSecs)
		if err != nil || !got.IsZero() {
			t.Fatalf("no param: got %v err %v, want zero/nil", got, err)
		}
	})

	t.Run("holdfor", func(t *testing.T) {
		got, err := parseFutureRelease("FROM:<a@x.com> HOLDFOR=3600", now, maxSecs)
		if err != nil {
			t.Fatalf("HOLDFOR: %v", err)
		}
		if want := now.Add(time.Hour); !got.Equal(want) {
			t.Errorf("HOLDFOR=3600: got %v, want %v", got, want)
		}
	})

	t.Run("holduntil", func(t *testing.T) {
		got, err := parseFutureRelease("FROM:<a@x.com> HOLDUNTIL=2026-06-10T15:00:00Z", now, maxSecs)
		if err != nil {
			t.Fatalf("HOLDUNTIL: %v", err)
		}
		want, perr := time.Parse(time.RFC3339, "2026-06-10T15:00:00Z")
		if perr != nil {
			t.Fatalf("parse want: %v", perr)
		}
		if !got.Equal(want) {
			t.Errorf("HOLDUNTIL: got %v, want %v", got, want)
		}
	})

	t.Run("mutually exclusive", func(t *testing.T) {
		if _, err := parseFutureRelease("FROM:<a@x.com> HOLDFOR=60 HOLDUNTIL=2026-06-10T15:00:00Z", now, maxSecs); err == nil {
			t.Error("HOLDFOR + HOLDUNTIL together must be rejected")
		}
	})

	t.Run("beyond max", func(t *testing.T) {
		if _, err := parseFutureRelease("FROM:<a@x.com> HOLDFOR=99999999", now, maxSecs); err == nil {
			t.Error("a hold beyond the server maximum must be rejected")
		}
	})

	t.Run("invalid holdfor", func(t *testing.T) {
		if _, err := parseFutureRelease("FROM:<a@x.com> HOLDFOR=soon", now, maxSecs); err == nil {
			t.Error("non-numeric HOLDFOR must be rejected")
		}
	})

	t.Run("negative holdfor", func(t *testing.T) {
		if _, err := parseFutureRelease("FROM:<a@x.com> HOLDFOR=-5", now, maxSecs); err == nil {
			t.Error("negative HOLDFOR must be rejected")
		}
	})
}
