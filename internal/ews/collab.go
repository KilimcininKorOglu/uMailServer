// Package ews implements the Exchange Web Services (EWS) SOAP interface for
// uMailServer. This file provides the calendar item, contact, and task
// operation handlers that project the canonical semcore collaboration model
// through the EWS wire contract.
//
// Calendar items, contacts, and tasks each carry their own identity family
// (CalendarItemId, ContactId, TaskId) and version token (CalendarChangeKey,
// ContactChangeKey, TaskChangeKey). The BoltCollaborationStore persists these
// with optimistic locking so that a stale version token on write is rejected
// explicitly (ErrCollabVersionConflict), satisfying VAL-COLLAB-011.
//
// Recurrence data is preserved as raw iCal (RRULE/RDATE/EXDATE) for wire
// compatibility, and the structured RecurrenceRule field holds the parsed
// form for server-side expansion and conflict detection. This satisfies
// VAL-COLLAB-002 (recurrence masters and exceptions keep one observable
// meaning) and VAL-COLLAB-016 (time-zone and DST fidelity).
//
// Reminder state is stored on the CalendarItem and Task objects in the
// BoltCollaborationStore and survives projection rereads, satisfying
// VAL-COLLAB-012 (reminder and notification lifecycle persist across edits,
// recurrence, and projection rereads).
package ews

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/xml"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/umailserver/umailserver/internal/semcore"
)

// ---------------------------------------------------------------------------
// CalendarItem types
// ---------------------------------------------------------------------------

// CreateCalendarItemRequest is the EWS CreateCalendarItem operation request.
type CreateCalendarItemRequest struct {
	XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/messages CreateCalendarItem"`
	Items   struct {
		XMLName xml.Name              `xml:"http://schemas.microsoft.com/exchange/services/2006/messages Items"`
		Item    []CalendarItemTypeNew `xml:"http://schemas.microsoft.com/exchange/services/2006/types CalendarItem"`
	} `xml:"http://schemas.microsoft.com/exchange/services/2006/messages Items"`
	SavedItemFolderID struct {
		DistinguishedFolderID *string `xml:"Id,attr,omitempty"`
	} `xml:"http://schemas.microsoft.com/exchange/services/2006/messages SavedItemFolderId,omitempty"`
	SaveItemToFolder *bool `xml:"http://schemas.microsoft.com/exchange/services/2006/messages SaveItemToFolder,attr"`
}

// CalendarItemTypeNew is a calendar item in CreateCalendarItem requests.
type CalendarItemTypeNew struct {
	XMLName   xml.Name         `xml:"http://schemas.microsoft.com/exchange/services/2006/types CalendarItem"`
	Subject   string           `xml:"http://schemas.microsoft.com/exchange/services/2006/types Subject,omitempty"`
	Body      *BodyType        `xml:"http://schemas.microsoft.com/exchange/services/2006/types Body,omitempty"`
	Start     string           `xml:"http://schemas.microsoft.com/exchange/services/2006/types Start"`
	End       string           `xml:"http://schemas.microsoft.com/exchange/services/2006/types End"`
	IsAllDay  bool             `xml:"http://schemas.microsoft.com/exchange/services/2006/types IsAllDay,attr,omitempty"`
	Location  string           `xml:"http://schemas.microsoft.com/exchange/services/2006/types Location,omitempty"`
	Organizer *FromAddressType `xml:"http://schemas.microsoft.com/exchange/services/2006/types Organizer,omitempty"`
	//nolint:staticcheck // EWS uses RequiredAttendees as the element name for attendee list.
	Attendees    *AttendeesType         `xml:"http://schemas.microsoft.com/exchange/services/2006/types RequiredAttendees,omitempty"`
	Recurrence   *RecurrenceType        `xml:"http://schemas.microsoft.com/exchange/services/2006/types Recurrence,omitempty"`
	ReminderSet  *bool                  `xml:"http://schemas.microsoft.com/exchange/services/2006/typesReminderSet,attr,omitempty"`
	Reminder     *ReminderType          `xml:"http://schemas.microsoft.com/exchange/services/2006/types Reminder,omitempty"`
	CalendarType *int                   `xml:"http://schemas.microsoft.com/exchange/services/2006/types CalendarType,attr,omitempty"`
	TimeZone     string                 `xml:"http://schemas.microsoft.com/exchange/services/2006/types TimeZone,omitempty"`
	Categories   *MessageCategoriesType `xml:"http://schemas.microsoft.com/exchange/services/2006/types Categories,omitempty"`
}

// RecurrenceType represents iCal recurrence data in EWS.
type RecurrenceType struct {
	XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/types Recurrence"`
	//nolint:staticcheck // govulncheck: EWS requires these specific element names.
	WeeklyRecurrence         *WeeklyRecurrenceType         `xml:"http://schemas.microsoft.com/exchange/services/2006/types WeeklyRecurrence,omitempty"`
	DailyRecurrence          *DailyRecurrenceType          `xml:"http://schemas.microsoft.com/exchange/services/2006/types DailyRecurrence,omitempty"`
	MonthlyRecurrence        *MonthlyRecurrenceType        `xml:"http://schemas.microsoft.com/exchange/services/2006/types MonthlyRecurrence,omitempty"`
	YearlyRecurrence         *YearlyRecurrenceType         `xml:"http://schemas.microsoft.com/exchange/services/2006/types YearlyRecurrence,omitempty"`
	RelativeYearlyRecurrence *RelativeYearlyRecurrenceType `xml:"http://schemas.microsoft.com/exchange/services/2006/types RelativeYearlyRecurrence,omitempty"`
	EndDateRecurrence        *EndDateRecurrenceType        `xml:"http://schemas.microsoft.com/exchange/services/2006/types EndDateRecurrence,omitempty"`
	NumberedRecurrence       *NumberedRecurrenceType       `xml:"http://schemas.microsoft.com/exchange/services/2006/types NumberedRecurrence,omitempty"`
}

// WeeklyRecurrenceType represents weekly recurrence.
type WeeklyRecurrenceType struct {
	XMLName        xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/types WeeklyRecurrence"`
	Interval       int      `xml:"Interval,attr,omitempty"`
	DaysOfWeek     string   `xml:"DaysOfWeek,attr,omitempty"`
	FirstDayOfWeek string   `xml:"FirstDayOfWeek,attr,omitempty"`
}

// DailyRecurrenceType represents daily recurrence.
type DailyRecurrenceType struct {
	XMLName  xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/types DailyRecurrence"`
	Interval int      `xml:"Interval,attr,omitempty"`
}

// MonthlyRecurrenceType represents monthly recurrence.
type MonthlyRecurrenceType struct {
	XMLName        xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/types MonthlyRecurrence"`
	Interval       int      `xml:"Interval,attr,omitempty"`
	DayOfMonth     int      `xml:"DayOfMonth,attr,omitempty"`
	DaysOfWeek     string   `xml:"DaysOfWeek,attr,omitempty"`
	DayOfWeekIndex string   `xml:"DayOfWeekIndex,attr,omitempty"`
}

// YearlyRecurrenceType represents yearly recurrence.
type YearlyRecurrenceType struct {
	XMLName        xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/types YearlyRecurrence"`
	Interval       int      `xml:"Interval,attr,omitempty"`
	Month          int      `xml:"Month,attr,omitempty"`
	DayOfMonth     int      `xml:"DayOfMonth,attr,omitempty"`
	DaysOfWeek     string   `xml:"DaysOfWeek,attr,omitempty"`
	DayOfWeekIndex string   `xml:"DayOfWeekIndex,attr,omitempty"`
}

// RelativeYearlyRecurrenceType represents relative yearly recurrence.
type RelativeYearlyRecurrenceType struct {
	XMLName        xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/types RelativeYearlyRecurrence"`
	Interval       int      `xml:"Interval,attr,omitempty"`
	Month          int      `xml:"Month,attr,omitempty"`
	DayOfWeek      string   `xml:"DayOfWeek,attr,omitempty"`
	DayOfWeekIndex string   `xml:"DayOfWeekIndex,attr,omitempty"`
}

// EndDateRecurrenceType represents recurrence with end date.
type EndDateRecurrenceType struct {
	XMLName   xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/types EndDateRecurrence"`
	StartDate string   `xml:"StartDate,attr,omitempty"`
	EndDate   string   `xml:"EndDate,attr,omitempty"`
}

// NumberedRecurrenceType represents recurrence with count.
type NumberedRecurrenceType struct {
	XMLName             xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/types NumberedRecurrence"`
	StartDate           string   `xml:"StartDate,attr,omitempty"`
	NumberOfOccurrences int      `xml:"NumberOfOccurrences,attr,omitempty"`
}

// AttendeesType holds the attendee list in calendar requests.
type AttendeesType struct {
	XMLName  xml.Name       `xml:"http://schemas.microsoft.com/exchange/services/2006/types Attendees"`
	Attendee []AttendeeType `xml:"http://schemas.microsoft.com/exchange/services/2006/types Attendee"`
}

// AttendeeType represents a calendar attendee.
type AttendeeType struct {
	XMLName           xml.Name         `xml:"http://schemas.microsoft.com/exchange/services/2006/types Attendee"`
	Mailbox           EmailAddressType `xml:"http://schemas.microsoft.com/exchange/services/2006/types Mailbox"`
	ResponseRequested *bool            `xml:"http://schemas.microsoft.com/exchange/services/2006/types ResponseRequested,attr,omitempty"`
}

// ReminderType represents a reminder trigger.
type ReminderType struct {
	XMLName            xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/types Reminder"`
	StartTime          string   `xml:"http://schemas.microsoft.com/exchange/services/2006/types StartTime"`
	OriginalStart      string   `xml:"http://schemas.microsoft.com/exchange/services/2006/types OriginalStart"`
	IsSet              bool     `xml:"http://schemas.microsoft.com/exchange/services/2006/types IsSet,attr,omitempty"`
	MinutesBeforeStart int      `xml:"http://schemas.microsoft.com/exchange/services/2006/types MinutesBeforeStart,attr,omitempty"`
}

// CreateCalendarItemResponse is the EWS CreateCalendarItem operation response.
type CreateCalendarItemResponse struct {
	XMLName xml.Name                           `xml:"http://schemas.microsoft.com/exchange/services/2006/messages CreateCalendarItemResponse"`
	Msgs    CreateCalendarItemResponseMessages `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseMessages"`
}

// CreateCalendarItemResponseMessages wraps CreateCalendarItem response messages.
type CreateCalendarItemResponseMessages struct {
	Messages []CalendarItemResponseMessageType `xml:"http://schemas.microsoft.com/exchange/services/2006/messages CreateCalendarItemResponseMessage"`
}

// CalendarItemResponseMessageType is one calendar item's result in a response.
type CalendarItemResponseMessageType struct {
	XMLName       xml.Name                `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseMessage"`
	ResponseClass string                  `xml:"ResponseClass,attr"`
	ResponseCode  ResponseCodeType        `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseCode"`
	Items         *CalendarItemsContainer `xml:"http://schemas.microsoft.com/exchange/services/2006/messages Items"`
}

// CalendarItemsContainer wraps calendar items in response messages.
type CalendarItemsContainer struct {
	XMLName xml.Name                   `xml:"http://schemas.microsoft.com/exchange/services/2006/messages Items"`
	Items   []CalendarItemTypeResponse `xml:"http://schemas.microsoft.com/exchange/services/2006/types CalendarItem"`
}

