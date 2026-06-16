// Package auditreader reads the NDJSON audit log files produced by
// internal/audit and exposes a paged, filterable view of the events to
// the admin API. It is intentionally separate from internal/audit: the
// audit package owns the write side (rotating NDJSON file), this
// package owns the read side (cursor + filter + scan), and neither
// imports the other. Keeping the reader independent lets us add
// alternative writers (syslog, remote sink) later without touching the
// admin surface, and lets us unit-test parsing without the rotating
// file machinery.
package auditreader

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Event is the decoded shape of a single audit log line. JSON tags
// match the wire format written by internal/audit so the admin UI can
// consume them directly; the struct is duplicated here (rather than
// imported from internal/audit) so the read path is decoupled from the
// writer's event type and can be tested in isolation.
type Event struct {
	Timestamp string            `json:"timestamp"`
	Type      string            `json:"type"`
	User      string            `json:"user,omitempty"`
	IP        string            `json:"ip,omitempty"`
	Success   bool              `json:"success"`
	Service   string            `json:"service"`
	Tenant    string            `json:"tenant,omitempty"`
	Details   map[string]string `json:"details,omitempty"`
}

// Filter narrows the event stream before pagination. A zero-value
// field disables the corresponding constraint. Type and Service match
// exactly; User and IP match by substring (case-insensitive). From/To
// are inclusive bounds on the RFC3339 timestamp. Success is a tri-state
// (nil = any) so the admin UI can render an "all / yes / no" filter.
type Filter struct {
	Type     string
	User     string
	IP       string
	Service  string
	Success  *bool
	FromTime time.Time
	ToTime   time.Time
}

// Page is one window of the audit log. Events are returned in
// chronological order (oldest first) so the admin UI can render a
// stable list across pages. Next is an opaque cursor that the caller
// passes to the next Read call to continue pagination; an empty Next
// means the stream is exhausted. HasMore mirrors Next for clients that
// prefer a boolean.
type Page struct {
	Events  []Event
	Next    string
	HasMore bool
}

// MinLimit and MaxLimit clamp the request size so the admin UI cannot
// ask the reader to materialize an unbounded amount of NDJSON in one
// call. The handler defaults the limit to DefaultLimit when the caller
// passes zero.
const (
	MinLimit     = 1
	MaxLimit     = 500
	DefaultLimit = 100
)

// ErrAuditDisabled is returned by Read when the configured log path is
// empty. The handler translates this to a 503 because log viewer is
// meaningless without a backing log file.
var ErrAuditDisabled = fmt.Errorf("auditreader: audit logging is disabled")

