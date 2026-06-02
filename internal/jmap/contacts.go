package jmap

import (
	"sort"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"github.com/umailserver/umailserver/internal/carddav"
)

// JMAP Contacts (RFC 8620 method patterns; objects in JSContact, RFC 9553) are
// backed by the canonical carddav.CollabStore — the same semcore collaboration
// folder EWS, CardDAV, and the webmail address book read and write. A contact
// created over JMAP is therefore visible from every surface and vice versa; each
// surface only translates the shared vCard at its own boundary.
//
// A ContactCard's JMAP id is its vCard UID, the canonical key the collaboration
// store upserts on, so ids are stable across edits and across protocols.

const (
	jmapDefaultAddressBookID   = "default"
	jmapDefaultAddressBookName = "Contacts"
)

// contactsEnabled reports whether the Contacts capability is wired.
func (s *Server) contactsEnabled() bool { return s.cardStore != nil }

// contactsState returns an opaque state string derived from the collection
// ETag, so ContactCard/get|query|changes agree on a single state value.
func (s *Server) contactsState(user string) string {
	etag := s.cardStore.GetAddressbookETag(user, jmapDefaultAddressBookID)
	return strings.Trim(etag, `"`)
}

// ---- AddressBook/get --------------------------------------------------------

func (s *Server) handleAddressBookGet(user string, call MethodCall) Response {
	accountID := argString(call.Args, "accountId")
	if valid, resp := validateAccountId(accountID, user, "AddressBook/get", call.ID); !valid {
		return resp
	}
	if !s.contactsEnabled() {
		return jmapError(call.ID, "notSupported", "contacts are not available")
	}

	ab := map[string]interface{}{
		"id":             jmapDefaultAddressBookID,
		"name":           jmapDefaultAddressBookName,
		"description":    nil,
		"sortOrder":      float64(0),
		"isDefault":      true,
		"isSubscribed":   true,
		"mayReadItems":   true,
		"mayAddItems":    true,
		"mayModifyItems": true,
		"mayRemoveItems": true,
		"mayRename":      false,
		"mayDelete":      false,
	}

	ids, hasIDs := call.Args["ids"].([]interface{})
	list := []interface{}{}
	notFound := []string{}
	if hasIDs {
		for _, raw := range ids {
			id, isStr := raw.(string)
			switch {
			case isStr && id == jmapDefaultAddressBookID:
				list = append(list, ab)
			case isStr:
				notFound = append(notFound, id)
			}
		}
	} else {
		list = append(list, ab)
	}

	return Response{
		Name: "AddressBook/get",
		Args: map[string]interface{}{
			"accountId": accountID,
			"state":     s.contactsState(user),
			"list":      list,
			"notFound":  notFound,
		},
		ID: call.ID,
	}
}

// ---- ContactCard/get --------------------------------------------------------

func (s *Server) handleContactCardGet(user string, call MethodCall) Response {
	accountID := argString(call.Args, "accountId")
	if valid, resp := validateAccountId(accountID, user, "ContactCard/get", call.ID); !valid {
		return resp
	}
	if !s.contactsEnabled() {
		return jmapError(call.ID, "notSupported", "contacts are not available")
	}

	raws, err := s.cardStore.GetContacts(user, jmapDefaultAddressBookID)
	if err != nil {
		return jmapError(call.ID, "serverFail", err.Error())
	}
	byUID := map[string]map[string]interface{}{}
	for _, vcf := range raws {
		if c, ok := parseVCardContact(vcf); ok {
			byUID[c.UID] = contactToJSContact(c)
		}
	}

	list := []interface{}{}
	notFound := []string{}
	if ids, hasIDs := call.Args["ids"].([]interface{}); hasIDs {
		for _, raw := range ids {
			id, isStr := raw.(string)
			if !isStr {
				continue
			}
			if obj, found := byUID[id]; found {
				list = append(list, obj)
			} else {
				notFound = append(notFound, id)
			}
		}
	} else {
		uids := make([]string, 0, len(byUID))
		for uid := range byUID {
			uids = append(uids, uid)
		}
		sort.Strings(uids)
		for _, uid := range uids {
			list = append(list, byUID[uid])
		}
	}

	return Response{
		Name: "ContactCard/get",
		Args: map[string]interface{}{
			"accountId": accountID,
			"state":     s.contactsState(user),
			"list":      list,
			"notFound":  notFound,
		},
		ID: call.ID,
	}
}