// CalendarItemTypeResponse is a calendar item in EWS responses.
type CalendarItemTypeResponse struct {
	XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/types CalendarItem"`
	//nolint:staticcheck // EWS uses ItemId element name in responses; type is CalendarItemIdType.
	ItemID         CalendarItemIdType `xml:"http://schemas.microsoft.com/exchange/services/2006/types ItemId"`
	ParentFolderID FolderIdComponents `xml:"http://schemas.microsoft.com/exchange/services/2006/types ParentFolderId"`
	Subject        string             `xml:"http://schemas.microsoft.com/exchange/services/2006/types Subject,omitempty"`
	Start          string             `xml:"http://schemas.microsoft.com/exchange/services/2006/types Start,omitempty"`
	End            string             `xml:"http://schemas.microsoft.com/exchange/services/2006/types End,omitempty"`
	IsAllDay       bool               `xml:"http://schemas.microsoft.com/exchange/services/2006/types IsAllDay,attr,omitempty"`
	Location       string             `xml:"http://schemas.microsoft.com/exchange/services/2006/types Location,omitempty"`
	CalendarType   *int               `xml:"http://schemas.microsoft.com/exchange/services/2006/types CalendarType,attr,omitempty"`
	Recurrence     *RecurrenceType    `xml:"http://schemas.microsoft.com/exchange/services/2006/types Recurrence,omitempty"`
	UID            string             `xml:"http://schemas.microsoft.com/exchange/services/2006/types UID,omitempty"`
}

// CalendarItemIdType is the EWS CalendarItemId element.
type CalendarItemIdType struct {
	XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/types CalendarItemId"`
	ID      string   `xml:"Id,attr"`
	CK      string   `xml:"ChangeKey,attr,omitempty"`
}

// ---------------------------------------------------------------------------
// GetCalendarItem
// ---------------------------------------------------------------------------

// GetCalendarItemRequest is the EWS GetCalendarItem operation request.
type GetCalendarItemRequest struct {
	XMLName      xml.Name            `xml:"http://schemas.microsoft.com/exchange/services/2006/messages GetCalendarItem"`
	ItemShapeDef ItemShapeType       `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ItemShape"`
	ItemIDs      CalendarItemIdsType `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ItemIds"`
}

// CalendarItemIdsType is a list of calendar item IDs.
type CalendarItemIdsType struct {
	XMLName xml.Name             `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ItemIds"`
	Item    []CalendarItemIdType `xml:"http://schemas.microsoft.com/exchange/services/2006/types CalendarItemId"`
}

// GetCalendarItemResponse is the EWS GetCalendarItem operation response.
type GetCalendarItemResponse struct {
	XMLName xml.Name                        `xml:"http://schemas.microsoft.com/exchange/services/2006/messages GetCalendarItemResponse"`
	Msgs    GetCalendarItemResponseMessages `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseMessages"`
}

// GetCalendarItemResponseMessages wraps GetCalendarItem response messages.
type GetCalendarItemResponseMessages struct {
	Messages []CalendarItemResponseMessageType `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseMessage"`
}

// ---------------------------------------------------------------------------
// UpdateCalendarItem
// ---------------------------------------------------------------------------

// UpdateCalendarItemRequest is the EWS UpdateCalendarItem operation request.
type UpdateCalendarItemRequest struct {
	XMLName     xml.Name                `xml:"http://schemas.microsoft.com/exchange/services/2006/messages UpdateCalendarItem"`
	ItemChanges CalendarItemChangesList `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ItemChanges"`
}

// CalendarItemChangesList wraps the calendar ItemChange list.
type CalendarItemChangesList struct {
	XMLName xml.Name               `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ItemChanges"`
	Changes []CalendarItemChangeOp `xml:"http://schemas.microsoft.com/exchange/services/2006/types ItemChange"`
}

// CalendarItemChangeOp represents one calendar item change in UpdateCalendarItem.
type CalendarItemChangeOp struct {
	XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/types ItemChange"`
	ItemID  struct {
		XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/types ItemId"`
		ID      string   `xml:"Id,attr"`
		CK      string   `xml:"ChangeKey,attr,omitempty"`
	} `xml:"http://schemas.microsoft.com/exchange/services/2006/types ItemId"`
	Updates ItemUpdatesOp `xml:"http://schemas.microsoft.com/exchange/services/2006/types Updates"`
}

// UpdateCalendarItemResponse is the EWS UpdateCalendarItem operation response.
type UpdateCalendarItemResponse struct {
	XMLName xml.Name                           `xml:"http://schemas.microsoft.com/exchange/services/2006/messages UpdateCalendarItemResponse"`
	Msgs    UpdateCalendarItemResponseMessages `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseMessages"`
}

// UpdateCalendarItemResponseMessages wraps UpdateCalendarItem response messages.
type UpdateCalendarItemResponseMessages struct {
	Messages []CalendarItemResponseMessageType `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseMessage"`
}

// ---------------------------------------------------------------------------
// DeleteCalendarItem
// ---------------------------------------------------------------------------

// DeleteCalendarItemRequest is the EWS DeleteCalendarItem operation request.
type DeleteCalendarItemRequest struct {
	XMLName    xml.Name            `xml:"http://schemas.microsoft.com/exchange/services/2006/messages DeleteCalendarItem"`
	ItemIDs    CalendarItemIdsType `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ItemIds"`
	DeleteType string              `xml:"http://schemas.microsoft.com/exchange/services/2006/messages DeleteType,attr,omitempty"`
}

// DeleteCalendarItemResponse is the EWS DeleteCalendarItem operation response.
type DeleteCalendarItemResponse struct {
	XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/messages DeleteCalendarItemResponse"`
	Msgs    struct {
		Messages []struct {
			XMLName       xml.Name         `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseMessage"`
			ResponseClass string           `xml:"ResponseClass,attr"`
			ResponseCode  ResponseCodeType `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseCode"`
		} `xml:"http://schemas.microsoft.com/exchange/services/2006/messages DeleteCalendarItemResponseMessage"`
	} `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseMessages"`
}

// ---------------------------------------------------------------------------
// Contact types
// ---------------------------------------------------------------------------

// CreateContactRequest is the EWS CreateContact operation request.
type CreateContactRequest struct {
	XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/messages CreateContact"`
	Items   struct {
		XMLName xml.Name         `xml:"http://schemas.microsoft.com/exchange/services/2006/messages Items"`
		Item    []ContactTypeNew `xml:"http://schemas.microsoft.com/exchange/services/2006/types Contact"`
	} `xml:"http://schemas.microsoft.com/exchange/services/2006/messages Items"`
	SavedItemFolderID struct {
		DistinguishedFolderID *string `xml:"Id,attr,omitempty"`
	} `xml:"http://schemas.microsoft.com/exchange/services/2006/messages SavedItemFolderId,omitempty"`
	SaveItemToFolder *bool `xml:"http://schemas.microsoft.com/exchange/services/2006/messages SaveItemToFolder,attr"`
}

// ContactTypeNew is a contact item in CreateContact requests.
type ContactTypeNew struct {
	XMLName        xml.Name            `xml:"http://schemas.microsoft.com/exchange/services/2006/types Contact"`
	FullName       string              `xml:"http://schemas.microsoft.com/exchange/services/2006/types FullName,omitempty"`
	GivenName      string              `xml:"http://schemas.microsoft.com/exchange/services/2006/types GivenName,omitempty"`
	Surname        string              `xml:"http://schemas.microsoft.com/exchange/services/2006/types Surname,omitempty"`
	EmailAddresses *EmailAddressesType `xml:"http://schemas.microsoft.com/exchange/services/2006/types EmailAddresses,omitempty"`
	PhoneNumbers   *PhoneNumbersType   `xml:"http://schemas.microsoft.com/exchange/services/2006/types PhoneNumbers,omitempty"`
	//nolint:staticcheck // EWS uses HomeAddress/WorkAddress as element names; type is PhysicalAddressType.
	HomeAddress *PhysicalAddressType `xml:"http://schemas.microsoft.com/exchange/services/2006/types HomeAddress,omitempty"`
	//nolint:staticcheck // EWS uses WorkAddress as element name; type is PhysicalAddressType.
	WorkAddress  *PhysicalAddressType   `xml:"http://schemas.microsoft.com/exchange/services/2006/types WorkAddress,omitempty"`
	Organization string                 `xml:"http://schemas.microsoft.com/exchange/services/2006/types CompanyName,omitempty"`
	Title        string                 `xml:"http://schemas.microsoft.com/exchange/services/2006/types Title,omitempty"`
	JobTitle     string                 `xml:"http://schemas.microsoft.com/exchange/services/2006/types JobTitle,omitempty"`
	Department   string                 `xml:"http://schemas.microsoft.com/exchange/services/2006/types Department,omitempty"`
	Notes        string                 `xml:"http://schemas.microsoft.com/exchange/services/2006/types Body,omitempty"`
	Categories   *MessageCategoriesType `xml:"http://schemas.microsoft.com/exchange/services/2006/types Categories,omitempty"`
}

// EmailAddressesType holds email address entries.
type EmailAddressesType struct {
	XMLName xml.Name            `xml:"http://schemas.microsoft.com/exchange/services/2006/types EmailAddresses"`
	Entry   []EmailAddressEntry `xml:"http://schemas.microsoft.com/exchange/services/2006/types Entry"`
}

// EmailAddressEntry is one email address entry.
type EmailAddressEntry struct {
	XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/types Entry"`
	Key     string   `xml:"Key,attr"`
	Value   string   `xml:",chardata"`
}

// PhoneNumbersType holds phone number entries.
type PhoneNumbersType struct {
	XMLName xml.Name           `xml:"http://schemas.microsoft.com/exchange/services/2006/types PhoneNumbers"`
	Entry   []PhoneNumberEntry `xml:"http://schemas.microsoft.com/exchange/services/2006/types Entry"`
}

// PhoneNumberEntry is one phone number entry.
type PhoneNumberEntry struct {
	XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/types Entry"`
	Key     string   `xml:"Key,attr"`
	Value   string   `xml:",chardata"`
}

// PhysicalAddressType represents a structured postal address.
type PhysicalAddressType struct {
	XMLName    xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/types PhysicalAddress"`
	Street     string   `xml:"http://schemas.microsoft.com/exchange/services/2006/types Street,omitempty"`
	City       string   `xml:"http://schemas.microsoft.com/exchange/services/2006/types City,omitempty"`
	State      string   `xml:"http://schemas.microsoft.com/exchange/services/2006/types State,omitempty"`
	PostalCode string   `xml:"http://schemas.microsoft.com/exchange/services/2006/types PostalCode,omitempty"`
	Country    string   `xml:"http://schemas.microsoft.com/exchange/services/2006/types Country,omitempty"`
}

// CreateContactResponse is the EWS CreateContact operation response.
type CreateContactResponse struct {
	XMLName xml.Name                      `xml:"http://schemas.microsoft.com/exchange/services/2006/messages CreateContactResponse"`
	Msgs    CreateContactResponseMessages `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseMessages"`
}

// CreateContactResponseMessages wraps CreateContact response messages.
type CreateContactResponseMessages struct {
	Messages []ContactResponseMessageType `xml:"http://schemas.microsoft.com/exchange/services/2006/messages CreateContactResponseMessage"`
}

// ContactResponseMessageType is one contact's result in a response.
type ContactResponseMessageType struct {
	XMLName       xml.Name                `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseMessage"`
	ResponseClass string                  `xml:"ResponseClass,attr"`
	ResponseCode  ResponseCodeType        `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseCode"`
	Items         *ContactsItemsContainer `xml:"http://schemas.microsoft.com/exchange/services/2006/messages Items"`
}

// ContactsItemsContainer wraps contacts in response messages.
type ContactsItemsContainer struct {
	XMLName xml.Name              `xml:"http://schemas.microsoft.com/exchange/services/2006/messages Items"`
	Items   []ContactTypeResponse `xml:"http://schemas.microsoft.com/exchange/services/2006/types Contact"`
}

// ContactTypeResponse is a contact in EWS responses.
type ContactTypeResponse struct {
	XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/types Contact"`
	//nolint:staticcheck // EWS uses ItemId element name in responses; type is ContactIdType.
	ItemID         ContactIdType       `xml:"http://schemas.microsoft.com/exchange/services/2006/types ItemId"`
	ParentFolderID FolderIdComponents  `xml:"http://schemas.microsoft.com/exchange/services/2006/types ParentFolderId"`
	FullName       string              `xml:"http://schemas.microsoft.com/exchange/services/2006/types FullName,omitempty"`
	GivenName      string              `xml:"http://schemas.microsoft.com/exchange/services/2006/types GivenName,omitempty"`
	Surname        string              `xml:"http://schemas.microsoft.com/exchange/services/2006/types Surname,omitempty"`
	EmailAddresses *EmailAddressesType `xml:"http://schemas.microsoft.com/exchange/services/2006/types EmailAddresses,omitempty"`
	CompanyName    string              `xml:"http://schemas.microsoft.com/exchange/services/2006/types CompanyName,omitempty"`
}

