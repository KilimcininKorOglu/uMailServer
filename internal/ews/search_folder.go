package ews

import (
	"strconv"
	"strings"

	"github.com/umailserver/umailserver/internal/semcore"
)

// search_folder.go translates EWS search-folder restrictions and base-folder
// references into the canonical semcore.SearchFolderDef, and back when needed.
// It deliberately reuses the same SearchFilter tree the FindItem path parses so
// the two surfaces agree on what a saved query selects.

// fieldURISuffix returns the property name from a FieldURI, dropping the
// "item:"/"message:" prefix so item:Subject and message:Subject collapse to one.
func fieldURISuffix(uri string) string {
	if idx := strings.IndexByte(uri, ':'); idx >= 0 {
		return uri[idx+1:]
	}
	return uri
}

// searchDefFromFilter walks a restriction filter and populates def with the
// criteria it can represent (From/Subject/Body contains, DateTimeReceived
// bounds, HasAttachments). The def is a flat AND-of-criteria, so every matched
// predicate is recorded; And/Or branches are descended into. Not is skipped (a
// negation cannot be expressed in the flat def — a documented simplification),
// as are predicates over properties the def does not model.
func searchDefFromFilter(f *SearchFilter, def *semcore.SearchFolderDef) {
	if f == nil {
		return
	}
	if f.And != nil {
		searchDefFromFilter(f.And, def)
	}
	if f.Or != nil {
		searchDefFromFilter(f.Or, def)
	}
	if f.Contains != nil {
		applyContainsToDef(*f.Contains, def)
	}
	if f.IsEqualTo != nil {
		applyComparisonToDef(*f.IsEqualTo, def, false, false)
	}
	if f.IsGreaterThan != nil {
		applyComparisonToDef(*f.IsGreaterThan, def, true, false)
	}
	if f.IsGreaterThanOrEqualTo != nil {
		applyComparisonToDef(*f.IsGreaterThanOrEqualTo, def, true, false)
	}
	if f.IsLessThan != nil {
		applyComparisonToDef(*f.IsLessThan, def, false, true)
	}
	if f.IsLessThanOrEqualTo != nil {
		applyComparisonToDef(*f.IsLessThanOrEqualTo, def, false, true)
	}
}

// applyContainsToDef records a Contains predicate as the matching text criterion.
func applyContainsToDef(c ContainsFilter, def *semcore.SearchFolderDef) {
	if c.FieldURI == nil || c.Constant.Value == "" {
		return
	}
	switch fieldURISuffix(c.FieldURI.URI) {
	case "From":
		def.From = c.Constant.Value
	case "Subject":
		def.Subject = c.Constant.Value
	case "Body":
		def.Body = c.Constant.Value
	}
}

// applyComparisonToDef records a comparison predicate. lowerBound/upperBound
// indicate a DateTimeReceived range edge (>=/> set the lower bound, <=/< the
// upper). Equality on From/Subject is treated as a contains criterion (an exact
// value still narrows the set), and equality on HasAttachments sets the flag.
func applyComparisonToDef(c ComparisonFilter, def *semcore.SearchFolderDef, lowerBound, upperBound bool) {
	if c.FieldURI == nil || c.FieldURIOrConstant == nil || c.FieldURIOrConstant.Constant == nil {
		return
	}
	val := c.FieldURIOrConstant.Constant.Value
	if val == "" {
		return
	}
	switch fieldURISuffix(c.FieldURI.URI) {
	case "From":
		def.From = val
	case "Subject":
		def.Subject = val
	case "DateTimeReceived":
		switch {
		case lowerBound:
			def.DateFrom = val
		case upperBound:
			def.DateTo = val
		}
	case "HasAttachments", "HasAttachment":
		if b, err := strconv.ParseBool(val); err == nil {
			def.HasAttachment = &b
		}
	}
}

// resolveBaseFolderNames turns a search folder's BaseFolderIds into the stored
// folder display names used as the search scope. Distinguished ids resolve via
// their role's canonical name (falling back to the mailbox's folder for that
// role); explicit folder ids resolve via the identity store. The root id and
// unresolvable references are dropped, leaving an empty scope (all mail folders).
func (s *Server) resolveBaseFolderNames(mailboxKey string, ids BaseFolderIDsType) []string {
	var names []string
	for _, d := range ids.Distinguished {
		role, ok := DistinguishedFolderIDs[d.ID]
		if !ok || role == "root" {
			continue
		}
		if name := semcore.CanonicalFolderNameForRole(role); name != "" {
			names = append(names, name)
			continue
		}
		if folder, err := s.identity.GetFolderByMailbox(mailboxKey, role); err == nil {
			if name, err := s.identity.FolderNameByID(mailboxKey, folder.FolderID); err == nil {
				names = append(names, name)
			}
		}
	}
	for _, f := range ids.Folder {
		fid, err := semcore.NewFolderId(f.ID)
		if err != nil {
			continue
		}
		if name, err := s.identity.FolderNameByID(mailboxKey, fid); err == nil {
			names = append(names, name)
		}
	}
	return names
}