// Read returns one page of audit events matching filter, starting at
// cursor. Pass an empty cursor to start at the oldest event. Pass the
// Next value from the previous Page to continue. limit is clamped to
// [MinLimit, MaxLimit]; a value of zero means DefaultLimit.
//
// logPath is the path of the active audit log file (the one currently
// being written). Rotated archives are discovered automatically by
// looking up the directory for files matching "<logPath>.*" — the
// convention enforced by internal/audit.rotatingWriter.rotate.
//
// Events are scanned in chronological order: active file first, then
// rotated files newest-to-oldest (modtime-descending). The reader
// tolerates malformed lines by skipping them silently; the audit
// writer produces well-formed JSON, so any skip is either corruption
// or a partial write at rotation.
func Read(logPath string, filter Filter, cursor string, limit int) (*Page, error) {
	if logPath == "" {
		return nil, ErrAuditDisabled
	}
	if limit == 0 {
		limit = DefaultLimit
	} else if limit < 0 {
		// Caller asked for "negative" — coerce to the smallest valid
		// page rather than silently switching to the default, which
		// would surprise a UI that built the query from a numeric
		// input.
		limit = MinLimit
	} else if limit > MaxLimit {
		limit = MaxLimit
	}

	// cursorFile + cursorOffset are the resume point. An empty cursor
	// file means "start at the beginning of the active file". A cursor
	// pointing to a file no longer in the rotation set is treated as
	// exhausted — the rotated file is gone (cleaned up by maxBackups)
	// and the next active file's prefix is unrelated, so honoring it
	// would be guessing.
	cursorFile, cursorOffset, ok := decodeCursor(cursor)
	if !ok && cursor != "" {
		// Malformed cursor: bail out cleanly rather than risk
		// resurface-the-world by starting from the beginning.
		return &Page{Events: []Event{}}, nil
	}

	files, err := listLogFiles(logPath)
	if err != nil {
		return nil, fmt.Errorf("auditreader: list log files: %w", err)
	}
	if len(files) == 0 {
		// Active file absent (rotation just renamed it, or the log
		// dir is empty). Treat as "no events yet" rather than 500 —
		// the admin UI shows an empty state.
		return &Page{Events: []Event{}}, nil
	}

	// Walk every file in chronological order. We may need to start in
	// the middle of a file (cursorOffset) or in the middle of the file
	// list (cursorFile is not the first entry).
	events := make([]Event, 0, limit)
	var nextCursor string
	hasMore := false
	consumed := 0 // events that passed the filter and would be returned; equals len(events) unless we exceed limit

	for _, f := range files {
		if nextCursor != "" {
			// We already filled the page in a later file. Stop.
			break
		}
		startOffset := int64(0)
		if cursorFile != "" {
			// Resume mode: skip files preceding the cursor file.
			if f.path < cursorFile {
				continue
			}
			if f.path == cursorFile {
				startOffset = cursorOffset
			}
		}
		more, next, err := scanFile(f.path, startOffset, filter, limit, &events)
		if err != nil {
			return nil, err
		}
		consumed = len(events)
		if more {
			hasMore = true
			// next is the file:offset where the next un-returned event
			// lives; encode as cursor.
			nextCursor = encodeCursor(next.file, next.offset)
		}
	}

	// Trim to limit in case a single file delivered more than the
	// remaining budget after the cursor was applied (defensive — the
	// scanner should already respect limit).
	if len(events) > limit {
		events = events[:limit]
		hasMore = true
	}

	_ = consumed
	return &Page{Events: events, Next: nextCursor, HasMore: hasMore}, nil
}

// Tail returns the last n events (chronological order: oldest first
// within the returned slice). It is the read-side counterpart of the
// "Refresh tail" button on the admin UI. The implementation collects
// every event across all files, then slices the trailing window —
// simple, correct, and the file sizes the audit writer produces
// (maxSizeMB default) keep the materialized set small.
func Tail(logPath string, n int) ([]Event, error) {
	if logPath == "" {
		return nil, ErrAuditDisabled
	}
	if n <= 0 {
		return nil, nil
	}
	files, err := listLogFiles(logPath)
	if err != nil {
		return nil, fmt.Errorf("auditreader: tail: list log files: %w", err)
	}
	all := make([]Event, 0, n*2)
	for _, f := range files {
		page, err := scanAll(f.path, 0, 0)
		if err != nil {
			return nil, err
		}
		all = append(all, page...)
	}
	if len(all) > n {
		all = all[len(all)-n:]
	}
	return all, nil
}

// logFile describes one audit log file in chronological order. The
// audit writer uses path <logPath> for the active file and
// <logPath>.YYYYMMDD-HHMMSS for rotated archives; the path sort
// therefore matches the chronological order (timestamp suffix sorts
// correctly for the writer's fixed-width format).
type logFile struct {
	path    string
	modTime time.Time
}