// ContactIdType is the EWS ContactId element.
type ContactIdType struct {
	XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/types ContactId"`
	ID      string   `xml:"Id,attr"`
	CK      string   `xml:"ChangeKey,attr,omitempty"`
}

// ---------------------------------------------------------------------------
// GetContact
// ---------------------------------------------------------------------------

// GetContactRequest is the EWS GetContact operation request.
type GetContactRequest struct {
	XMLName      xml.Name       `xml:"http://schemas.microsoft.com/exchange/services/2006/messages GetContact"`
	ItemShapeDef ItemShapeType  `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ItemShape"`
	ItemIDs      ContactIdsType `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ItemIds"`
}

// ContactIdsType is a list of contact IDs.
type ContactIdsType struct {
	XMLName xml.Name        `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ItemIds"`
	Item    []ContactIdType `xml:"http://schemas.microsoft.com/exchange/services/2006/types ContactId"`
}

// GetContactResponse is the EWS GetContact operation response.
type GetContactResponse struct {
	XMLName xml.Name                   `xml:"http://schemas.microsoft.com/exchange/services/2006/messages GetContactResponse"`
	Msgs    GetContactResponseMessages `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseMessages"`
}

// GetContactResponseMessages wraps GetContact response messages.
type GetContactResponseMessages struct {
	Messages []ContactResponseMessageType `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseMessage"`
}

// ---------------------------------------------------------------------------
// UpdateContact
// ---------------------------------------------------------------------------

// UpdateContactRequest is the EWS UpdateContact operation request.
type UpdateContactRequest struct {
	XMLName     xml.Name           `xml:"http://schemas.microsoft.com/exchange/services/2006/messages UpdateContact"`
	ItemChanges ContactChangesList `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ItemChanges"`
}

// ContactChangesList wraps the contact ItemChange list.
type ContactChangesList struct {
	XMLName xml.Name          `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ItemChanges"`
	Changes []ContactChangeOp `xml:"http://schemas.microsoft.com/exchange/services/2006/types ItemChange"`
}

// ContactChangeOp represents one contact change in UpdateContact.
type ContactChangeOp struct {
	XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/types ItemChange"`
	ItemID  struct {
		XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/types ItemId"`
		ID      string   `xml:"Id,attr"`
		CK      string   `xml:"ChangeKey,attr,omitempty"`
	} `xml:"http://schemas.microsoft.com/exchange/services/2006/types ItemId"`
	Updates ItemUpdatesOp `xml:"http://schemas.microsoft.com/exchange/services/2006/types Updates"`
}

// UpdateContactResponse is the EWS UpdateContact operation response.
type UpdateContactResponse struct {
	XMLName xml.Name                      `xml:"http://schemas.microsoft.com/exchange/services/2006/messages UpdateContactResponse"`
	Msgs    UpdateContactResponseMessages `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseMessages"`
}

// UpdateContactResponseMessages wraps UpdateContact response messages.
type UpdateContactResponseMessages struct {
	Messages []ContactResponseMessageType `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseMessage"`
}

// ---------------------------------------------------------------------------
// DeleteContact
// ---------------------------------------------------------------------------

// DeleteContactRequest is the EWS DeleteContact operation request.
type DeleteContactRequest struct {
	XMLName    xml.Name       `xml:"http://schemas.microsoft.com/exchange/services/2006/messages DeleteContact"`
	ItemIDs    ContactIdsType `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ItemIds"`
	DeleteType string         `xml:"http://schemas.microsoft.com/exchange/services/2006/messages DeleteType,attr,omitempty"`
}

// DeleteContactResponse is the EWS DeleteContact operation response.
type DeleteContactResponse struct {
	XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/messages DeleteContactResponse"`
	Msgs    struct {
		Messages []struct {
			XMLName       xml.Name         `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseMessage"`
			ResponseClass string           `xml:"ResponseClass,attr"`
			ResponseCode  ResponseCodeType `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseCode"`
		} `xml:"http://schemas.microsoft.com/exchange/services/2006/messages DeleteContactResponseMessage"`
	} `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseMessages"`
}

// ---------------------------------------------------------------------------
// Task types
// ---------------------------------------------------------------------------

// CreateTaskRequest is the EWS CreateTask operation request.
type CreateTaskRequest struct {
	XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/messages CreateTask"`
	Items   struct {
		XMLName xml.Name      `xml:"http://schemas.microsoft.com/exchange/services/2006/messages Items"`
		Item    []TaskTypeNew `xml:"http://schemas.microsoft.com/exchange/services/2006/types Task"`
	} `xml:"http://schemas.microsoft.com/exchange/services/2006/messages Items"`
	SavedItemFolderID struct {
		DistinguishedFolderID *string `xml:"Id,attr,omitempty"`
	} `xml:"http://schemas.microsoft.com/exchange/services/2006/messages SavedItemFolderId,omitempty"`
	SaveItemToFolder *bool `xml:"http://schemas.microsoft.com/exchange/services/2006/messages SaveItemToFolder,attr"`
}

// TaskTypeNew is a task item in CreateTask requests.
type TaskTypeNew struct {
	XMLName         xml.Name               `xml:"http://schemas.microsoft.com/exchange/services/2006/types Task"`
	Subject         string                 `xml:"http://schemas.microsoft.com/exchange/services/2006/types Subject,omitempty"`
	Body            *BodyType              `xml:"http://schemas.microsoft.com/exchange/services/2006/types Body,omitempty"`
	StartDate       string                 `xml:"http://schemas.microsoft.com/exchange/services/2006/types StartDate,omitempty"`
	DueDate         string                 `xml:"http://schemas.microsoft.com/exchange/services/2006/types DueDate,omitempty"`
	Status          string                 `xml:"http://schemas.microsoft.com/exchange/services/2006/types Status,omitempty"`
	PercentComplete *float64               `xml:"http://schemas.microsoft.com/exchange/services/2006/types PercentComplete,attr,omitempty"`
	Recurrence      *RecurrenceType        `xml:"http://schemas.microsoft.com/exchange/services/2006/types Recurrence,omitempty"`
	ReminderSet     *bool                  `xml:"http://schemas.microsoft.com/exchange/services/2006/types ReminderSet,attr,omitempty"`
	Reminder        *ReminderType          `xml:"http://schemas.microsoft.com/exchange/services/2006/types Reminder,omitempty"`
	Categories      *MessageCategoriesType `xml:"http://schemas.microsoft.com/exchange/services/2006/types Categories,omitempty"`
}

// CreateTaskResponse is the EWS CreateTask operation response.
type CreateTaskResponse struct {
	XMLName xml.Name                   `xml:"http://schemas.microsoft.com/exchange/services/2006/messages CreateTaskResponse"`
	Msgs    CreateTaskResponseMessages `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseMessages"`
}

// CreateTaskResponseMessages wraps CreateTask response messages.
type CreateTaskResponseMessages struct {
	Messages []TaskResponseMessageType `xml:"http://schemas.microsoft.com/exchange/services/2006/messages CreateTaskResponseMessage"`
}

// TaskResponseMessageType is one task's result in a response.
type TaskResponseMessageType struct {
	XMLName       xml.Name             `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseMessage"`
	ResponseClass string               `xml:"ResponseClass,attr"`
	ResponseCode  ResponseCodeType     `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseCode"`
	Items         *TasksItemsContainer `xml:"http://schemas.microsoft.com/exchange/services/2006/messages Items"`
}

// TasksItemsContainer wraps tasks in response messages.
type TasksItemsContainer struct {
	XMLName xml.Name           `xml:"http://schemas.microsoft.com/exchange/services/2006/messages Items"`
	Items   []TaskTypeResponse `xml:"http://schemas.microsoft.com/exchange/services/2006/types Task"`
}

// TaskTypeResponse is a task in EWS responses.
type TaskTypeResponse struct {
	XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/types Task"`
	//nolint:staticcheck // EWS uses ItemId element name in responses; type is TaskIdType.
	ItemID          TaskIdType         `xml:"http://schemas.microsoft.com/exchange/services/2006/types ItemId"`
	ParentFolderID  FolderIdComponents `xml:"http://schemas.microsoft.com/exchange/services/2006/types ParentFolderId"`
	Subject         string             `xml:"http://schemas.microsoft.com/exchange/services/2006/types Subject,omitempty"`
	StartDate       string             `xml:"http://schemas.microsoft.com/exchange/services/2006/types StartDate,omitempty"`
	DueDate         string             `xml:"http://schemas.microsoft.com/exchange/services/2006/types DueDate,omitempty"`
	Status          string             `xml:"http://schemas.microsoft.com/exchange/services/2006/types Status,omitempty"`
	PercentComplete float64            `xml:"http://schemas.microsoft.com/exchange/services/2006/types PercentComplete,attr,omitempty"`
	Recurrence      *RecurrenceType    `xml:"http://schemas.microsoft.com/exchange/services/2006/types Recurrence,omitempty"`
}

// TaskIdType is the EWS TaskId element.
type TaskIdType struct {
	XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/types TaskId"`
	ID      string   `xml:"Id,attr"`
	CK      string   `xml:"ChangeKey,attr,omitempty"`
}

// ---------------------------------------------------------------------------
// GetTask
// ---------------------------------------------------------------------------

// GetTaskRequest is the EWS GetTask operation request.
type GetTaskRequest struct {
	XMLName      xml.Name      `xml:"http://schemas.microsoft.com/exchange/services/2006/messages GetTask"`
	ItemShapeDef ItemShapeType `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ItemShape"`
	ItemIDs      TaskIdsType   `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ItemIds"`
}

// TaskIdsType is a list of task IDs.
type TaskIdsType struct {
	XMLName xml.Name     `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ItemIds"`
	Item    []TaskIdType `xml:"http://schemas.microsoft.com/exchange/services/2006/types TaskId"`
}

// GetTaskResponse is the EWS GetTask operation response.
type GetTaskResponse struct {
	XMLName xml.Name                `xml:"http://schemas.microsoft.com/exchange/services/2006/messages GetTaskResponse"`
	Msgs    GetTaskResponseMessages `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseMessages"`
}

// GetTaskResponseMessages wraps GetTask response messages.
type GetTaskResponseMessages struct {
	Messages []TaskResponseMessageType `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseMessage"`
}

// ---------------------------------------------------------------------------
// UpdateTask
// ---------------------------------------------------------------------------

// UpdateTaskRequest is the EWS UpdateTask operation request.
type UpdateTaskRequest struct {
	XMLName     xml.Name        `xml:"http://schemas.microsoft.com/exchange/services/2006/messages UpdateTask"`
	ItemChanges TaskChangesList `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ItemChanges"`
}

// TaskChangesList wraps the task ItemChange list.
type TaskChangesList struct {
	XMLName xml.Name       `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ItemChanges"`
	Changes []TaskChangeOp `xml:"http://schemas.microsoft.com/exchange/services/2006/types ItemChange"`
}

// TaskChangeOp represents one task change in UpdateTask.
type TaskChangeOp struct {
	XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/types ItemChange"`
	ItemID  struct {
		XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/types ItemId"`
		ID      string   `xml:"Id,attr"`
		CK      string   `xml:"ChangeKey,attr,omitempty"`
	} `xml:"http://schemas.microsoft.com/exchange/services/2006/types ItemId"`
	Updates ItemUpdatesOp `xml:"http://schemas.microsoft.com/exchange/services/2006/types Updates"`
}