// ---- ContactCard/query ------------------------------------------------------

func (s *Server) handleContactCardQuery(user string, call MethodCall) Response {
	accountID := argString(call.Args, "accountId")
	if valid, resp := validateAccountId(accountID, user, "ContactCard/query", call.ID); !valid {
		return resp
	}
	if !s.contactsEnabled() {
		return jmapError(call.ID, "notSupported", "contacts are not available")
	}

	raws, err := s.cardStore.GetContacts(user, jmapDefaultAddressBookID)
	if err != nil {
		return jmapError(call.ID, "serverFail", err.Error())
	}
	ids := []string{}
	for _, vcf := range raws {
		if c, ok := parseVCardContact(vcf); ok {
			ids = append(ids, c.UID)
		}
	}
	sort.Strings(ids)

	return Response{
		Name: "ContactCard/query",
		Args: map[string]interface{}{
			"accountId":           accountID,
			"queryState":          s.contactsState(user),
			"canCalculateChanges": false,
			"position":            float64(0),
			"total":               float64(len(ids)),
			"ids":                 toIfaceStrings(ids),
		},
		ID: call.ID,
	}
}

// ---- ContactCard/changes ----------------------------------------------------

func (s *Server) handleContactCardChanges(user string, call MethodCall) Response {
	accountID := argString(call.Args, "accountId")
	if valid, resp := validateAccountId(accountID, user, "ContactCard/changes", call.ID); !valid {
		return resp
	}
	if !s.contactsEnabled() {
		return jmapError(call.ID, "notSupported", "contacts are not available")
	}
	current := s.contactsState(user)
	since := argString(call.Args, "sinceState")
	if since == current {
		return Response{
			Name: "ContactCard/changes",
			Args: map[string]interface{}{
				"accountId":      accountID,
				"oldState":       since,
				"newState":       current,
				"hasMoreChanges": false,
				"created":        []interface{}{},
				"updated":        []interface{}{},
				"destroyed":      []interface{}{},
			},
			ID: call.ID,
		}
	}
	return jmapError(call.ID, "cannotCalculateChanges", "contact change log is not available; re-fetch with ContactCard/get")
}

// ---- ContactCard/set --------------------------------------------------------

func (s *Server) handleContactCardSet(user string, call MethodCall, createdIDs map[string]string) Response {
	accountID := argString(call.Args, "accountId")
	if valid, resp := validateAccountId(accountID, user, "ContactCard/set", call.ID); !valid {
		return resp
	}
	if !s.contactsEnabled() {
		return jmapError(call.ID, "notSupported", "contacts are not available")
	}

	oldState := s.contactsState(user)
	created := map[string]interface{}{}
	notCreated := map[string]interface{}{}
	updated := map[string]interface{}{}
	notUpdated := map[string]interface{}{}
	destroyed := []string{}
	notDestroyed := map[string]interface{}{}

	for creationID, raw := range argMap(call.Args, "create") {
		props, ok := raw.(map[string]interface{})
		if !ok {
			notCreated[creationID] = map[string]interface{}{"type": "invalidProperties"}
			continue
		}
		c := vcardContact{UID: argString(props, "uid")}
		if c.UID == "" {
			c.UID = uuid.New().String()
		}
		applyJSContactPatch(&c, props)
		if c.FullName == "" {
			c.FullName = deriveFullName(c)
		}
		if c.FullName == "" {
			notCreated[creationID] = map[string]interface{}{"type": "invalidProperties", "description": "name is required"}
			continue
		}
		if err := s.cardStore.SaveContact(user, jmapDefaultAddressBookID, &carddav.Contact{UID: c.UID}, buildVCardContact(c)); err != nil {
			notCreated[creationID] = map[string]interface{}{"type": "serverFail", "description": err.Error()}
			continue
		}
		created[creationID] = map[string]interface{}{"id": c.UID}
		if createdIDs != nil {
			createdIDs[creationID] = c.UID
		}
	}

	for id, raw := range argMap(call.Args, "update") {
		patch, ok := raw.(map[string]interface{})
		if !ok {
			notUpdated[id] = map[string]interface{}{"type": "invalidPatch"}
			continue
		}
		existing, err := s.cardStore.GetContact(user, jmapDefaultAddressBookID, id)
		if err != nil {
			notUpdated[id] = map[string]interface{}{"type": "serverFail", "description": err.Error()}
			continue
		}
		c, ok := parseVCardContact(existing)
		if existing == "" || !ok {
			notUpdated[id] = map[string]interface{}{"type": "notFound"}
			continue
		}
		c.UID = id
		applyJSContactPatch(&c, patch)
		if c.FullName == "" {
			c.FullName = deriveFullName(c)
		}
		if err := s.cardStore.SaveContact(user, jmapDefaultAddressBookID, &carddav.Contact{UID: c.UID}, buildVCardContact(c)); err != nil {
			notUpdated[id] = map[string]interface{}{"type": "serverFail", "description": err.Error()}
			continue
		}
		updated[id] = nil
	}

	for _, raw := range argSlice(call.Args, "destroy") {
		id, ok := raw.(string)
		if !ok {
			continue
		}
		if err := s.cardStore.DeleteContact(user, jmapDefaultAddressBookID, id); err != nil {
			notDestroyed[id] = map[string]interface{}{"type": "serverFail", "description": err.Error()}
			continue
		}
		destroyed = append(destroyed, id)
	}

	return Response{
		Name: "ContactCard/set",
		Args: map[string]interface{}{
			"accountId":    accountID,
			"oldState":     oldState,
			"newState":     s.contactsState(user),
			"created":      created,
			"updated":      updated,
			"destroyed":    destroyed,
			"notCreated":   notCreated,
			"notUpdated":   notUpdated,
			"notDestroyed": notDestroyed,
		},
		ID: call.ID,
	}
}