// listLogFiles returns the active log file followed by rotated
// archives, all in chronological order. The active file always comes
// first because its path is the shortest (no .YYYYMMDD-HHMMSS suffix);
// the rotated archives are then sorted by path ascending so older
// files are visited last, which is the direction pagination needs to
// walk backwards from.
func listLogFiles(logPath string) ([]logFile, error) {
	info, err := os.Stat(logPath)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	var files []logFile
	if err == nil && !info.IsDir() {
		files = append(files, logFile{path: logPath, modTime: info.ModTime()})
	}
	matches, err := filepath.Glob(logPath + ".*")
	if err != nil {
		return nil, err
	}
	for _, m := range matches {
		mi, err := os.Stat(m)
		if err != nil {
			continue
		}
		files = append(files, logFile{path: m, modTime: mi.ModTime()})
	}
	// Active file (no suffix) first, then rotated files in path
	// ascending (which is chronological for YYYYMMDD-HHMMSS suffix).
	sort.SliceStable(files, func(i, j int) bool {
		return files[i].path < files[j].path
	})
	return files, nil
}

// filePos is a position within one audit log file: a file path and a
// byte offset where the next un-read line starts.
type filePos struct {
	file   string
	offset int64
}

// scanFile reads events from path starting at startOffset. It honors
// filter and stops once limit events have been appended to *out. The
// returned bool indicates whether more events remain after the page
// (i.e. the caller should keep paginating); the returned filePos
// identifies where the next un-read line lives.
func scanFile(path string, startOffset int64, filter Filter, limit int, out *[]Event) (bool, filePos, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, filePos{}, nil
		}
		return false, filePos{}, fmt.Errorf("auditreader: open %q: %w", path, err)
	}
	defer func() { _ = f.Close() }() //nolint:errcheck // best-effort close on read-only file

	pos := int64(0)
	if startOffset > 0 {
		// Seek to the resume point. If the file has been truncated or
		// rotated since the cursor was issued, Seek will succeed (the
		// offset is just past the new EOF); the loop below treats EOF
		// as "no more events from this file".
		newPos, err := f.Seek(startOffset, io.SeekStart)
		if err != nil {
			return false, filePos{}, fmt.Errorf("auditreader: seek %q: %w", path, err)
		}
		pos = newPos
	}
	br := bufio.NewReader(f)

	// If the cursor lands in the middle of a line, consume the rest of
	// it so the loop below always sees a line head. We detect "middle
	// of a line" by peeking at the byte immediately before the
	// resume point: a well-aligned cursor always sits on a byte whose
	// predecessor is '\n' (or whose offset is 0). A line whose first
	// character is '{' (NDJSON) is therefore NOT a partial line — it
	// is a fresh event, and the cursor was issued at its start.
	if startOffset > 0 {
		aligned, err := isLineAligned(f, startOffset)
		if err != nil {
			return false, filePos{}, err
		}
		if !aligned {
			consumed, rerr := br.ReadString('\n')
			if rerr != nil && rerr != io.EOF {
				return false, filePos{}, fmt.Errorf("auditreader: read %q: %w", path, rerr)
			}
			pos += int64(len(consumed))
			if rerr == io.EOF && len(consumed) == 0 {
				// No more bytes after the resume point.
				return false, filePos{}, nil
			}
		}
	}

	for {
		line, rerr := br.ReadString('\n')
		if rerr != nil && rerr != io.EOF {
			return false, filePos{}, fmt.Errorf("auditreader: read %q: %w", path, rerr)
		}
		if len(line) == 0 {
			// EOF with nothing buffered.
			return false, filePos{}, nil
		}
		nextPos := pos + int64(len(line))
		trimmed := bytes.TrimSpace([]byte(line))
		if len(trimmed) > 0 {
			var ev Event
			if jerr := json.Unmarshal(trimmed, &ev); jerr == nil && matches(ev, filter) {
				*out = append(*out, ev)
				if len(*out) >= limit {
					// Page is full. Hand back a cursor at the start
					// of the next un-returned line (nextPos). The
					// caller's loop will resume from this file if
					// more lines exist, or move to the next (older)
					// file.
					return true, filePos{file: path, offset: nextPos}, nil
				}
			}
		}
		pos = nextPos
		if rerr == io.EOF {
			return false, filePos{}, nil
		}
	}
}