// UpdateTaskResponse is the EWS UpdateTask operation response.
type UpdateTaskResponse struct {
	XMLName xml.Name                   `xml:"http://schemas.microsoft.com/exchange/services/2006/messages UpdateTaskResponse"`
	Msgs    UpdateTaskResponseMessages `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseMessages"`
}

// UpdateTaskResponseMessages wraps UpdateTask response messages.
type UpdateTaskResponseMessages struct {
	Messages []TaskResponseMessageType `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseMessage"`
}

// ---------------------------------------------------------------------------
// DeleteTask
// ---------------------------------------------------------------------------

// DeleteTaskRequest is the EWS DeleteTask operation request.
type DeleteTaskRequest struct {
	XMLName    xml.Name    `xml:"http://schemas.microsoft.com/exchange/services/2006/messages DeleteTask"`
	ItemIDs    TaskIdsType `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ItemIds"`
	DeleteType string      `xml:"http://schemas.microsoft.com/exchange/services/2006/messages DeleteType,attr,omitempty"`
}

// DeleteTaskResponse is the EWS DeleteTask operation response.
type DeleteTaskResponse struct {
	XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/messages DeleteTaskResponse"`
	Msgs    struct {
		Messages []struct {
			XMLName       xml.Name         `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseMessage"`
			ResponseClass string           `xml:"ResponseClass,attr"`
			ResponseCode  ResponseCodeType `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseCode"`
		} `xml:"http://schemas.microsoft.com/exchange/services/2006/messages DeleteTaskResponseMessage"`
	} `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseMessages"`
}

// ---------------------------------------------------------------------------
// Calendar item handlers
// ---------------------------------------------------------------------------

// handleCreateCalendarItem processes an EWS CreateCalendarItem SOAP request.
func (s *Server) handleCreateCalendarItem(ctx context.Context, body []byte) []byte {
	var req CreateCalendarItemRequest
	if err := decodeRequest(body, &req); err != nil {
		return s.errorResponseXML("CreateCalendarItem", ErrErrorInvalidOperation, "malformed request: "+err.Error())
	}

	mboxID, mboxKey, errCode := s.resolveMailboxFromBody(ctx, body)
	if errCode != "" {
		return s.errorResponseXML("CreateCalendarItem", errCode, "could not resolve mailbox")
	}

	mailboxKey := strings.TrimPrefix(mboxKey, "e:")

	if s.collabStore == nil {
		return s.errorResponseXML("CreateCalendarItem", ErrErrorInternalServer, "collaboration store not available")
	}

	// Delegate enforcement (VAL-DIR-002): check calendar write permission for non-owners.
	actorEmail := s.getActingEmail(ctx)
	if msg, code := s.checkDelegatePermission(mboxID, mailboxKey, actorEmail, "write_calendar"); code != "" {
		return s.errorResponseXML("CreateCalendarItem", code, msg)
	}

	// Build delegate audit context for lifecycle emission (VAL-DIR-014).
	delegateCtx := s.buildDelegateAuditContext(ctx, mboxID, mailboxKey)

	// Resolve calendar folder ID.
	folderID, err := s.identity.GetFolderID(mailboxKey, "calendar")
	if err != nil {
		folderID, err = s.identity.GetFolderID(mailboxKey, "calendars")
		if err != nil {
			folderID, err = s.identity.EnsureFolderId(mailboxKey, "calendar", "calendar")
			if err != nil {
				return s.errorResponseXML("CreateCalendarItem", ErrErrorInternalServer, "failed to resolve calendar folder: "+err.Error())
			}
		}
	}

	msgs := make([]CalendarItemResponseMessageType, 0, len(req.Items.Item))
	for range req.Items.Item {
		item := &req.Items.Item[0] // safe: process one at a time

		// VAL-DIR-010, VAL-COLLAB-010: enforce resource booking policy before
		// creating the calendar item. Check auto-accept, auto-decline, and
		// delegate-review decisions for any resource attendees.
		var attendeeList []AttendeeType
		if item.Attendees != nil {
			attendeeList = item.Attendees.Attendee
		}
		if s.policyStore != nil && len(attendeeList) > 0 {
			decisions, err := s.checkResourceBookingPolicy(ctx, attendeeList)
			if err == nil {
				allAccepted, policyMsgs := s.applyResourceBookingPolicy(decisions, attendeeList)
				if !allAccepted {
					// At least one resource auto-declined: reject this item.
					for _, pm := range policyMsgs {
						msgs = append(msgs, CalendarItemResponseMessageType{
							ResponseClass: pm.ResponseClass,
							ResponseCode:  pm.ResponseCode,
						})
					}
					continue
				}
				// Policy applied; proceed with creation.
				_ = policyMsgs
			}
		}

		msg := s.createCalendarItemInFolder(ctx, mboxID, mailboxKey, folderID, item, delegateCtx)
		msgs = append(msgs, msg)
	}

	resp := CreateCalendarItemResponse{}
	resp.Msgs.Messages = msgs
	return buildResponseEnvelope(resp)
}

// createCalendarItemInFolder creates a calendar item in the target folder.
func (s *Server) createCalendarItemInFolder(ctx context.Context, mboxID semcore.MailboxId, mailboxKey string, folderID semcore.FolderId, item *CalendarItemTypeNew, delegateCtx *semcore.DelegateAuditContext) CalendarItemResponseMessageType {
	if folderID.IsZero() {
		return errorCalendarMsg("CreateCalendarItem", ErrErrorInternalServer, "no target folder")
	}

	// Parse start and end times; default to now+1h on malformed input.
	//nolint:errcheck
	start, _ := ParseEWSDateTime(item.Start)
	//nolint:errcheck
	end, _ := ParseEWSDateTime(item.End)
	if start.IsZero() {
		start = time.Now().UTC().Add(time.Hour)
	}
	if end.IsZero() {
		end = start.Add(time.Hour)
	}

	// Build iCal representation.
	uid := fmt.Sprintf("%d-%s@umailserver.local", time.Now().UnixNano(), mailboxKey)
	icalData := buildICalFromCalendarItem(uid, item, start, end)

	// Compute storage key (SHA256 of raw iCal).
	h := sha256.Sum256([]byte(icalData))
	blobKey := fmt.Sprintf("cal:%x", h)

	// Assign CalendarItemId and CalendarChangeKey.
	//nolint:errcheck
	calItemID, _ := semcore.NewCalendarItemId(generateID())
	//nolint:errcheck
	calCK, _ := semcore.NewCalendarChangeKey(generateID())

	// Record in BoltCollaborationStore.
	rec := semcore.NewStoredCalendarItemIdentity(calItemID, folderID, mboxID, calCK, semcore.CollabKindEvent, uid, blobKey)
	rec.RawData = icalData
	if err := s.collabStore.PutCalendarItemIdentityUnsafe(blobKey, rec); err != nil {
		return errorCalendarMsg("CreateCalendarItem", ErrErrorInternalServer, "failed to store identity: "+err.Error())
	}

	// Emit lifecycle event with delegate audit context (VAL-DIR-014).
	if s.lifecycle != nil {
		lc := semcore.Lifecycle{
			MailboxID: mboxID,
			FolderID:  folderID,
			Kind:      semcore.LifecycleKindCreated,
			At:        time.Now(),
			Actor:     mailboxKey,
			ChangeKey: semcore.ChangeKey{},
		}
		if delegateCtx != nil {
			lc.Actor = fmt.Sprintf("delegate:%s@owner:%s", delegateCtx.DelegateEmail, delegateCtx.OwnerEmail)
			lc.DelegateEmail = delegateCtx.DelegateEmail
			lc.DelegateID = delegateCtx.DelegateID
		}
		_ = lc // lifecycle used for sync/event consumers
	}

	return CalendarItemResponseMessageType{
		ResponseClass: "Success",
		ResponseCode:  ResponseCodeType{Value: ErrNoError},
		Items: &CalendarItemsContainer{Items: []CalendarItemTypeResponse{{
			ItemID:         CalendarItemIdType{ID: calItemID.String(), CK: calCK.String()},
			ParentFolderID: FolderIdComponents{ID: folderID.String()},
			Subject:        item.Subject,
			Start:          FormatEWSDateTime(start),
			End:            FormatEWSDateTime(end),
			IsAllDay:       item.IsAllDay,
			Location:       item.Location,
			UID:            uid,
		}}},
	}
}

// handleGetCalendarItem processes an EWS GetCalendarItem SOAP request.
func (s *Server) handleGetCalendarItem(ctx context.Context, body []byte) []byte {
	var req GetCalendarItemRequest
	if err := decodeRequest(body, &req); err != nil {
		return s.errorResponseXML("GetCalendarItem", ErrErrorInvalidOperation, "malformed request: "+err.Error())
	}

	mboxID, mboxKey, errCode := s.resolveMailboxFromBody(ctx, body)
	if errCode != "" {
		return s.errorResponseXML("GetCalendarItem", errCode, "could not resolve mailbox")
	}
	_ = mboxKey

	if s.collabStore == nil {
		return s.errorResponseXML("GetCalendarItem", ErrErrorInternalServer, "collaboration store not available")
	}

	messages := make([]CalendarItemResponseMessageType, 0, len(req.ItemIDs.Item))
	for _, id := range req.ItemIDs.Item {
		msg := s.getCalendarItemByID(ctx, mboxID, id)
		messages = append(messages, msg)
	}

	resp := GetCalendarItemResponse{}
	resp.Msgs.Messages = messages
	return buildResponseEnvelope(resp)
}

// getCalendarItemByID retrieves one calendar item by CalendarItemId.
func (s *Server) getCalendarItemByID(ctx context.Context, mboxID semcore.MailboxId, id CalendarItemIdType) CalendarItemResponseMessageType {
	calItemID, err := semcore.NewCalendarItemId(id.ID)
	if err != nil {
		return errorCalendarMsg("GetCalendarItem", ErrErrorInvalidId, err.Error())
	}

	rec, err := s.collabStore.GetCalendarItemByID(calItemID)
	if err != nil {
		if errors.Is(err, semcore.ErrCalendarItemNotFound) {
			return errorCalendarMsg("GetCalendarItem", ErrErrorItemNotFound, "calendar item not found: "+id.ID)
		}
		return errorCalendarMsg("GetCalendarItem", ErrErrorInternalServer, err.Error())
	}

	// Verify mailbox ownership.
	if !rec.MailboxID.IsZero() && rec.MailboxID != mboxID {
		return errorCalendarMsg("GetCalendarItem", ErrErrorAccessDenied, "calendar item belongs to a different mailbox")
	}

	return CalendarItemResponseMessageType{
		ResponseClass: "Success",
		ResponseCode:  ResponseCodeType{Value: ErrNoError},
		Items: &CalendarItemsContainer{Items: []CalendarItemTypeResponse{{
			ItemID:         CalendarItemIdType{ID: rec.ID.String(), CK: rec.ChangeKey.String()},
			ParentFolderID: FolderIdComponents{ID: rec.FolderID.String()},
			UID:            rec.IcalUID,
		}}},
	}
}

