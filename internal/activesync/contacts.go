package activesync

import (
	"strconv"
	"strings"
	"time"

	"github.com/umailserver/umailserver/internal/activesync/wbxml"
)

// ContactItem is one address-book entry projected for an EAS Sync Add/Change of
// a contacts collection. It carries the practical core of a contact — name,
// file-as, organization, the common email/phone slots and the work/home postal
// addresses — which covers the vast majority of real cards. ETag drives the
// enumerate-and-diff cursor (a contacts collection has no change journal).
type ContactItem struct {
	ServerID string
	ETag     string
	UID      string

	FirstName  string
	LastName   string
	MiddleName string
	Suffix     string
	Title      string // name prefix (Mr., Dr.); EAS page-1 Title, vCard N prefix
	FileAs     string // display / file-as name (vCard FN)

	CompanyName string
	Department  string
	JobTitle    string // EAS page-1 JobTitle, vCard TITLE property

	Emails        []string // positional Email1/2/3Address (up to 3)
	MobilePhone   string
	HomePhone     string
	BusinessPhone string

	Business contactAddress
	Home     contactAddress

	Body     string // notes (AirSyncBase Body for 16.x)
	Birthday time.Time
}

// contactAddress is one EAS postal address (a typed vCard ADR component group).
type contactAddress struct {
	Street     string
	City       string
	State      string
	PostalCode string
	Country    string
}

// ContactSource supplies a contacts collection's entries for the Sync command.
// folderID is the canonical (semcore) contacts folder id — the routing prefix
// already stripped. Implementations read the same collaboration store every
// other surface (EWS, CardDAV) reads, so a phone's address book converges with
// them on one source.
type ContactSource interface {
	// ListItems returns the contacts folder's current entries.
	ListItems(email, folderID string) ([]ContactItem, error)
}

// ContactItemFromVCard projects a canonical vCard payload (the collab store's
// RawData) into a ContactItem. serverID is the stable EAS item id (the vCard
// UID, which the canonical store keys on) and etag the collab ETag. The EAS
// surface owns this projection rather than sharing one with EWS/CardDAV — each
// surface maps the canonical card into its own wire shape. vCard reuses the same
// RFC line-folding and "NAME;PARAM:VALUE" syntax as iCalendar, so the shared
// unfold/parse helpers apply.
func ContactItemFromVCard(serverID, etag, raw string) ContactItem {
	item := ContactItem{ServerID: serverID, ETag: etag}
	vcard := sectionBody(raw, "VCARD")
	if vcard == "" {
		return item
	}
	for _, line := range unfoldICal(vcard) {
		name, params, value := parseICalLine(line)
		switch name {
		case "UID":
			item.UID = unescapeICalText(value)
		case "FN":
			item.FileAs = unescapeICalText(value)
		case "N":
			// Family;Given;Additional;Prefix;Suffix
			c := splitVCardValue(value)
			item.LastName = vcardField(c, 0)
			item.FirstName = vcardField(c, 1)
			item.MiddleName = vcardField(c, 2)
			item.Title = vcardField(c, 3)
			item.Suffix = vcardField(c, 4)
		case "ORG":
			// Company;Department
			c := splitVCardValue(value)
			item.CompanyName = vcardField(c, 0)
			item.Department = vcardField(c, 1)
		case "TITLE":
			item.JobTitle = unescapeICalText(value)
		case "EMAIL":
			if len(item.Emails) < 3 && value != "" {
				item.Emails = append(item.Emails, unescapeICalText(value))
			}
		case "TEL":
			switch telType(params) {
			case "CELL":
				item.MobilePhone = unescapeICalText(value)
			case "HOME":
				item.HomePhone = unescapeICalText(value)
			default:
				item.BusinessPhone = unescapeICalText(value)
			}
		case "ADR":
			addr := vcardAddress(value)
			if adrType(params) == "HOME" {
				item.Home = addr
			} else {
				item.Business = addr
			}
		case "NOTE":
			item.Body = unescapeICalText(value)
		case "BDAY":
			if t, ok := parseVCardDate(value); ok {
				item.Birthday = t
			}
		}
	}
	return item
}