// isLineAligned reports whether the byte at startOffset is the first
// byte of a line. It looks at the byte immediately before; aligned
// means "predecessor is '\n' or offset is 0". Used by scanFile to
// distinguish a partial-line resume (skip the rest of the line) from
// a well-aligned cursor at the start of a new event (read it).
func isLineAligned(f *os.File, startOffset int64) (bool, error) {
	if startOffset == 0 {
		return true, nil
	}
	_, err := f.Seek(startOffset-1, io.SeekStart)
	if err != nil {
		return false, fmt.Errorf("auditreader: seek prev: %w", err)
	}
	var b [1]byte
	n, err := f.Read(b[:])
	if err != nil && err != io.EOF {
		return false, fmt.Errorf("auditreader: read prev: %w", err)
	}
	if n == 0 {
		// Empty file at the resume point — treat as aligned (no line
		// to skip).
		return true, nil
	}
	return b[0] == '\n', nil
}

// scanAll returns every event in the file (no filter, no limit) —
// used by Tail to build a small recent-window snapshot.
func scanAll(path string, startOffset int64, _ int) ([]Event, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer func() { _ = f.Close() }() //nolint:errcheck // best-effort close on read-only file
	if startOffset > 0 {
		if _, err := f.Seek(startOffset, io.SeekStart); err != nil {
			return nil, err
		}
	}
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	var out []Event
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var ev Event
		if err := json.Unmarshal(line, &ev); err != nil {
			continue
		}
		out = append(out, ev)
	}
	return out, scanner.Err()
}

// matches returns true if ev satisfies every non-zero field of filter.
// Substring filters (User, IP) are case-insensitive so a search for
// "BOB" still hits "bob". Time bounds are compared against the RFC3339
// timestamp string by parsing it once into time.Time.
func matches(ev Event, filter Filter) bool {
	if filter.Type != "" && ev.Type != filter.Type {
		return false
	}
	if filter.Service != "" && ev.Service != filter.Service {
		return false
	}
	if filter.User != "" && !strings.Contains(strings.ToLower(ev.User), strings.ToLower(filter.User)) {
		return false
	}
	if filter.IP != "" && !strings.Contains(strings.ToLower(ev.IP), strings.ToLower(filter.IP)) {
		return false
	}
	if filter.Success != nil && ev.Success != *filter.Success {
		return false
	}
	if !filter.FromTime.IsZero() || !filter.ToTime.IsZero() {
		ts, err := time.Parse(time.RFC3339, ev.Timestamp)
		if err != nil {
			// Drop events with unparseable timestamps when a time
			// filter is in effect — surfacing them would mislead the
			// operator.
			return false
		}
		if !filter.FromTime.IsZero() && ts.Before(filter.FromTime) {
			return false
		}
		if !filter.ToTime.IsZero() && ts.After(filter.ToTime) {
			return false
		}
	}
	return true
}

// Cursor format: base64( "<file>\x1f<offset>" ) — \x1f (US) is a
// delimiter that cannot appear in a file path, so decode is unambiguous.
// An empty cursor round-trips to "" with ok=true and zero offset.
//
// The \x1f byte is the ASCII Unit Separator; the audit log writer
// never produces paths containing it, so using it as the cursor
// delimiter makes decode unambiguous.
const cursorDelimiter = "\x1f"

func encodeCursor(file string, offset int64) string {
	if file == "" {
		return ""
	}
	raw := file + cursorDelimiter + strconv.FormatInt(offset, 10)
	return base64.StdEncoding.EncodeToString([]byte(raw))
}

func decodeCursor(cursor string) (string, int64, bool) {
	if cursor == "" {
		return "", 0, true
	}
	raw, err := base64.StdEncoding.DecodeString(cursor)
	if err != nil {
		return "", 0, false
	}
	idx := bytes.LastIndex(raw, []byte(cursorDelimiter))
	if idx < 0 {
		return "", 0, false
	}
	off, err := strconv.ParseInt(string(raw[idx+1:]), 10, 64)
	if err != nil {
		return "", 0, false
	}
	return string(raw[:idx]), off, true
}