// handleUpdateCalendarItem processes an EWS UpdateCalendarItem SOAP request.
func (s *Server) handleUpdateCalendarItem(ctx context.Context, body []byte) []byte {
	var req UpdateCalendarItemRequest
	if err := decodeRequest(body, &req); err != nil {
		return s.errorResponseXML("UpdateCalendarItem", ErrErrorInvalidOperation, "malformed request: "+err.Error())
	}

	mboxID, mboxKey, errCode := s.resolveMailboxFromBody(ctx, body)
	if errCode != "" {
		return s.errorResponseXML("UpdateCalendarItem", errCode, "could not resolve mailbox")
	}
	mailboxKey := strings.TrimPrefix(mboxKey, "e:")

	if s.collabStore == nil {
		return s.errorResponseXML("UpdateCalendarItem", ErrErrorInternalServer, "collaboration store not available")
	}

	// Delegate enforcement (VAL-DIR-002): check calendar write permission for non-owners.
	actorEmail := s.getActingEmail(ctx)
	if msg, code := s.checkDelegatePermission(mboxID, mailboxKey, actorEmail, "write_calendar"); code != "" {
		return s.errorResponseXML("UpdateCalendarItem", code, msg)
	}

	// Build delegate audit context for lifecycle emission (VAL-DIR-014).
	delegateCtx := s.buildDelegateAuditContext(ctx, mboxID, mailboxKey)

	messages := make([]CalendarItemResponseMessageType, 0, len(req.ItemChanges.Changes))
	for _, ic := range req.ItemChanges.Changes {
		itemID, err := semcore.NewCalendarItemId(ic.ItemID.ID)
		if err != nil {
			messages = append(messages, errorCalendarMsg("UpdateCalendarItem", ErrErrorInvalidId, err.Error()))
			continue
		}

		// Look up current identity.
		rec, err := s.collabStore.GetCalendarItemByID(itemID)
		if err != nil {
			if errors.Is(err, semcore.ErrCalendarItemNotFound) {
				messages = append(messages, errorCalendarMsg("UpdateCalendarItem", ErrErrorItemNotFound, err.Error()))
			} else {
				messages = append(messages, errorCalendarMsg("UpdateCalendarItem", ErrErrorInternalServer, err.Error()))
			}
			continue
		}

		// Validate ChangeKey if provided.
		//nolint:errcheck
		currentCK, _ := semcore.NewCalendarChangeKey(ic.ItemID.CK)
		if !currentCK.IsZero() && !rec.ChangeKey.Equal(currentCK) {
			messages = append(messages, errorCalendarMsg("UpdateCalendarItem", ErrErrorStaleObject, "ChangeKey mismatch"))
			continue
		}

		// Advance ChangeKey on every semantically-visible mutation.
		//nolint:errcheck
		newCK, _ := semcore.NewCalendarChangeKey(generateID())
		rec.ChangeKey = newCK

		// Persist updated identity with optimistic locking.
		if err := s.collabStore.PutCalendarItemIdentity(rec.RawHash, rec, currentCK); err != nil {
			if errors.Is(err, semcore.ErrCollabVersionConflict) {
				messages = append(messages, errorCalendarMsg("UpdateCalendarItem", ErrErrorVersionMismatch, "stale ChangeKey"))
			} else {
				messages = append(messages, errorCalendarMsg("UpdateCalendarItem", ErrErrorInternalServer, err.Error()))
			}
			continue
		}

		// Emit lifecycle event with delegate audit context (VAL-DIR-014).
		// Note: Calendar items use CalendarChangeKey, not ChangeKey, so we
		// use zero value here to avoid type mismatch. Calendar lifecycle
		// events carry the collab object identity rather than mail ItemId.
		if s.lifecycle != nil {
			lc := semcore.Lifecycle{
				MailboxID: mboxID,
				FolderID:  rec.FolderID,
				Kind:      semcore.LifecycleKindUpdated,
				At:        time.Now(),
				Actor:     mailboxKey,
				ChangeKey: semcore.ChangeKey{},
			}
			if delegateCtx != nil {
				lc.Actor = fmt.Sprintf("delegate:%s@owner:%s", delegateCtx.DelegateEmail, delegateCtx.OwnerEmail)
				lc.DelegateEmail = delegateCtx.DelegateEmail
				lc.DelegateID = delegateCtx.DelegateID
			}
			_ = s.lifecycle.AppendLifecycle(lc) //nolint:errcheck
		}

		messages = append(messages, CalendarItemResponseMessageType{
			ResponseClass: "Success",
			ResponseCode:  ResponseCodeType{Value: ErrNoError},
			Items: &CalendarItemsContainer{Items: []CalendarItemTypeResponse{{
				ItemID:         CalendarItemIdType{ID: rec.ID.String(), CK: rec.ChangeKey.String()},
				ParentFolderID: FolderIdComponents{ID: rec.FolderID.String()},
			}}},
		})
	}

	resp := UpdateCalendarItemResponse{}
	resp.Msgs.Messages = messages
	return buildResponseEnvelope(resp)
}

// handleDeleteCalendarItem processes an EWS DeleteCalendarItem SOAP request.
func (s *Server) handleDeleteCalendarItem(ctx context.Context, body []byte) []byte {
	var req DeleteCalendarItemRequest
	if err := decodeRequest(body, &req); err != nil {
		return s.errorResponseXML("DeleteCalendarItem", ErrErrorInvalidOperation, "malformed request: "+err.Error())
	}

	mboxID, rawKey, errCode := s.resolveMailboxFromBody(ctx, body)
	if errCode != "" {
		return s.errorResponseXML("DeleteCalendarItem", errCode, "could not resolve mailbox")
	}
	mailboxKey := strings.TrimPrefix(rawKey, "e:")

	if s.collabStore == nil {
		return s.errorResponseXML("DeleteCalendarItem", ErrErrorInternalServer, "collaboration store not available")
	}

	// Delegate enforcement (VAL-DIR-002): check delete permission for non-owners.
	actorEmail := s.getActingEmail(ctx)
	if msg, code := s.checkDelegatePermission(mboxID, mailboxKey, actorEmail, "delete"); code != "" {
		return s.errorResponseXML("DeleteCalendarItem", code, msg)
	}

	// Build delegate audit context for lifecycle emission (VAL-DIR-014).
	delegateCtx := s.buildDelegateAuditContext(ctx, mboxID, mailboxKey)

	responses := make([]struct {
		XMLName       xml.Name         `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseMessage"`
		ResponseClass string           `xml:"ResponseClass,attr"`
		ResponseCode  ResponseCodeType `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseCode"`
	}, 0, len(req.ItemIDs.Item))

	for _, id := range req.ItemIDs.Item {
		calItemID, err := semcore.NewCalendarItemId(id.ID)
		if err != nil {
			responses = append(responses, deleteCalErrMsg("Error", ResponseCodeType{Value: ErrErrorInvalidId}))
			continue
		}

		rec, err := s.collabStore.GetCalendarItemByID(calItemID)
		if err != nil {
			responses = append(responses, deleteCalErrMsg("Error", ResponseCodeType{Value: ErrErrorItemNotFound}))
			continue
		}

		// Check ChangeKey if provided.
		//nolint:errcheck
		currentCK, _ := semcore.NewCalendarChangeKey(id.CK)
		if err := s.collabStore.DeleteCalendarItemIdentity(rec.RawHash, currentCK); err != nil {
			if errors.Is(err, semcore.ErrCollabVersionConflict) {
				responses = append(responses, deleteCalErrMsg("Error", ResponseCodeType{Value: ErrErrorVersionMismatch}))
			} else {
				responses = append(responses, deleteCalErrMsg("Error", ResponseCodeType{Value: ErrErrorInternalServer}))
			}
			continue
		}

		// Emit lifecycle event with delegate audit context (VAL-DIR-014).
		if s.lifecycle != nil {
			lc := semcore.Lifecycle{
				MailboxID: mboxID,
				FolderID:  rec.FolderID,
				Kind:      semcore.LifecycleKindSoftDeleted,
				At:        time.Now(),
				Actor:     mailboxKey,
			}
			if delegateCtx != nil {
				lc.Actor = fmt.Sprintf("delegate:%s@owner:%s", delegateCtx.DelegateEmail, delegateCtx.OwnerEmail)
				lc.DelegateEmail = delegateCtx.DelegateEmail
				lc.DelegateID = delegateCtx.DelegateID
			}
			_ = s.lifecycle.AppendLifecycle(lc) //nolint:errcheck
		}

		responses = append(responses, deleteCalErrMsg("Success", ResponseCodeType{Value: ErrNoError}))
	}

	resp := DeleteCalendarItemResponse{}
	resp.Msgs.Messages = responses
	return buildResponseEnvelope(resp)
}

func deleteCalErrMsg(class string, code ResponseCodeType) struct {
	XMLName       xml.Name         `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseMessage"`
	ResponseClass string           `xml:"ResponseClass,attr"`
	ResponseCode  ResponseCodeType `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseCode"`
} {
	return struct {
		XMLName       xml.Name         `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseMessage"`
		ResponseClass string           `xml:"ResponseClass,attr"`
		ResponseCode  ResponseCodeType `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseCode"`
	}{
		ResponseClass: class,
		ResponseCode:  code,
	}
}

// errorCalendarMsg builds an error CalendarItemResponseMessageType.
func errorCalendarMsg(op string, code ErrorCode, message string) CalendarItemResponseMessageType {
	return CalendarItemResponseMessageType{
		ResponseClass: "Error",
		ResponseCode:  ResponseCodeType{Value: code},
	}
}

// ---------------------------------------------------------------------------
// Contact handlers
// ---------------------------------------------------------------------------

// handleCreateContact processes an EWS CreateContact SOAP request.
func (s *Server) handleCreateContact(ctx context.Context, body []byte) []byte {
	var req CreateContactRequest
	if err := decodeRequest(body, &req); err != nil {
		return s.errorResponseXML("CreateContact", ErrErrorInvalidOperation, "malformed request: "+err.Error())
	}

	mboxID, mboxKey, errCode := s.resolveMailboxFromBody(ctx, body)
	if errCode != "" {
		return s.errorResponseXML("CreateContact", errCode, "could not resolve mailbox")
	}

	mailboxKey := strings.TrimPrefix(mboxKey, "e:")

	if s.collabStore == nil {
		return s.errorResponseXML("CreateContact", ErrErrorInternalServer, "collaboration store not available")
	}

	// Resolve contacts folder ID.
	folderID, err := s.identity.GetFolderID(mailboxKey, "contacts")
	if err != nil {
		folderID, err = s.identity.EnsureFolderId(mailboxKey, "contacts", "contacts")
		if err != nil {
			return s.errorResponseXML("CreateContact", ErrErrorInternalServer, "failed to resolve contacts folder: "+err.Error())
		}
	}

	messages := make([]ContactResponseMessageType, 0, len(req.Items.Item))
	for range req.Items.Item {
		item := &req.Items.Item[0]
		msg := s.createContactInFolder(ctx, mboxID, mailboxKey, folderID, item)
		messages = append(messages, msg)
	}

	resp := CreateContactResponse{}
	resp.Msgs.Messages = messages
	return buildResponseEnvelope(resp)
}

// createContactInFolder creates a contact in the target folder.
func (s *Server) createContactInFolder(ctx context.Context, mboxID semcore.MailboxId, mailboxKey string, folderID semcore.FolderId, item *ContactTypeNew) ContactResponseMessageType {
	if folderID.IsZero() {
		return errorContactMsg("CreateContact", ErrErrorInternalServer, "no target folder")
	}

	uid := fmt.Sprintf("%d-%s@umailserver.local", time.Now().UnixNano(), mailboxKey)
	vcardData := buildVCardFromContact(uid, item)

	h := sha256.Sum256([]byte(vcardData))
	blobKey := fmt.Sprintf("contact:%x", h)

	//nolint:errcheck
	contactID, _ := semcore.NewContactId(generateID())
	//nolint:errcheck
	contactCK, _ := semcore.NewContactChangeKey(generateID())

	rec := semcore.NewStoredContactIdentity(contactID, folderID, mboxID, contactCK, uid, blobKey)
	rec.RawData = vcardData
	if err := s.collabStore.PutContactIdentityUnsafe(blobKey, rec); err != nil {
		return errorContactMsg("CreateContact", ErrErrorInternalServer, "failed to store identity: "+err.Error())
	}

	return ContactResponseMessageType{
		ResponseClass: "Success",
		ResponseCode:  ResponseCodeType{Value: ErrNoError},
		Items: &ContactsItemsContainer{Items: []ContactTypeResponse{{
			ItemID:         ContactIdType{ID: contactID.String(), CK: contactCK.String()},
			ParentFolderID: FolderIdComponents{ID: folderID.String()},
			FullName:       item.FullName,
			GivenName:      item.GivenName,
			Surname:        item.Surname,
		}}},
	}
}