// =============================================================================
// vCard <-> JSContact conversion (canonical RawData is vCard)
// =============================================================================

// vcardContact is the structured view of a vCard used to translate between the
// canonical vCard and the JMAP JSContact shape. Properties we do not model are
// carried verbatim in passthrough so an edit over JMAP never drops data the
// other protocols depend on.
type vcardContact struct {
	UID         string
	FullName    string
	First       string
	Last        string
	Emails      []string
	Phones      []string
	Org         string
	Title       string
	Note        string
	passthrough []string
}

func parseVCardContact(vcf string) (vcardContact, bool) {
	var c vcardContact
	inCard := false
	for _, line := range unfoldICSLines(vcf) {
		switch {
		case strings.EqualFold(line, "BEGIN:VCARD"):
			inCard = true
			continue
		case strings.EqualFold(line, "END:VCARD"):
			inCard = false
			continue
		}
		if !inCard {
			continue
		}
		name, _, value := splitICSLine(line)
		switch name {
		case "UID":
			c.UID = value
		case "FN":
			c.FullName = icsUnescape(value)
		case "N":
			parts := strings.Split(value, ";")
			if len(parts) > 0 {
				c.Last = icsUnescape(parts[0])
			}
			if len(parts) > 1 {
				c.First = icsUnescape(parts[1])
			}
		case "EMAIL":
			if value != "" {
				c.Emails = append(c.Emails, value)
			}
		case "TEL":
			if value != "" {
				c.Phones = append(c.Phones, value)
			}
		case "ORG":
			c.Org = icsUnescape(strings.TrimSuffix(value, ";"))
		case "TITLE":
			c.Title = icsUnescape(value)
		case "NOTE":
			c.Note = icsUnescape(value)
		case "VERSION", "PRODID", "":
			// regenerated on build
		default:
			c.passthrough = append(c.passthrough, line)
		}
	}
	if c.UID == "" {
		return c, false
	}
	return c, true
}

func buildVCardContact(c vcardContact) string {
	lines := []string{
		"BEGIN:VCARD",
		"VERSION:3.0",
		"UID:" + c.UID,
		foldICSLine("FN:" + icsEscape(c.FullName)),
		foldICSLine("N:" + icsEscape(c.Last) + ";" + icsEscape(c.First) + ";;;"),
	}
	for _, e := range c.Emails {
		if e != "" {
			lines = append(lines, foldICSLine("EMAIL:"+e))
		}
	}
	for _, p := range c.Phones {
		if p != "" {
			lines = append(lines, foldICSLine("TEL:"+p))
		}
	}
	if c.Org != "" {
		lines = append(lines, foldICSLine("ORG:"+icsEscape(c.Org)))
	}
	if c.Title != "" {
		lines = append(lines, foldICSLine("TITLE:"+icsEscape(c.Title)))
	}
	if c.Note != "" {
		lines = append(lines, foldICSLine("NOTE:"+icsEscape(c.Note)))
	}
	lines = append(lines, c.passthrough...)
	lines = append(lines, "END:VCARD")
	return strings.Join(lines, "\r\n") + "\r\n"
}

