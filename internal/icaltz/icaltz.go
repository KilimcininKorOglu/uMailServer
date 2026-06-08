// Package icaltz builds RFC 5545 timezone-aware DTSTART/DTEND values and the
// matching VTIMEZONE component for an IANA zone.
//
// Why this exists: a recurring calendar event must keep its CIVIL time anchored
// to an IANA timezone (e.g. "every Monday 14:00 America/New_York") so each
// client expands the recurrence in that zone and DST transitions shift the
// absolute instant correctly. Emitting a bare UTC instant (DTSTART:...Z) loses
// that anchor — across a DST boundary the wall-clock time drifts by an hour.
// The webmail/API, EWS, and JMAP iCal builders all route through here so every
// surface stores/serves the same TZID-carrying, DST-correct iCalendar.
package icaltz

import (
	"fmt"
	"strings"
	"time"
)

// IsUTC reports whether a TZID should be treated as plain UTC (no VTIMEZONE,
// bare "Z" instants): empty, "UTC", or an unloadable zone.
func IsUTC(tzid string) bool {
	if tzid == "" || strings.EqualFold(tzid, "UTC") {
		return true
	}
	_, err := time.LoadLocation(tzid)
	return err != nil
}

// FormatProperty renders a date-time property line ("DTSTART"/"DTEND"/...).
// With a real IANA tzid it emits the CIVIL local time tagged with TZID
// (DTSTART;TZID=America/New_York:20260601T140000); otherwise it emits the UTC
// instant (DTSTART:20260601T180000Z). The instant is unambiguous either way.
func FormatProperty(name, tzid string, t time.Time) string {
	if IsUTC(tzid) {
		return name + ":" + t.UTC().Format("20060102T150405Z")
	}
	loc, err := time.LoadLocation(tzid)
	if err != nil {
		return name + ":" + t.UTC().Format("20060102T150405Z")
	}
	return name + ";TZID=" + tzid + ":" + t.In(loc).Format("20060102T150405")
}

// transition is one UTC offset change within a year.
type transition struct {
	at         time.Time // the instant the new offset takes effect (UTC)
	offBefore  int       // seconds east of UTC just before
	offAfter   int       // seconds east of UTC at/after
	nameAfter  string    // zone abbreviation in effect after
	isDaylight bool      // true when the after-offset is the larger (DST) one
}

// VTimezone returns a VTIMEZONE component for the zone observed around ref's
// year, or "" for UTC/unloadable zones (caller then uses bare-UTC instants).
// Zones with DST get STANDARD + DAYLIGHT subcomponents with YEARLY BYDAY rules
// derived from the observed transitions; fixed-offset zones get a single
// STANDARD subcomponent.
func VTimezone(tzid string, ref time.Time) string {
	if IsUTC(tzid) {
		return ""
	}
	loc, err := time.LoadLocation(tzid)
	if err != nil {
		return ""
	}

	year := ref.Year()
	trans := findTransitions(loc, year)

	var b strings.Builder
	b.WriteString("BEGIN:VTIMEZONE\r\n")
	b.WriteString("TZID:" + tzid + "\r\n")

	if len(trans) < 2 {
		// No DST in this year: a single fixed-offset STANDARD subcomponent.
		_, off := time.Date(year, 1, 1, 0, 0, 0, 0, loc).Zone()
		name, _ := time.Date(year, 1, 1, 0, 0, 0, 0, loc).Zone()
		b.WriteString("BEGIN:STANDARD\r\n")
		b.WriteString("DTSTART:19700101T000000\r\n")
		b.WriteString("TZOFFSETFROM:" + formatOffset(off) + "\r\n")
		b.WriteString("TZOFFSETTO:" + formatOffset(off) + "\r\n")
		b.WriteString("TZNAME:" + name + "\r\n")
		b.WriteString("END:STANDARD\r\n")
		b.WriteString("END:VTIMEZONE\r\n")
		return b.String()
	}

	for _, tr := range trans {
		comp := "STANDARD"
		if tr.isDaylight {
			comp = "DAYLIGHT"
		}
		// Local civil time at which the transition takes effect, computed in the
		// OFFSET BEFORE the change (RFC 5545 wants the transition's local start).
		localStart := tr.at.In(time.FixedZone("", tr.offBefore))
		b.WriteString("BEGIN:" + comp + "\r\n")
		b.WriteString("DTSTART:" + localStart.Format("20060102T150405") + "\r\n")
		b.WriteString("TZOFFSETFROM:" + formatOffset(tr.offBefore) + "\r\n")
		b.WriteString("TZOFFSETTO:" + formatOffset(tr.offAfter) + "\r\n")
		b.WriteString("TZNAME:" + tr.nameAfter + "\r\n")
		b.WriteString("RRULE:" + yearlyByDayRule(localStart) + "\r\n")
		b.WriteString("END:" + comp + "\r\n")
	}
	b.WriteString("END:VTIMEZONE\r\n")
	return b.String()
}