// contactAppData projects a ContactItem into its EAS ApplicationData elements:
// the Contacts-class fields (code page 1) plus the AirSyncBase Body (code page
// 17), which carries the notes for 16.x clients (the page-1 Body token is 2.5
// legacy). Only populated fields are emitted, mirroring a real device.
func contactAppData(it ContactItem) []*wbxml.Element {
	con := func(name, text string) *wbxml.Element {
		return &wbxml.Element{Page: wbxml.PageContacts, Name: name, Text: text}
	}
	var els []*wbxml.Element
	add := func(name, text string) {
		if text != "" {
			els = append(els, con(name, text))
		}
	}
	add("FirstName", it.FirstName)
	add("MiddleName", it.MiddleName)
	add("LastName", it.LastName)
	add("Suffix", it.Suffix)
	add("Title", it.Title)
	add("FileAs", it.FileAs)
	add("CompanyName", it.CompanyName)
	add("Department", it.Department)
	add("JobTitle", it.JobTitle)
	for i, e := range it.Emails {
		switch i {
		case 0:
			add("Email1Address", e)
		case 1:
			add("Email2Address", e)
		case 2:
			add("Email3Address", e)
		}
	}
	add("MobilePhoneNumber", it.MobilePhone)
	add("HomePhoneNumber", it.HomePhone)
	add("BusinessPhoneNumber", it.BusinessPhone)
	add("BusinessAddressStreet", it.Business.Street)
	add("BusinessAddressCity", it.Business.City)
	add("BusinessAddressState", it.Business.State)
	add("BusinessAddressPostalCode", it.Business.PostalCode)
	add("BusinessAddressCountry", it.Business.Country)
	add("HomeAddressStreet", it.Home.Street)
	add("HomeAddressCity", it.Home.City)
	add("HomeAddressState", it.Home.State)
	add("HomeAddressPostalCode", it.Home.PostalCode)
	add("HomeAddressCountry", it.Home.Country)
	if !it.Birthday.IsZero() {
		add("Birthday", it.Birthday.UTC().Format(compactDateTime))
	}
	els = append(els, &wbxml.Element{Page: wbxml.PageAirSyncBase, Name: "Body", Children: []*wbxml.Element{
		{Page: wbxml.PageAirSyncBase, Name: "Type", Text: "1"},
		{Page: wbxml.PageAirSyncBase, Name: "EstimatedDataSize", Text: strconv.Itoa(len(it.Body))},
		{Page: wbxml.PageAirSyncBase, Name: "Truncated", Text: "0"},
		{Page: wbxml.PageAirSyncBase, Name: "Data", Text: it.Body},
	}})
	return els
}

// contactItemFromAppData parses a client's contact ApplicationData (an EAS Add
// or Change body) into a ContactItem. It is the inverse of contactAppData: the
// fields come from the Contacts page (1) and the notes from AirSyncBase (page
// 17), the shape a 16.x client sends. ServerID/ETag are not set (the caller
// assigns them).
func contactItemFromAppData(app *wbxml.Element) ContactItem {
	it := ContactItem{}
	if app == nil {
		return it
	}
	get := func(name string) string {
		if e := app.Sub(name); e != nil {
			return e.Text
		}
		return ""
	}
	it.UID = get("UID")
	it.FirstName = get("FirstName")
	it.MiddleName = get("MiddleName")
	it.LastName = get("LastName")
	it.Suffix = get("Suffix")
	it.Title = get("Title")
	it.FileAs = get("FileAs")
	it.CompanyName = get("CompanyName")
	it.Department = get("Department")
	it.JobTitle = get("JobTitle")
	for _, name := range []string{"Email1Address", "Email2Address", "Email3Address"} {
		if v := get(name); v != "" {
			it.Emails = append(it.Emails, v)
		}
	}
	it.MobilePhone = get("MobilePhoneNumber")
	it.HomePhone = get("HomePhoneNumber")
	it.BusinessPhone = get("BusinessPhoneNumber")
	it.Business = contactAddress{
		Street:     get("BusinessAddressStreet"),
		City:       get("BusinessAddressCity"),
		State:      get("BusinessAddressState"),
		PostalCode: get("BusinessAddressPostalCode"),
		Country:    get("BusinessAddressCountry"),
	}
	it.Home = contactAddress{
		Street:     get("HomeAddressStreet"),
		City:       get("HomeAddressCity"),
		State:      get("HomeAddressState"),
		PostalCode: get("HomeAddressPostalCode"),
		Country:    get("HomeAddressCountry"),
	}
	it.Birthday = parseCompactTime(get("Birthday"))
	if body := app.Sub("Body"); body != nil {
		if d := body.Sub("Data"); d != nil {
			it.Body = d.Text
		}
	}
	return it
}