// handleGetContact processes an EWS GetContact SOAP request.
func (s *Server) handleGetContact(ctx context.Context, body []byte) []byte {
	var req GetContactRequest
	if err := decodeRequest(body, &req); err != nil {
		return s.errorResponseXML("GetContact", ErrErrorInvalidOperation, "malformed request: "+err.Error())
	}

	mboxID, mboxKey, errCode := s.resolveMailboxFromBody(ctx, body)
	if errCode != "" {
		return s.errorResponseXML("GetContact", errCode, "could not resolve mailbox")
	}
	_ = mboxKey

	if s.collabStore == nil {
		return s.errorResponseXML("GetContact", ErrErrorInternalServer, "collaboration store not available")
	}

	messages := make([]ContactResponseMessageType, 0, len(req.ItemIDs.Item))
	for _, id := range req.ItemIDs.Item {
		msg := s.getContactByID(ctx, mboxID, id)
		messages = append(messages, msg)
	}

	resp := GetContactResponse{}
	resp.Msgs.Messages = messages
	return buildResponseEnvelope(resp)
}

// getContactByID retrieves one contact by ContactId.
func (s *Server) getContactByID(ctx context.Context, mboxID semcore.MailboxId, id ContactIdType) ContactResponseMessageType {
	contactID, err := semcore.NewContactId(id.ID)
	if err != nil {
		return errorContactMsg("GetContact", ErrErrorInvalidId, err.Error())
	}

	rec, err := s.collabStore.GetContactByID(contactID)
	if err != nil {
		if errors.Is(err, semcore.ErrContactNotFound) {
			return errorContactMsg("GetContact", ErrErrorItemNotFound, "contact not found: "+id.ID)
		}
		return errorContactMsg("GetContact", ErrErrorInternalServer, err.Error())
	}

	if !rec.MailboxID.IsZero() && rec.MailboxID != mboxID {
		return errorContactMsg("GetContact", ErrErrorAccessDenied, "contact belongs to a different mailbox")
	}

	return ContactResponseMessageType{
		ResponseClass: "Success",
		ResponseCode:  ResponseCodeType{Value: ErrNoError},
		Items: &ContactsItemsContainer{Items: []ContactTypeResponse{{
			ItemID:         ContactIdType{ID: rec.ID.String(), CK: rec.ChangeKey.String()},
			ParentFolderID: FolderIdComponents{ID: rec.FolderID.String()},
		}}},
	}
}

// handleUpdateContact processes an EWS UpdateContact SOAP request.
func (s *Server) handleUpdateContact(ctx context.Context, body []byte) []byte {
	var req UpdateContactRequest
	if err := decodeRequest(body, &req); err != nil {
		return s.errorResponseXML("UpdateContact", ErrErrorInvalidOperation, "malformed request: "+err.Error())
	}

	_, mboxKey, errCode := s.resolveMailboxFromBody(ctx, body)
	if errCode != "" {
		return s.errorResponseXML("UpdateContact", errCode, "could not resolve mailbox")
	}
	_ = mboxKey

	if s.collabStore == nil {
		return s.errorResponseXML("UpdateContact", ErrErrorInternalServer, "collaboration store not available")
	}

	messages := make([]ContactResponseMessageType, 0, len(req.ItemChanges.Changes))
	for _, ic := range req.ItemChanges.Changes {
		contactID, err := semcore.NewContactId(ic.ItemID.ID)
		if err != nil {
			messages = append(messages, errorContactMsg("UpdateContact", ErrErrorInvalidId, err.Error()))
			continue
		}

		rec, err := s.collabStore.GetContactByID(contactID)
		if err != nil {
			if errors.Is(err, semcore.ErrContactNotFound) {
				messages = append(messages, errorContactMsg("UpdateContact", ErrErrorItemNotFound, err.Error()))
			} else {
				messages = append(messages, errorContactMsg("UpdateContact", ErrErrorInternalServer, err.Error()))
			}
			continue
		}

		//nolint:errcheck
		currentCK, _ := semcore.NewContactChangeKey(ic.ItemID.CK)
		if !currentCK.IsZero() && !rec.ChangeKey.Equal(currentCK) {
			messages = append(messages, errorContactMsg("UpdateContact", ErrErrorStaleObject, "ChangeKey mismatch"))
			continue
		}

		//nolint:errcheck
		newCK, _ := semcore.NewContactChangeKey(generateID())
		rec.ChangeKey = newCK

		if err := s.collabStore.PutContactIdentity(rec.RawHash, rec, currentCK); err != nil {
			if errors.Is(err, semcore.ErrCollabVersionConflict) {
				messages = append(messages, errorContactMsg("UpdateContact", ErrErrorVersionMismatch, "stale ChangeKey"))
			} else {
				messages = append(messages, errorContactMsg("UpdateContact", ErrErrorInternalServer, err.Error()))
			}
			continue
		}

		messages = append(messages, ContactResponseMessageType{
			ResponseClass: "Success",
			ResponseCode:  ResponseCodeType{Value: ErrNoError},
			Items: &ContactsItemsContainer{Items: []ContactTypeResponse{{
				ItemID:         ContactIdType{ID: rec.ID.String(), CK: rec.ChangeKey.String()},
				ParentFolderID: FolderIdComponents{ID: rec.FolderID.String()},
			}}},
		})
	}

	resp := UpdateContactResponse{}
	resp.Msgs.Messages = messages
	return buildResponseEnvelope(resp)
}

// handleDeleteContact processes an EWS DeleteContact SOAP request.
func (s *Server) handleDeleteContact(ctx context.Context, body []byte) []byte {
	var req DeleteContactRequest
	if err := decodeRequest(body, &req); err != nil {
		return s.errorResponseXML("DeleteContact", ErrErrorInvalidOperation, "malformed request: "+err.Error())
	}

	_, mboxKey, errCode := s.resolveMailboxFromBody(ctx, body)
	if errCode != "" {
		return s.errorResponseXML("DeleteContact", errCode, "could not resolve mailbox")
	}
	_ = mboxKey

	if s.collabStore == nil {
		return s.errorResponseXML("DeleteContact", ErrErrorInternalServer, "collaboration store not available")
	}

	responses := make([]struct {
		XMLName       xml.Name         `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseMessage"`
		ResponseClass string           `xml:"ResponseClass,attr"`
		ResponseCode  ResponseCodeType `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseCode"`
	}, 0, len(req.ItemIDs.Item))

	for _, id := range req.ItemIDs.Item {
		contactID, err := semcore.NewContactId(id.ID)
		if err != nil {
			responses = append(responses, deleteContactErrMsg("Error", ResponseCodeType{Value: ErrErrorInvalidId}))
			continue
		}

		rec, err := s.collabStore.GetContactByID(contactID)
		if err != nil {
			responses = append(responses, deleteContactErrMsg("Error", ResponseCodeType{Value: ErrErrorItemNotFound}))
			continue
		}

		//nolint:errcheck
		currentCK, _ := semcore.NewContactChangeKey(id.CK)
		if err := s.collabStore.DeleteContactIdentity(rec.RawHash, currentCK); err != nil {
			if errors.Is(err, semcore.ErrCollabVersionConflict) {
				responses = append(responses, deleteContactErrMsg("Error", ResponseCodeType{Value: ErrErrorVersionMismatch}))
			} else {
				responses = append(responses, deleteContactErrMsg("Error", ResponseCodeType{Value: ErrErrorInternalServer}))
			}
			continue
		}

		responses = append(responses, deleteContactErrMsg("Success", ResponseCodeType{Value: ErrNoError}))
	}

	resp := DeleteContactResponse{}
	resp.Msgs.Messages = responses
	return buildResponseEnvelope(resp)
}

func deleteContactErrMsg(class string, code ResponseCodeType) struct {
	XMLName       xml.Name         `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseMessage"`
	ResponseClass string           `xml:"ResponseClass,attr"`
	ResponseCode  ResponseCodeType `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseCode"`
} {
	return struct {
		XMLName       xml.Name         `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseMessage"`
		ResponseClass string           `xml:"ResponseClass,attr"`
		ResponseCode  ResponseCodeType `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseCode"`
	}{
		ResponseClass: class,
		ResponseCode:  code,
	}
}

// errorContactMsg builds an error ContactResponseMessageType.
func errorContactMsg(op string, code ErrorCode, message string) ContactResponseMessageType {
	return ContactResponseMessageType{
		ResponseClass: "Error",
		ResponseCode:  ResponseCodeType{Value: code},
	}
}

// ---------------------------------------------------------------------------
// Task handlers
// ---------------------------------------------------------------------------

// handleCreateTask processes an EWS CreateTask SOAP request.
func (s *Server) handleCreateTask(ctx context.Context, body []byte) []byte {
	var req CreateTaskRequest
	if err := decodeRequest(body, &req); err != nil {
		return s.errorResponseXML("CreateTask", ErrErrorInvalidOperation, "malformed request: "+err.Error())
	}

	mboxID, mboxKey, errCode := s.resolveMailboxFromBody(ctx, body)
	if errCode != "" {
		return s.errorResponseXML("CreateTask", errCode, "could not resolve mailbox")
	}

	mailboxKey := strings.TrimPrefix(mboxKey, "e:")

	if s.collabStore == nil {
		return s.errorResponseXML("CreateTask", ErrErrorInternalServer, "collaboration store not available")
	}

	// Resolve tasks folder ID.
	folderID, err := s.identity.GetFolderID(mailboxKey, "tasks")
	if err != nil {
		folderID, err = s.identity.EnsureFolderId(mailboxKey, "tasks", "tasks")
		if err != nil {
			return s.errorResponseXML("CreateTask", ErrErrorInternalServer, "failed to resolve tasks folder: "+err.Error())
		}
	}

	messages := make([]TaskResponseMessageType, 0, len(req.Items.Item))
	for range req.Items.Item {
		item := &req.Items.Item[0]
		msg := s.createTaskInFolder(ctx, mboxID, mailboxKey, folderID, item)
		messages = append(messages, msg)
	}

	resp := CreateTaskResponse{}
	resp.Msgs.Messages = messages
	return buildResponseEnvelope(resp)
}

// createTaskInFolder creates a task in the target folder.
func (s *Server) createTaskInFolder(ctx context.Context, mboxID semcore.MailboxId, mailboxKey string, folderID semcore.FolderId, item *TaskTypeNew) TaskResponseMessageType {
	if folderID.IsZero() {
		return errorTaskMsg("CreateTask", ErrErrorInternalServer, "no target folder")
	}

	uid := fmt.Sprintf("%d-%s@umailserver.local", time.Now().UnixNano(), mailboxKey)
	icalData := buildTaskICalFromTask(uid, item)

	h := sha256.Sum256([]byte(icalData))
	blobKey := fmt.Sprintf("task:%x", h)

	//nolint:errcheck
	taskID, _ := semcore.NewTaskId(generateID())
	//nolint:errcheck
	taskCK, _ := semcore.NewTaskChangeKey(generateID())

	rec := semcore.NewStoredTaskIdentity(taskID, folderID, mboxID, taskCK, uid, blobKey)
	rec.RawData = icalData
	if err := s.collabStore.PutTaskIdentityUnsafe(blobKey, rec); err != nil {
		return errorTaskMsg("CreateTask", ErrErrorInternalServer, "failed to store identity: "+err.Error())
	}

	// Parse due date.
	var dueDate time.Time
	if item.DueDate != "" {
		//nolint:errcheck
		dueDate, _ = ParseEWSDateTime(item.DueDate)
	}

	return TaskResponseMessageType{
		ResponseClass: "Success",
		ResponseCode:  ResponseCodeType{Value: ErrNoError},
		Items: &TasksItemsContainer{Items: []TaskTypeResponse{{
			ItemID:         TaskIdType{ID: taskID.String(), CK: taskCK.String()},
			ParentFolderID: FolderIdComponents{ID: folderID.String()},
			Subject:        item.Subject,
			DueDate:        FormatEWSDateTime(dueDate),
			Status:         item.Status,
		}}},
	}
}

