package wire

import "time"

// fileTimeEpochDiff is the number of seconds between the FILETIME epoch
// (1601-01-01 UTC) and the Unix epoch (1970-01-01 UTC).
const fileTimeEpochDiff = 11644473600

// FileTimeFromTime converts a time.Time to a MAPI FILETIME: the count of
// 100-nanosecond intervals since 1601-01-01 UTC. The zero time maps to 0.
func FileTimeFromTime(t time.Time) uint64 {
	if t.IsZero() {
		return 0
	}
	return uint64(t.Unix()+fileTimeEpochDiff)*10_000_000 + uint64(t.Nanosecond()/100)
}

// TimeFromFileTime converts a MAPI FILETIME (100-nanosecond intervals since
// 1601-01-01 UTC) to a UTC time.Time. A zero FILETIME maps to the zero time.
func TimeFromFileTime(ft uint64) time.Time {
	if ft == 0 {
		return time.Time{}
	}
	sec := int64(ft/10_000_000) - fileTimeEpochDiff
	nsec := int64(ft%10_000_000) * 100
	return time.Unix(sec, nsec).UTC()
}