// BuildVCard renders a ContactItem as a canonical RFC 6350 vCard. The EAS surface
// owns this builder; the payload is stored verbatim and read back by EWS/CardDAV,
// so it carries the cross-surface UID and the contact's core fields. Typed
// TEL/ADR/EMAIL components mirror the EAS slots they were projected from.
func BuildVCard(it ContactItem) string {
	var b strings.Builder
	b.WriteString("BEGIN:VCARD\r\nVERSION:3.0\r\nPRODID:-//uMailServer//ActiveSync//EN\r\n")
	b.WriteString("UID:" + it.UID + "\r\n")
	b.WriteString("N:" + escapeICalText(it.LastName) + ";" + escapeICalText(it.FirstName) + ";" +
		escapeICalText(it.MiddleName) + ";" + escapeICalText(it.Title) + ";" + escapeICalText(it.Suffix) + "\r\n")
	fn := it.FileAs
	if fn == "" {
		fn = strings.TrimSpace(it.FirstName + " " + it.LastName)
	}
	b.WriteString("FN:" + escapeICalText(fn) + "\r\n")
	if it.CompanyName != "" || it.Department != "" {
		b.WriteString("ORG:" + escapeICalText(it.CompanyName) + ";" + escapeICalText(it.Department) + "\r\n")
	}
	if it.JobTitle != "" {
		b.WriteString("TITLE:" + escapeICalText(it.JobTitle) + "\r\n")
	}
	for _, e := range it.Emails {
		if e != "" {
			b.WriteString("EMAIL;TYPE=INTERNET:" + escapeICalText(e) + "\r\n")
		}
	}
	if it.MobilePhone != "" {
		b.WriteString("TEL;TYPE=CELL:" + escapeICalText(it.MobilePhone) + "\r\n")
	}
	if it.HomePhone != "" {
		b.WriteString("TEL;TYPE=HOME:" + escapeICalText(it.HomePhone) + "\r\n")
	}
	if it.BusinessPhone != "" {
		b.WriteString("TEL;TYPE=WORK:" + escapeICalText(it.BusinessPhone) + "\r\n")
	}
	if it.Business != (contactAddress{}) {
		b.WriteString("ADR;TYPE=WORK:" + buildVCardADR(it.Business) + "\r\n")
	}
	if it.Home != (contactAddress{}) {
		b.WriteString("ADR;TYPE=HOME:" + buildVCardADR(it.Home) + "\r\n")
	}
	if it.Body != "" {
		b.WriteString("NOTE:" + escapeICalText(it.Body) + "\r\n")
	}
	if !it.Birthday.IsZero() {
		b.WriteString("BDAY:" + it.Birthday.UTC().Format("2006-01-02") + "\r\n")
	}
	b.WriteString("END:VCARD\r\n")
	return b.String()
}