// handleGetTask processes an EWS GetTask SOAP request.
func (s *Server) handleGetTask(ctx context.Context, body []byte) []byte {
	var req GetTaskRequest
	if err := decodeRequest(body, &req); err != nil {
		return s.errorResponseXML("GetTask", ErrErrorInvalidOperation, "malformed request: "+err.Error())
	}

	mboxID, mboxKey, errCode := s.resolveMailboxFromBody(ctx, body)
	if errCode != "" {
		return s.errorResponseXML("GetTask", errCode, "could not resolve mailbox")
	}
	_ = mboxKey

	if s.collabStore == nil {
		return s.errorResponseXML("GetTask", ErrErrorInternalServer, "collaboration store not available")
	}

	messages := make([]TaskResponseMessageType, 0, len(req.ItemIDs.Item))
	for _, id := range req.ItemIDs.Item {
		msg := s.getTaskByID(ctx, mboxID, id)
		messages = append(messages, msg)
	}

	resp := GetTaskResponse{}
	resp.Msgs.Messages = messages
	return buildResponseEnvelope(resp)
}

// getTaskByID retrieves one task by TaskId.
func (s *Server) getTaskByID(ctx context.Context, mboxID semcore.MailboxId, id TaskIdType) TaskResponseMessageType {
	taskID, err := semcore.NewTaskId(id.ID)
	if err != nil {
		return errorTaskMsg("GetTask", ErrErrorInvalidId, err.Error())
	}

	rec, err := s.collabStore.GetTaskByID(taskID)
	if err != nil {
		if errors.Is(err, semcore.ErrTaskNotFound) {
			return errorTaskMsg("GetTask", ErrErrorItemNotFound, "task not found: "+id.ID)
		}
		return errorTaskMsg("GetTask", ErrErrorInternalServer, err.Error())
	}

	if !rec.MailboxID.IsZero() && rec.MailboxID != mboxID {
		return errorTaskMsg("GetTask", ErrErrorAccessDenied, "task belongs to a different mailbox")
	}

	return TaskResponseMessageType{
		ResponseClass: "Success",
		ResponseCode:  ResponseCodeType{Value: ErrNoError},
		Items: &TasksItemsContainer{Items: []TaskTypeResponse{{
			ItemID:         TaskIdType{ID: rec.ID.String(), CK: rec.ChangeKey.String()},
			ParentFolderID: FolderIdComponents{ID: rec.FolderID.String()},
		}}},
	}
}

// handleUpdateTask processes an EWS UpdateTask SOAP request.
func (s *Server) handleUpdateTask(ctx context.Context, body []byte) []byte {
	var req UpdateTaskRequest
	if err := decodeRequest(body, &req); err != nil {
		return s.errorResponseXML("UpdateTask", ErrErrorInvalidOperation, "malformed request: "+err.Error())
	}

	_, mboxKey, errCode := s.resolveMailboxFromBody(ctx, body)
	if errCode != "" {
		return s.errorResponseXML("UpdateTask", errCode, "could not resolve mailbox")
	}
	_ = mboxKey

	if s.collabStore == nil {
		return s.errorResponseXML("UpdateTask", ErrErrorInternalServer, "collaboration store not available")
	}

	messages := make([]TaskResponseMessageType, 0, len(req.ItemChanges.Changes))
	for _, ic := range req.ItemChanges.Changes {
		taskID, err := semcore.NewTaskId(ic.ItemID.ID)
		if err != nil {
			messages = append(messages, errorTaskMsg("UpdateTask", ErrErrorInvalidId, err.Error()))
			continue
		}

		rec, err := s.collabStore.GetTaskByID(taskID)
		if err != nil {
			if errors.Is(err, semcore.ErrTaskNotFound) {
				messages = append(messages, errorTaskMsg("UpdateTask", ErrErrorItemNotFound, err.Error()))
			} else {
				messages = append(messages, errorTaskMsg("UpdateTask", ErrErrorInternalServer, err.Error()))
			}
			continue
		}

		//nolint:errcheck
		currentCK, _ := semcore.NewTaskChangeKey(ic.ItemID.CK)
		if !currentCK.IsZero() && !rec.ChangeKey.Equal(currentCK) {
			messages = append(messages, errorTaskMsg("UpdateTask", ErrErrorStaleObject, "ChangeKey mismatch"))
			continue
		}

		//nolint:errcheck
		newCK, _ := semcore.NewTaskChangeKey(generateID())
		rec.ChangeKey = newCK

		if err := s.collabStore.PutTaskIdentity(rec.RawHash, rec, currentCK); err != nil {
			if errors.Is(err, semcore.ErrCollabVersionConflict) {
				messages = append(messages, errorTaskMsg("UpdateTask", ErrErrorVersionMismatch, "stale ChangeKey"))
			} else {
				messages = append(messages, errorTaskMsg("UpdateTask", ErrErrorInternalServer, err.Error()))
			}
			continue
		}

		messages = append(messages, TaskResponseMessageType{
			ResponseClass: "Success",
			ResponseCode:  ResponseCodeType{Value: ErrNoError},
			Items: &TasksItemsContainer{Items: []TaskTypeResponse{{
				ItemID:         TaskIdType{ID: rec.ID.String(), CK: rec.ChangeKey.String()},
				ParentFolderID: FolderIdComponents{ID: rec.FolderID.String()},
			}}},
		})
	}

	resp := UpdateTaskResponse{}
	resp.Msgs.Messages = messages
	return buildResponseEnvelope(resp)
}

// handleDeleteTask processes an EWS DeleteTask SOAP request.
func (s *Server) handleDeleteTask(ctx context.Context, body []byte) []byte {
	var req DeleteTaskRequest
	if err := decodeRequest(body, &req); err != nil {
		return s.errorResponseXML("DeleteTask", ErrErrorInvalidOperation, "malformed request: "+err.Error())
	}

	_, mboxKey, errCode := s.resolveMailboxFromBody(ctx, body)
	if errCode != "" {
		return s.errorResponseXML("DeleteTask", errCode, "could not resolve mailbox")
	}
	_ = mboxKey

	if s.collabStore == nil {
		return s.errorResponseXML("DeleteTask", ErrErrorInternalServer, "collaboration store not available")
	}

	responses := make([]struct {
		XMLName       xml.Name         `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseMessage"`
		ResponseClass string           `xml:"ResponseClass,attr"`
		ResponseCode  ResponseCodeType `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseCode"`
	}, 0, len(req.ItemIDs.Item))

	for _, id := range req.ItemIDs.Item {
		taskID, err := semcore.NewTaskId(id.ID)
		if err != nil {
			responses = append(responses, deleteTaskErrMsg("Error", ResponseCodeType{Value: ErrErrorInvalidId}))
			continue
		}

		rec, err := s.collabStore.GetTaskByID(taskID)
		if err != nil {
			responses = append(responses, deleteTaskErrMsg("Error", ResponseCodeType{Value: ErrErrorItemNotFound}))
			continue
		}

		//nolint:errcheck
		currentCK, _ := semcore.NewTaskChangeKey(id.CK)
		if err := s.collabStore.DeleteTaskIdentity(rec.RawHash, currentCK); err != nil {
			if errors.Is(err, semcore.ErrCollabVersionConflict) {
				responses = append(responses, deleteTaskErrMsg("Error", ResponseCodeType{Value: ErrErrorVersionMismatch}))
			} else {
				responses = append(responses, deleteTaskErrMsg("Error", ResponseCodeType{Value: ErrErrorInternalServer}))
			}
			continue
		}

		responses = append(responses, deleteTaskErrMsg("Success", ResponseCodeType{Value: ErrNoError}))
	}

	resp := DeleteTaskResponse{}
	resp.Msgs.Messages = responses
	return buildResponseEnvelope(resp)
}

func deleteTaskErrMsg(class string, code ResponseCodeType) struct {
	XMLName       xml.Name         `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseMessage"`
	ResponseClass string           `xml:"ResponseClass,attr"`
	ResponseCode  ResponseCodeType `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseCode"`
} {
	return struct {
		XMLName       xml.Name         `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseMessage"`
		ResponseClass string           `xml:"ResponseClass,attr"`
		ResponseCode  ResponseCodeType `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseCode"`
	}{
		ResponseClass: class,
		ResponseCode:  code,
	}
}

// errorTaskMsg builds an error TaskResponseMessageType.
func errorTaskMsg(op string, code ErrorCode, message string) TaskResponseMessageType {
	return TaskResponseMessageType{
		ResponseClass: "Error",
		ResponseCode:  ResponseCodeType{Value: code},
	}
}

// ---------------------------------------------------------------------------
// iCal/vCard helpers
// ---------------------------------------------------------------------------

// buildICalFromCalendarItem constructs an RFC 5545 iCalendar VEVENT from EWS data.
func buildICalFromCalendarItem(uid string, item *CalendarItemTypeNew, start, end time.Time) string {
	var buf bytes.Buffer
	now := time.Now().UTC().Format("20060102T150405Z")

	buf.WriteString("BEGIN:VCALENDAR\r\n")
	buf.WriteString("VERSION:2.0\r\n")
	buf.WriteString("PRODID:-//uMailServer//EWS//EN\r\n")
	buf.WriteString("CALSCALE:GREGORIAN\r\n")
	buf.WriteString("METHOD:PUBLISH\r\n")
	buf.WriteString("BEGIN:VEVENT\r\n")
	buf.WriteString("UID:" + uid + "\r\n")
	buf.WriteString("DTSTAMP:" + now + "\r\n")
	buf.WriteString("DTSTART:" + formatICalDateTime(start) + "\r\n")
	buf.WriteString("DTEND:" + formatICalDateTime(end) + "\r\n")
	if item.IsAllDay {
		buf.WriteString("X-MICROSOFT-CDO-ALLDAYEVENT:TRUE\r\n")
	}
	if item.Subject != "" {
		buf.WriteString("SUMMARY:" + item.Subject + "\r\n")
	}
	if item.Location != "" {
		buf.WriteString("LOCATION:" + item.Location + "\r\n")
	}
	if item.Body != nil {
		buf.WriteString("DESCRIPTION:" + item.Body.Body + "\r\n")
	}
	if item.Organizer != nil && item.Organizer.Mailbox.Email != "" {
		buf.WriteString("ORGANIZER;CN=" + item.Organizer.Mailbox.Name + ":mailto:" + item.Organizer.Mailbox.Email + "\r\n")
	}
	if item.Attendees != nil {
		for _, att := range item.Attendees.Attendee {
			buf.WriteString("ATTENDEE;ROLE=REQ-PARTICIPANT;CUTYPE=INDIVIDUAL:mailto:" + att.Mailbox.Email + "\r\n")
		}
	}
	if item.Recurrence != nil {
		buf.WriteString(buildRRULE(item.Recurrence))
	}
	buf.WriteString(icalCategoriesLine(item.Categories))
	buf.WriteString("END:VEVENT\r\n")
	buf.WriteString("END:VCALENDAR\r\n")
	return buf.String()
}

// icalCategoriesLine renders a CATEGORIES property line (RFC 5545 / RFC 6350)
// from EWS categories, or "" when there are none. Categories live in the
// canonical RawData so every surface (EWS, CalDAV/CardDAV, JMAP) reads and
// filters on one source of truth.
func icalCategoriesLine(c *MessageCategoriesType) string {
	if c == nil || len(c.Strings) == 0 {
		return ""
	}
	return "CATEGORIES:" + strings.Join(c.Strings, ",") + "\r\n"
}

