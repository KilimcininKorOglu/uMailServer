package activesync

import (
	"errors"
	"strconv"
	"strings"

	"github.com/umailserver/umailserver/internal/activesync/wbxml"
)

// Search status codes (MS-ASCMD 2.2.3.144.5/.10): 1 = success at the command and
// store level; 3 = a server error or an unsupported store.
const (
	searchStatusSuccess      = "1"
	searchStatusStoreSuccess = "1"
	searchStatusServerError  = "3"
)

// galRangeDefault bounds an absent or open-ended Range to the GAL directory cap
// the canonical address-book surfaces apply (100 entries, indices 0..99).
const galRangeDefault = 99

// GALResult is one Global Address List match returned by the Search command's GAL
// store. It carries only the fields the canonical directory exposes; Alias is the
// address local-part.
type GALResult struct {
	DisplayName string
	Email       string
	Alias       string
}

// GALSource resolves Global Address List entries for the Search command's GAL
// store. It mirrors the policy-filtered directory the NSPI/OAB address-book
// surfaces expose, so every GAL surface agrees on one source.
type GALSource interface {
	ResolveGAL(query string) []GALResult
}

// SetGALSource wires the canonical GAL source the Search command's GAL store
// queries. When nil, a GAL Search reports a store error.
func (s *Server) SetGALSource(g GALSource) {
	s.gal = g
}

// handleSearch answers the Search command (MS-ASCMD). It dispatches on the
// store name: "GAL" resolves Global Address List entries; any other store
// (Mailbox, DocumentLibrary) is not implemented and reports an overall error
// status, the signal for the client to fall back rather than hang.
func (s *Server) handleSearch(ctx *Context) ([]byte, error) {
	root, err := wbxml.Unmarshal(ctx.Body)
	if err != nil {
		return nil, err
	}
	store := root.Sub("Store")
	if store == nil {
		return searchErrorResponse(searchStatusServerError)
	}
	switch strings.ToUpper(textOf(store.Sub("Name"))) {
	case "GAL":
		return s.searchGAL(store)
	default:
		return searchErrorResponse(searchStatusServerError)
	}
}

// searchGAL resolves the GAL query, applies the requested Range window, and
// encodes the matches. Each Result carries DisplayName, Alias and EmailAddress
// under Properties; Range and Total trail the results (some older clients need
// both to render the list).
func (s *Server) searchGAL(store *wbxml.Element) ([]byte, error) {
	if s.gal == nil {
		return nil, errors.New("activesync: GAL source not configured")
	}
	query := textOf(store.Sub("Query"))
	start, end := searchRange(store)

	entries := s.gal.ResolveGAL(query)
	total := len(entries)
	lo, hi := clampRange(start, end, total)
	window := entries[lo:hi]

	storeEl := &wbxml.Element{Page: wbxml.PageSearch, Name: "Store", Children: []*wbxml.Element{
		{Page: wbxml.PageSearch, Name: "Status", Text: searchStatusStoreSuccess},
	}}
	for _, e := range window {
		storeEl.Children = append(storeEl.Children, galResult(e))
	}
	if len(window) > 0 {
		storeEl.Children = append(storeEl.Children,
			&wbxml.Element{Page: wbxml.PageSearch, Name: "Range", Text: rangeText(lo, lo+len(window)-1)},
			&wbxml.Element{Page: wbxml.PageSearch, Name: "Total", Text: strconv.Itoa(total)})
	}

	resp := &wbxml.Element{Page: wbxml.PageSearch, Name: "Search", Children: []*wbxml.Element{
		{Page: wbxml.PageSearch, Name: "Status", Text: searchStatusSuccess},
		{Page: wbxml.PageSearch, Name: "Response", Children: []*wbxml.Element{storeEl}},
	}}
	return wbxml.Marshal(resp)
}

// galResult encodes one GAL match as a Result element. DisplayName and
// EmailAddress are always present; Alias only when the directory carries it. The
// GAL properties live on the GAL code page nested inside the Search Properties.
func galResult(e GALResult) *wbxml.Element {
	props := &wbxml.Element{Page: wbxml.PageSearch, Name: "Properties", Children: []*wbxml.Element{
		{Page: wbxml.PageGAL, Name: "DisplayName", Text: e.DisplayName},
	}}
	if e.Alias != "" {
		props.Children = append(props.Children,
			&wbxml.Element{Page: wbxml.PageGAL, Name: "Alias", Text: e.Alias})
	}
	props.Children = append(props.Children,
		&wbxml.Element{Page: wbxml.PageGAL, Name: "EmailAddress", Text: e.Email})
	return &wbxml.Element{Page: wbxml.PageSearch, Name: "Result", Children: []*wbxml.Element{props}}
}

// searchErrorResponse encodes a Search response carrying only the overall Status
// (no Response block), used when the store is unsupported or unconfigured.
func searchErrorResponse(status string) ([]byte, error) {
	return wbxml.Marshal(&wbxml.Element{Page: wbxml.PageSearch, Name: "Search", Children: []*wbxml.Element{
		{Page: wbxml.PageSearch, Name: "Status", Text: status},
	}})
}

// searchRange reads Options>Range ("start-end", inclusive, zero-based). A missing
// or malformed range defaults to the full GAL window (0..99), which the caller
// clamps to the actual match count.
func searchRange(store *wbxml.Element) (start, end int) {
	end = galRangeDefault
	opts := store.Sub("Options")
	if opts == nil {
		return 0, end
	}
	r := textOf(opts.Sub("Range"))
	parts := strings.SplitN(r, "-", 2)
	if len(parts) != 2 {
		return 0, end
	}
	if a, err := strconv.Atoi(strings.TrimSpace(parts[0])); err == nil {
		start = a
	}
	if b, err := strconv.Atoi(strings.TrimSpace(parts[1])); err == nil {
		end = b
	}
	return start, end
}

// clampRange turns an inclusive [start, end] request into a valid [lo, hi) slice
// bound for a result set of size total.
func clampRange(start, end, total int) (lo, hi int) {
	if start < 0 {
		start = 0
	}
	lo = min(start, total)
	hi = max(min(end+1, total), lo)
	return lo, hi
}

// rangeText formats an inclusive "first-last" Range value.
func rangeText(first, last int) string {
	return strconv.Itoa(first) + "-" + strconv.Itoa(last)
}
