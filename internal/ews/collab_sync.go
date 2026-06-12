// Package ews implements the Exchange Web Services (EWS) SOAP interface for
// uMailServer. This file projects canonical collaboration records (calendar
// events, contacts, tasks) from the semcore collaboration store into the typed
// EWS item elements (<t:CalendarItem>, <t:Contact>, <t:Task>) that FindItem,
// SyncFolderItems, and GetItem must emit.
//
// Bare <t:Message> is rejected by Outlook for collaboration folders: the client
// instantiates the item by its declared element type and silently drops a
// calendar/contact/task that arrives as a generic message. Each struct below is
// therefore ordered to the EWS ItemType base sequence (ItemId, ParentFolderId,
// ItemClass, Subject, …) followed by the type extension's own sequence, exactly
// as defined in the EWS Types.xsd. encoding/xml emits struct fields verbatim, so
// the field declaration order is load-bearing, not cosmetic.
package ews

import (
	"encoding/xml"
	"strconv"
	"strings"

	"github.com/umailserver/umailserver/internal/semcore"
)

// ---------------------------------------------------------------------------
// Typed response structs (schema-ordered)
// ---------------------------------------------------------------------------

// CalendarItemResponse is a calendar event projected for FindItem/SyncFolderItems.
// Field order follows ItemType (ItemId, ParentFolderId, ItemClass, Subject,
// Categories) then the CalendarItemType extension (UID, Start, End,
// OriginalStart, IsAllDayEvent, LegacyFreeBusyStatus, Location). Start/End MUST
// precede IsAllDayEvent and UID is emitted early, per Types.xsd:4914.
type CalendarItemResponse struct {
	XMLName        xml.Name               `xml:"http://schemas.microsoft.com/exchange/services/2006/types CalendarItem"`
	ItemID         ItemIdType             `xml:"http://schemas.microsoft.com/exchange/services/2006/types ItemId"`
	ParentFolderID FolderIdComponents     `xml:"http://schemas.microsoft.com/exchange/services/2006/types ParentFolderId"`
	ItemClass      string                 `xml:"http://schemas.microsoft.com/exchange/services/2006/types ItemClass"`
	Subject        string                 `xml:"http://schemas.microsoft.com/exchange/services/2006/types Subject,omitempty"`
	Categories     *MessageCategoriesType `xml:"http://schemas.microsoft.com/exchange/services/2006/types Categories,omitempty"`
	UID            string                 `xml:"http://schemas.microsoft.com/exchange/services/2006/types UID,omitempty"`
	Start          string                 `xml:"http://schemas.microsoft.com/exchange/services/2006/types Start,omitempty"`
	End            string                 `xml:"http://schemas.microsoft.com/exchange/services/2006/types End,omitempty"`
	IsAllDayEvent  bool                   `xml:"http://schemas.microsoft.com/exchange/services/2006/types IsAllDayEvent"`
	Location       string                 `xml:"http://schemas.microsoft.com/exchange/services/2006/types Location,omitempty"`
}

// ContactItemResponse is a contact projected for FindItem/SyncFolderItems.
// Field order follows ItemType (ItemId, ParentFolderId, ItemClass, Subject,
// Categories) then the ContactItemType extension (FileAs, DisplayName,
// GivenName, CompleteName, CompanyName, EmailAddresses, Surname) per Types.xsd.
// CompleteName sits between GivenName and CompanyName in the schema sequence;
// Outlook for Mac reads the People card name from CompleteName, so it is
// load-bearing, not redundant with the flat GivenName/Surname elements.
type ContactItemResponse struct {
	XMLName        xml.Name               `xml:"http://schemas.microsoft.com/exchange/services/2006/types Contact"`
	ItemID         ItemIdType             `xml:"http://schemas.microsoft.com/exchange/services/2006/types ItemId"`
	ParentFolderID FolderIdComponents     `xml:"http://schemas.microsoft.com/exchange/services/2006/types ParentFolderId"`
	ItemClass      string                 `xml:"http://schemas.microsoft.com/exchange/services/2006/types ItemClass"`
	Subject        string                 `xml:"http://schemas.microsoft.com/exchange/services/2006/types Subject,omitempty"`
	Categories     *MessageCategoriesType `xml:"http://schemas.microsoft.com/exchange/services/2006/types Categories,omitempty"`
	FileAs         string                 `xml:"http://schemas.microsoft.com/exchange/services/2006/types FileAs,omitempty"`
	DisplayName    string                 `xml:"http://schemas.microsoft.com/exchange/services/2006/types DisplayName,omitempty"`
	GivenName      string                 `xml:"http://schemas.microsoft.com/exchange/services/2006/types GivenName,omitempty"`
	CompleteName   *CompleteNameType      `xml:"http://schemas.microsoft.com/exchange/services/2006/types CompleteName,omitempty"`
	CompanyName    string                 `xml:"http://schemas.microsoft.com/exchange/services/2006/types CompanyName,omitempty"`
	EmailAddresses *EmailAddressesType    `xml:"http://schemas.microsoft.com/exchange/services/2006/types EmailAddresses,omitempty"`
	Surname        string                 `xml:"http://schemas.microsoft.com/exchange/services/2006/types Surname,omitempty"`
}