// findTransitions scans a year minute-by-day to locate UTC offset changes,
// refining each to the minute. Returns them in chronological order (typically
// the spring DAYLIGHT change then the fall STANDARD change).
func findTransitions(loc *time.Location, year int) []transition {
	var out []transition
	prev := time.Date(year, 1, 1, 0, 0, 0, 0, loc)
	_, prevOff := prev.Zone()
	// Step day by day; when the offset differs, binary-search the boundary.
	for day := 1; day <= 366; day++ {
		cur := time.Date(year, 1, 1, 0, 0, 0, 0, loc).AddDate(0, 0, day)
		if cur.Year() != year {
			break
		}
		_, off := cur.Zone()
		if off != prevOff {
			at := refineTransition(loc, cur.AddDate(0, 0, -1), cur)
			nameAfter, offAfter := at.Zone()
			out = append(out, transition{
				at:         at.UTC(),
				offBefore:  prevOff,
				offAfter:   offAfter,
				nameAfter:  nameAfter,
				isDaylight: offAfter > prevOff,
			})
			prevOff = off
		}
	}
	return out
}

// refineTransition binary-searches between two instants (one day apart) for the
// minute at which the UTC offset changes.
func refineTransition(loc *time.Location, lo, hi time.Time) time.Time {
	_, loOff := lo.Zone()
	for hi.Sub(lo) > time.Second {
		mid := lo.Add(hi.Sub(lo) / 2)
		_, midOff := mid.Zone()
		if midOff == loOff {
			lo = mid
		} else {
			hi = mid
		}
	}
	return hi.In(loc)
}

// formatOffset renders a UTC offset (seconds east) as ±HHMM per RFC 5545.
func formatOffset(seconds int) string {
	sign := "+"
	if seconds < 0 {
		sign = "-"
		seconds = -seconds
	}
	return fmt.Sprintf("%s%02d%02d", sign, seconds/3600, (seconds%3600)/60)
}

// yearlyByDayRule builds "FREQ=YEARLY;BYMONTH=M;BYDAY=nDD" describing the nth
// weekday-of-month for the given local transition date (n=-1 for the last
// occurrence), which is how IANA DST rules are conventionally expressed.
func yearlyByDayRule(local time.Time) string {
	weekdays := []string{"SU", "MO", "TU", "WE", "TH", "FR", "SA"}
	dd := weekdays[int(local.Weekday())]
	// Which occurrence of this weekday within the month (1-based).
	nth := (local.Day()-1)/7 + 1
	// If no same-weekday falls in the following 7 days, it is the last one.
	if local.AddDate(0, 0, 7).Month() != local.Month() {
		nth = -1
	}
	return fmt.Sprintf("FREQ=YEARLY;BYMONTH=%d;BYDAY=%d%s", int(local.Month()), nth, dd)
}