// contactOwnedProps is the set of vCard properties BuildVCard emits — the only
// ones the EAS contact projection can represent. MergeVCard replaces exactly
// these on a client edit and preserves everything else (PHOTO, NICKNAME, IMPP,
// CATEGORIES, X-*). Keep it in lockstep with BuildVCard. Note that EMAIL/TEL/ADR
// are owned by name, so a card's extra phones or emails beyond the modeled
// mobile/home/work + the two addresses are not yet preserved across an edit —
// full multi-value (Supported) ghosting is deferred.
var contactOwnedProps = map[string]bool{
	"VERSION": true, "PRODID": true, "UID": true, "N": true, "FN": true,
	"ORG": true, "TITLE": true, "EMAIL": true, "TEL": true, "ADR": true,
	"NOTE": true, "BDAY": true,
}

// MergeVCard rebuilds the card from the edited item but preserves every vCard
// property the projection does not model, so a phone edit that touches only
// modeled fields does not erase the canonical card's photo, categories, or
// other extended properties. Falls back to a fresh build with no existing card.
func MergeVCard(existing string, it ContactItem) string {
	rebuilt := BuildVCard(it)
	if strings.TrimSpace(existing) == "" {
		return rebuilt
	}
	return mergeRFCSection(existing, rebuilt, "VCARD", contactOwnedProps)
}

// buildVCardADR renders an address as a vCard ADR value: POBox;Ext;Street;City;
// Region;PostalCode;Country (the leading PO-box and extended fields are unused).
func buildVCardADR(a contactAddress) string {
	return ";;" + escapeICalText(a.Street) + ";" + escapeICalText(a.City) + ";" +
		escapeICalText(a.State) + ";" + escapeICalText(a.PostalCode) + ";" + escapeICalText(a.Country)
}

// vcardAddress parses a vCard ADR value (POBox;Ext;Street;City;Region;PostalCode;
// Country) into the EAS address fields, folding the PO-box and extended-address
// components into the street so no entered text is lost.
func vcardAddress(value string) contactAddress {
	c := splitVCardValue(value)
	street := strings.TrimSpace(strings.Join([]string{vcardField(c, 0), vcardField(c, 1), vcardField(c, 2)}, " "))
	return contactAddress{
		Street:     street,
		City:       vcardField(c, 3),
		State:      vcardField(c, 4),
		PostalCode: vcardField(c, 5),
		Country:    vcardField(c, 6),
	}
}

// vcardField returns the i-th structured component, unescaped, or "" when absent.
func vcardField(components []string, i int) string {
	if i < 0 || i >= len(components) {
		return ""
	}
	return strings.TrimSpace(unescapeICalText(components[i]))
}

// splitVCardValue splits a structured vCard value on unescaped ';' separators,
// leaving '\;' escapes intact for the per-component unescape.
func splitVCardValue(s string) []string {
	var parts []string
	var cur strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			cur.WriteByte(s[i])
			cur.WriteByte(s[i+1])
			i++
			continue
		}
		if s[i] == ';' {
			parts = append(parts, cur.String())
			cur.Reset()
			continue
		}
		cur.WriteByte(s[i])
	}
	parts = append(parts, cur.String())
	return parts
}

// telType returns the most specific phone type (CELL/HOME/WORK) from a TEL
// parameter set, defaulting to WORK (business) when no recognized type is given.
func telType(params map[string]string) string {
	t := strings.ToUpper(params["TYPE"])
	switch {
	case strings.Contains(t, "CELL"), strings.Contains(t, "MOBILE"):
		return "CELL"
	case strings.Contains(t, "HOME"):
		return "HOME"
	default:
		return "WORK"
	}
}

// adrType returns HOME when an ADR parameter set marks a home address, else WORK.
func adrType(params map[string]string) string {
	if strings.Contains(strings.ToUpper(params["TYPE"]), "HOME") {
		return "HOME"
	}
	return "WORK"
}

// parseVCardDate parses a vCard BDAY date (ISO "2006-01-02" or basic "20060102")
// to a UTC instant at midnight, or false when empty or malformed.
func parseVCardDate(value string) (time.Time, bool) {
	value = strings.TrimSpace(value)
	for _, layout := range []string{"2006-01-02", "20060102"} {
		if t, err := time.Parse(layout, value); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}