// TaskItemResponse is a task projected for FindItem/SyncFolderItems. Field order
// follows ItemType (ItemId, ParentFolderId, ItemClass, Subject, Categories) then
// the TaskType extension (DueDate, IsComplete, Owner, PercentComplete,
// StartDate, Status) per Types.xsd:4074. DueDate precedes IsComplete;
// PercentComplete/StartDate/Status follow in their schema positions.
type TaskItemResponse struct {
	XMLName         xml.Name               `xml:"http://schemas.microsoft.com/exchange/services/2006/types Task"`
	ItemID          ItemIdType             `xml:"http://schemas.microsoft.com/exchange/services/2006/types ItemId"`
	ParentFolderID  FolderIdComponents     `xml:"http://schemas.microsoft.com/exchange/services/2006/types ParentFolderId"`
	ItemClass       string                 `xml:"http://schemas.microsoft.com/exchange/services/2006/types ItemClass"`
	Subject         string                 `xml:"http://schemas.microsoft.com/exchange/services/2006/types Subject,omitempty"`
	Categories      *MessageCategoriesType `xml:"http://schemas.microsoft.com/exchange/services/2006/types Categories,omitempty"`
	DueDate         string                 `xml:"http://schemas.microsoft.com/exchange/services/2006/types DueDate,omitempty"`
	IsComplete      bool                   `xml:"http://schemas.microsoft.com/exchange/services/2006/types IsComplete"`
	PercentComplete int                    `xml:"http://schemas.microsoft.com/exchange/services/2006/types PercentComplete"`
	StartDate       string                 `xml:"http://schemas.microsoft.com/exchange/services/2006/types StartDate,omitempty"`
	Status          string                 `xml:"http://schemas.microsoft.com/exchange/services/2006/types Status,omitempty"`
}

// ---------------------------------------------------------------------------
// Projectors: canonical record -> typed response
// ---------------------------------------------------------------------------

// rawCalendarToResponse projects a stored calendar item's canonical VEVENT into
// a schema-ordered CalendarItemResponse. Reads are scoped to the VEVENT so a
// timezone-anchored event's VTIMEZONE (which carries its own DTSTART) cannot
// leak into Start/End.
func rawCalendarToResponse(rec semcore.StoredCalendarItemIdentity, folderID semcore.FolderId) CalendarItemResponse {
	ev := icalComponent(rec.RawData, "VEVENT")

	subject := extractDirProp(ev, "SUMMARY")
	if subject == "" {
		subject = rec.IcalUID
	}
	uid := extractDirProp(ev, "UID")
	if uid == "" {
		uid = rec.IcalUID
	}

	resp := CalendarItemResponse{
		ItemID:         ItemIdType{ID: rec.ID.String(), CK: rec.ChangeKey.String()},
		ParentFolderID: FolderIdComponents{ID: folderID.String()},
		ItemClass:      "IPM.Appointment",
		Subject:        subject,
		Categories:     categoriesResponse(parseICalCategories(rec.RawData)),
		UID:            uid,
		Location:       extractDirProp(ev, "LOCATION"),
	}
	if t := icalEventInstant(ev, "DTSTART"); !t.IsZero() {
		resp.Start = FormatEWSDateTime(t)
	}
	if t := icalEventInstant(ev, "DTEND"); !t.IsZero() {
		resp.End = FormatEWSDateTime(t)
	}
	// An all-day event uses a DATE (not DATE-TIME) DTSTART, surfaced as
	// VALUE=DATE on the property line.
	if strings.EqualFold(extractDirPropParam(ev, "DTSTART", "VALUE"), "DATE") {
		resp.IsAllDayEvent = true
	}
	return resp
}