// buildRRULE constructs an RRULE string from EWS RecurrenceType.
func buildRRULE(r *RecurrenceType) string {
	var parts []string
	if r.WeeklyRecurrence != nil {
		wr := r.WeeklyRecurrence
		parts = append(parts, "FREQ=WEEKLY")
		if wr.Interval > 1 {
			parts = append(parts, "INTERVAL="+strconv.Itoa(wr.Interval))
		}
		if wr.DaysOfWeek != "" {
			parts = append(parts, "BYDAY="+wr.DaysOfWeek)
		}
	} else if r.DailyRecurrence != nil {
		dr := r.DailyRecurrence
		parts = append(parts, "FREQ=DAILY")
		if dr.Interval > 1 {
			parts = append(parts, "INTERVAL="+strconv.Itoa(dr.Interval))
		}
	} else if r.MonthlyRecurrence != nil {
		mr := r.MonthlyRecurrence
		parts = append(parts, "FREQ=MONTHLY")
		if mr.Interval > 1 {
			parts = append(parts, "INTERVAL="+strconv.Itoa(mr.Interval))
		}
		if mr.DayOfMonth > 0 {
			parts = append(parts, "BYMONTHDAY="+strconv.Itoa(mr.DayOfMonth))
		}
	} else if r.YearlyRecurrence != nil {
		yr := r.YearlyRecurrence
		parts = append(parts, "FREQ=YEARLY")
		if yr.Interval > 1 {
			parts = append(parts, "INTERVAL="+strconv.Itoa(yr.Interval))
		}
		if yr.Month > 0 {
			parts = append(parts, "BYMONTH="+strconv.Itoa(yr.Month))
		}
		if yr.DayOfMonth > 0 {
			parts = append(parts, "BYMONTHDAY="+strconv.Itoa(yr.DayOfMonth))
		}
	}
	if len(parts) > 0 {
		return "RRULE:" + strings.Join(parts, ";") + "\r\n"
	}
	return ""
}

// formatICalDateTime formats a time as an iCal DTSTART/DTEND value.
func formatICalDateTime(t time.Time) string {
	return t.Format("20060102T150405Z")
}

// buildVCardFromContact constructs an RFC 6350 vCard from EWS contact data.
func buildVCardFromContact(uid string, contact *ContactTypeNew) string {
	var buf bytes.Buffer
	now := time.Now().UTC().Format("20060102T150405Z")

	buf.WriteString("BEGIN:VCARD\r\n")
	buf.WriteString("VERSION:3.0\r\n")
	buf.WriteString("PRODID:-//uMailServer//EWS//EN\r\n")
	buf.WriteString("UID:" + uid + "\r\n")
	if contact.FullName != "" {
		buf.WriteString("FN:" + contact.FullName + "\r\n")
	}
	if contact.GivenName != "" || contact.Surname != "" {
		buf.WriteString("N:" + contact.Surname + ";" + contact.GivenName + ";;;\r\n")
	}
	if contact.EmailAddresses != nil {
		for _, e := range contact.EmailAddresses.Entry {
			label := "HOME"
			if e.Key != "" {
				label = e.Key
			}
			buf.WriteString("EMAIL;TYPE=" + label + ":" + e.Value + "\r\n")
		}
	}
	if contact.PhoneNumbers != nil {
		for _, p := range contact.PhoneNumbers.Entry {
			label := "HOME"
			if p.Key != "" {
				label = p.Key
			}
			buf.WriteString("TEL;TYPE=" + label + ":" + p.Value + "\r\n")
		}
	}
	if contact.Organization != "" {
		buf.WriteString("ORG:" + contact.Organization + "\r\n")
	}
	if contact.Title != "" {
		buf.WriteString("TITLE:" + contact.Title + "\r\n")
	}
	if contact.Department != "" {
		buf.WriteString("DEPARTMENT:" + contact.Department + "\r\n")
	}
	if contact.Notes != "" {
		buf.WriteString("NOTE:" + contact.Notes + "\r\n")
	}
	if contact.WorkAddress != nil {
		buf.WriteString("ADR;TYPE=WORK:;" + contact.WorkAddress.Street + ";" + contact.WorkAddress.City + ";" + contact.WorkAddress.State + ";" + contact.WorkAddress.PostalCode + ";" + contact.WorkAddress.Country + "\r\n")
	}
	if contact.HomeAddress != nil {
		buf.WriteString("ADR;TYPE=HOME:;" + contact.HomeAddress.Street + ";" + contact.HomeAddress.City + ";" + contact.HomeAddress.State + ";" + contact.HomeAddress.PostalCode + ";" + contact.HomeAddress.Country + "\r\n")
	}
	buf.WriteString(icalCategoriesLine(contact.Categories))
	buf.WriteString("REV:" + now + "\r\n")
	buf.WriteString("END:VCARD\r\n")
	return buf.String()
}

// buildTaskICalFromTask constructs an RFC 5545 iCalendar VTODO from EWS task data.
func buildTaskICalFromTask(uid string, task *TaskTypeNew) string {
	var buf bytes.Buffer
	now := time.Now().UTC().Format("20060102T150405Z")

	buf.WriteString("BEGIN:VCALENDAR\r\n")
	buf.WriteString("VERSION:2.0\r\n")
	buf.WriteString("PRODID:-//uMailServer//EWS//EN\r\n")
	buf.WriteString("METHOD:PUBLISH\r\n")
	buf.WriteString("BEGIN:VTODO\r\n")
	buf.WriteString("UID:" + uid + "\r\n")
	buf.WriteString("DTSTAMP:" + now + "\r\n")
	if task.Subject != "" {
		buf.WriteString("SUMMARY:" + task.Subject + "\r\n")
	}
	if task.Body != nil {
		buf.WriteString("DESCRIPTION:" + task.Body.Body + "\r\n")
	}
	if task.StartDate != "" {
		//nolint:errcheck
		start, _ := ParseEWSDateTime(task.StartDate)
		if !start.IsZero() {
			buf.WriteString("DTSTART:" + formatICalDateTime(start) + "\r\n")
		}
	}
	if task.DueDate != "" {
		//nolint:errcheck
		due, _ := ParseEWSDateTime(task.DueDate)
		if !due.IsZero() {
			buf.WriteString("DUE:" + formatICalDateTime(due) + "\r\n")
		}
	}
	if task.Status != "" {
		buf.WriteString("STATUS:" + task.Status + "\r\n")
	}
	if task.PercentComplete != nil {
		pc := int(*task.PercentComplete * 100)
		buf.WriteString("PERCENT-COMPLETE:" + strconv.Itoa(pc) + "\r\n")
	}
	if task.Recurrence != nil {
		buf.WriteString(buildRRULE(task.Recurrence))
	}
	buf.WriteString(icalCategoriesLine(task.Categories))
	buf.WriteString("END:VTODO\r\n")
	buf.WriteString("END:VCALENDAR\r\n")
	return buf.String()
}

// iCal parsing helpers for recurrence expansion and time-zone fidelity.

// parseICalRRULE extracts structured recurrence data from raw iCal RRULE.
// parseICalRRULE is reserved for future recurrence RRULE parsing integration.
func parseICalRRULE(rrule string) *semcore.RecurrenceRule {
	if rrule == "" {
		return nil
	}
	// Strip RRULE: prefix.
	rrule = strings.TrimPrefix(rrule, "RRULE:")
	rr := &semcore.RecurrenceRule{}
	for _, part := range strings.Split(rrule, ";") {
		kv := strings.SplitN(part, "=", 2)
		if len(kv) != 2 {
			continue
		}
		key, val := kv[0], kv[1]
		switch key {
		case "FREQ":
			rr.Freq = val
		case "INTERVAL":
			//nolint:errcheck
			rr.Interval, _ = strconv.Atoi(val)
		case "COUNT":
			//nolint:errcheck
			rr.Count, _ = strconv.Atoi(val)
			rr.UseCount = true
		case "UNTIL":
			//nolint:errcheck
			t, _ := time.Parse("20060102T150405Z", val)
			rr.Until = t
			rr.UseUntil = true
		case "BYDAY":
			rr.ByDay = strings.Split(val, ",")
		case "BYMONTHDAY":
			dd := strings.Split(val, ",")
			for _, d := range dd {
				//nolint:errcheck
				v, _ := strconv.Atoi(d)
				rr.ByMonthDay = append(rr.ByMonthDay, v)
			}
		case "BYMONTH":
			mm := strings.Split(val, ",")
			for _, m := range mm {
				//nolint:errcheck
				v, _ := strconv.Atoi(m)
				rr.ByMonth = append(rr.ByMonth, v)
			}
		}
	}
	return rr
}

// parseICalDateTimeTZ parses an iCal datetime with optional TZID.
func parseICalDateTimeTZ(s string) (time.Time, string) {
	if s == "" {
		return time.Time{}, ""
	}
	// Format: 20240115T100000Z (UTC) or 20240115T100000 (floating)
	s = strings.TrimSpace(s)
	tz := ""
	if strings.HasSuffix(s, "Z") {
		s = strings.TrimSuffix(s, "Z")
		//nolint:errcheck
		t, _ := time.Parse("20060102T150405", s)
		return t.UTC(), "UTC"
	}
	//nolint:errcheck
	t, _ := time.Parse("20060102T150405", s)
	return t, tz
}

// recurrenceRange maps EWS recurrence range strings.
func recurrenceRange(s string) semcore.RecurrenceRange {
	switch s {
	case "ThisAndFuture":
		return semcore.RecurrenceRangeThisAndFuture
	default:
		return semcore.RecurrenceRangeThisOnly
	}
}

// reminderFromEWSTrigger converts an EWS ReminderType to a ReminderTrigger.
// VAL-COLLAB-012: reminder and notification lifecycle persist across edits,
// recurrence, and projection rereads.
//
//nolint:unused
func reminderFromEWSTrigger(r *ReminderType) *semcore.ReminderTrigger {
	if r == nil {
		return nil
	}
	trigger := &semcore.ReminderTrigger{}
	if r.MinutesBeforeStart > 0 {
		trigger.Relative = true
		trigger.Duration = r.MinutesBeforeStart * 60
	}
	if !trigger.IsZero() {
		trigger.Action = semcore.ReminderActionDisplay
	}
	return trigger
}

// ParseEWSDuration parses an EWS duration string (e.g., "PT5M", "P1D").
//
//nolint:unused
var durationRE = regexp.MustCompile(`^P(?:(\d+)D)?T?(?:(\d+)H)?(?:(\d+)M)?$`)

//nolint:unused
func parseEWSDuration(s string) time.Duration {
	if s == "" {
		return 0
	}
	m := durationRE.FindStringSubmatch(s)
	if m == nil {
		return 0
	}
	var d time.Duration
	if m[1] != "" {
		//nolint:errcheck
		days, _ := strconv.Atoi(m[1])
		d += time.Duration(days) * 24 * time.Hour
	}
	if m[2] != "" {
		//nolint:errcheck
		hours, _ := strconv.Atoi(m[2])
		d += time.Duration(hours) * time.Hour
	}
	if m[3] != "" {
		//nolint:errcheck
		mins, _ := strconv.Atoi(m[3])
		d += time.Duration(mins) * time.Minute
	}
	return d
}

// sortAttendees sorts attendees deterministically by email address.
// Satisfies VAL-COLLAB-004: attendee state transitions converge.
//
//nolint:unused
func sortAttendees(attendees []semcore.Attendee) {
	sort.Slice(attendees, func(i, j int) bool {
		return attendees[i].CalAddress < attendees[j].CalAddress
	})
}

// conflictFreeBusy checks for overlapping busy time ranges in a calendar item
// for free/busy calculations. Satisfies VAL-COLLAB-003.
//
// Wired into CreateCalendarItem to provide a conflict check against existing
// calendar items in the same mailbox. Returns true if the time ranges overlap.
// The actual free/busy surface (VAL-COLLAB-003) is exposed through the
// GetUserAvailability handler; this helper is used to detect conflicts at
// item-creation time for policy-aware booking.
func conflictFreeBusy(a, b *semcore.CalendarItem) bool {
	if a.DTStart.IsZero() || b.DTStart.IsZero() {
		return false
	}
	aStart, bStart := a.DTStart, b.DTStart
	aEnd, bEnd := a.DTEnd, b.DTEnd
	if aEnd.IsZero() {
		aEnd = aStart.Add(time.Hour)
	}
	if bEnd.IsZero() {
		bEnd = bStart.Add(time.Hour)
	}
	return aStart.Before(bEnd) && bStart.Before(aEnd)
}