func contactToJSContact(c vcardContact) map[string]interface{} {
	obj := map[string]interface{}{
		"@type":   "Card",
		"version": "1.0",
		"id":      c.UID,
		"uid":     c.UID,
		"kind":    "individual",
	}
	name := map[string]interface{}{"@type": "Name"}
	if c.FullName != "" {
		name["full"] = c.FullName
	}
	components := []interface{}{}
	if c.First != "" {
		components = append(components, map[string]interface{}{"@type": "NameComponent", "kind": "given", "value": c.First})
	}
	if c.Last != "" {
		components = append(components, map[string]interface{}{"@type": "NameComponent", "kind": "surname", "value": c.Last})
	}
	if len(components) > 0 {
		name["components"] = components
	}
	obj["name"] = name

	if len(c.Emails) > 0 {
		emails := map[string]interface{}{}
		for i, e := range c.Emails {
			emails[emailKey(i)] = map[string]interface{}{"@type": "EmailAddress", "address": e}
		}
		obj["emails"] = emails
	}
	if len(c.Phones) > 0 {
		phones := map[string]interface{}{}
		for i, p := range c.Phones {
			phones[phoneKey(i)] = map[string]interface{}{"@type": "Phone", "number": p}
		}
		obj["phones"] = phones
	}
	if c.Org != "" {
		obj["organizations"] = map[string]interface{}{"o1": map[string]interface{}{"@type": "Organization", "name": c.Org}}
	}
	if c.Title != "" {
		obj["titles"] = map[string]interface{}{"t1": map[string]interface{}{"@type": "Title", "name": c.Title}}
	}
	if c.Note != "" {
		obj["notes"] = map[string]interface{}{"n1": map[string]interface{}{"@type": "Note", "note": c.Note}}
	}
	return obj
}

func applyJSContactPatch(c *vcardContact, patch map[string]interface{}) {
	if v, ok := patch["name"].(map[string]interface{}); ok {
		if full, isStr := v["full"].(string); isStr {
			c.FullName = full
		}
		if comps, isSlice := v["components"].([]interface{}); isSlice {
			for _, raw := range comps {
				comp, isMap := raw.(map[string]interface{})
				if !isMap {
					continue
				}
				kind := argString(comp, "kind")
				value := argString(comp, "value")
				switch kind {
				case "given":
					c.First = value
				case "surname":
					c.Last = value
				}
			}
		}
	}
	if v, ok := patch["emails"].(map[string]interface{}); ok {
		c.Emails = collectByField(v, "address")
	}
	if v, ok := patch["phones"].(map[string]interface{}); ok {
		c.Phones = collectByField(v, "number")
	}
	if v, ok := patch["organizations"].(map[string]interface{}); ok {
		if names := collectByField(v, "name"); len(names) > 0 {
			c.Org = names[0]
		}
	}
	if v, ok := patch["titles"].(map[string]interface{}); ok {
		if names := collectByField(v, "name"); len(names) > 0 {
			c.Title = names[0]
		}
	}
	if v, ok := patch["notes"].(map[string]interface{}); ok {
		if notes := collectByField(v, "note"); len(notes) > 0 {
			c.Note = notes[0]
		}
	}
}

// collectByField pulls a string field out of every object in a JSContact map,
// in deterministic key order.
func collectByField(m map[string]interface{}, field string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var out []string
	for _, k := range keys {
		obj, ok := m[k].(map[string]interface{})
		if !ok {
			continue
		}
		if val, isStr := obj[field].(string); isStr && val != "" {
			out = append(out, val)
		}
	}
	return out
}

func deriveFullName(c vcardContact) string {
	full := strings.TrimSpace(c.First + " " + c.Last)
	if full != "" {
		return full
	}
	if len(c.Emails) > 0 {
		return c.Emails[0]
	}
	return ""
}

func emailKey(i int) string { return "e" + strconv.Itoa(i+1) }
func phoneKey(i int) string { return "p" + strconv.Itoa(i+1) }