// rawContactToResponse projects a stored contact's canonical vCard into a
// schema-ordered ContactItemResponse. vCard FN -> DisplayName/FileAs, the N
// "Family;Given;Additional;Prefix;Suffix" structure -> Surname/GivenName, ORG
// -> CompanyName, EMAIL -> EmailAddress1.
func rawContactToResponse(rec semcore.StoredContactIdentity, folderID semcore.FolderId) ContactItemResponse {
	fn := extractDirProp(rec.RawData, "FN")

	var surname, given string
	if n := extractDirProp(rec.RawData, "N"); n != "" {
		parts := strings.Split(n, ";")
		if len(parts) > 0 {
			surname = strings.TrimSpace(parts[0])
		}
		if len(parts) > 1 {
			given = strings.TrimSpace(parts[1])
		}
	}

	subject := fn
	if subject == "" {
		subject = strings.TrimSpace(given + " " + surname)
	}
	if subject == "" {
		subject = rec.IcalUID
	}

	fullName := fn
	if fullName == "" {
		fullName = strings.TrimSpace(given + " " + surname)
	}

	resp := ContactItemResponse{
		ItemID:         ItemIdType{ID: rec.ID.String(), CK: rec.ChangeKey.String()},
		ParentFolderID: FolderIdComponents{ID: folderID.String()},
		ItemClass:      "IPM.Contact",
		Subject:        subject,
		Categories:     categoriesResponse(parseICalCategories(rec.RawData)),
		FileAs:         fn,
		DisplayName:    fn,
		GivenName:      given,
		CompleteName: &CompleteNameType{
			FirstName: given,
			LastName:  surname,
			FullName:  fullName,
		},
		CompanyName: vcardOrg(extractDirProp(rec.RawData, "ORG")),
		Surname:     surname,
	}
	if email := extractDirProp(rec.RawData, "EMAIL"); email != "" {
		resp.EmailAddresses = &EmailAddressesType{
			Entry: []EmailAddressEntry{{
				Key:         "EmailAddress1",
				Name:        fullName,
				RoutingType: "SMTP",
				MailboxType: "Contact",
				Value:       strings.TrimSpace(email),
			}},
		}
	}
	return resp
}

// vcardOrg extracts the organization name from a vCard ORG value, which is a
// ';'-separated list "Company;Unit;…"; only the first component is the company.
func vcardOrg(org string) string {
	if org == "" {
		return ""
	}
	if semi := strings.IndexByte(org, ';'); semi >= 0 {
		return strings.TrimSpace(org[:semi])
	}
	return strings.TrimSpace(org)
}

// rawTaskToResponse projects a stored task's canonical VTODO into a
// schema-ordered TaskItemResponse. VTODO SUMMARY -> Subject, DUE -> DueDate,
// DTSTART -> StartDate, STATUS -> Status (NEEDS-ACTION->NotStarted,
// IN-PROCESS->InProgress, COMPLETED->Completed), PERCENT-COMPLETE ->
// PercentComplete. IsComplete is set when the task is COMPLETED.
func rawTaskToResponse(rec semcore.StoredTaskIdentity, folderID semcore.FolderId) TaskItemResponse {
	todo := icalComponent(rec.RawData, "VTODO")

	subject := extractDirProp(todo, "SUMMARY")
	if subject == "" {
		subject = rec.IcalUID
	}

	status := icalStatusToEWS(extractDirProp(todo, "STATUS"))

	percent := 0
	if pc := extractDirProp(todo, "PERCENT-COMPLETE"); pc != "" {
		if v, err := strconv.Atoi(strings.TrimSpace(pc)); err == nil {
			percent = v
		}
	}

	resp := TaskItemResponse{
		ItemID:          ItemIdType{ID: rec.ID.String(), CK: rec.ChangeKey.String()},
		ParentFolderID:  FolderIdComponents{ID: folderID.String()},
		ItemClass:       "IPM.Task",
		Subject:         subject,
		Categories:      categoriesResponse(parseICalCategories(rec.RawData)),
		Status:          status,
		PercentComplete: percent,
		IsComplete:      status == "Completed",
	}
	if due := icalToEWSDateTime(extractDirProp(todo, "DUE")); due != "" {
		resp.DueDate = due
	}
	if start := icalToEWSDateTime(extractDirProp(todo, "DTSTART")); start != "" {
		resp.StartDate = start
	}
	return resp
}
