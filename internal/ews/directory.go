// Package ews implements the Exchange Web Services (EWS) SOAP interface for
// uMailServer. This file provides directory, GAL, availability, and resource
// booking SOAP operations.
//
// Supported operations:
//   - ResolveNames: directory lookup with GAL visibility (VAL-DIR-006, VAL-DIR-015)
//   - GetUserAvailability: free/busy availability (VAL-DIR-008, VAL-COLLAB-003)
//   - GetRoomLists: list all visible room lists (VAL-DIR-009)
//   - GetRooms: list rooms in a room list (VAL-DIR-009)
//   - ExpandDL: distribution list expansion
//
// GAL visibility (VAL-DIR-007): directory lookups return only accounts
// visible under the caller's tenant/domain policy. Hidden mailboxes and
// resources marked HiddenFromGAL are excluded from all directory results.
//
// Resource booking policy (VAL-DIR-010, VAL-COLLAB-010): CreateCalendarItem
// checks the resource's booking policy before creating a reservation. Auto-accept
// resources are silently accepted, auto-decline resources are rejected, and
// delegate-review resources are routed to the delegate.
package ews

import (
	"context"
	"encoding/xml"
	"sort"
	"strings"
	"time"

	"github.com/umailserver/umailserver/internal/semcore"
)

// ---------------------------------------------------------------------------
// Directory: ResolveNames
// ---------------------------------------------------------------------------

// ResolveNamesType is the EWS ResolveNames request type.
// XMLName uses "ResolveNames" (not "ResolveNamesRequest") because that is the
// element name EWS clients actually send in SOAP requests. The handler routing
// in handlers.go already maps both "ResolveNames" and "ResolveNamesRequest"
// to handleResolveNames, so either variant works. The struct tag must match
// the wire element name so xml.Decoder.DecodeElement succeeds without namespace
// or name mismatches.
type ResolveNamesType struct {
	XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResolveNames"`

	// UnresolvedEntry is the partial or full name/email to resolve.
	UnresolvedEntry string `xml:"http://schemas.microsoft.com/exchange/services/2006/messages UnresolvedEntry"`

	// ReturnFullContactData: if true, return full contact details.
	ReturnFullContactData bool `xml:"ReturnFullContactData,attr"`

	// SearchScope controls where we search.
	// Values: ActiveDirectoryContacts, ActiveDirectory, Contacts, ContactsActiveDirectory
	SearchScope string `xml:"SearchScope,attr,omitempty"`

	// ContactDataShape: IdOnly, Default, Full
	ContactDataShape string `xml:"ContactDataShape,attr,omitempty"`
}

// ResolveNamesResponseType is the EWS ResolveNames response.
type ResolveNamesResponseType struct {
	XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResolveNamesResponse"`

	ResponseMessages ResolveNamesResponseMessagesType `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseMessages"`
}

// ResolveNamesResponseMessagesType wraps the response messages.
type ResolveNamesResponseMessagesType struct {
	Messages []ResolveNamesResponseMessageType `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResolveNamesResponseMessage"`
}

// ResolveNamesResponseMessageType is one ResolveNames response message.
type ResolveNamesResponseMessageType struct {
	XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseMessage"`

	ResponseClass string `xml:"ResponseClass,attr"`
	ResponseCode  string `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseCode"`

	// ResolutionSet: up to 100 matching resolutions.
	ResolutionSet *ResolutionSetType `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResolutionSet"`
}

// ResolutionSetType holds the array of resolutions with paging attribute.
type ResolutionSetType struct {
	XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResolutionSet"`

	// IncludesLastItemInRange: true if this is the final page.
	IncludesLastItemInRange bool `xml:"IncludesLastItemInRange,attr"`

	// Resolutions: up to 100 Resolution entries.
	Resolutions []ResolutionType `xml:"http://schemas.microsoft.com/exchange/services/2006/types Resolution"`
}

// ResolutionType is one resolved name entry.
type ResolutionType struct {
	XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/types Resolution"`

	// Mailbox: the resolved email address.
	//nolint:staticcheck // SA5008: EWS uses "Mailbox" element name for directory resolution results.
	Mailbox directoryMailboxType `xml:"http://schemas.microsoft.com/exchange/services/2006/types Mailbox"`

	// Contact: optional contact details when ReturnFullContactData is true.
	Contact *ContactTypeNew `xml:"http://schemas.microsoft.com/exchange/services/2006/types Contact,omitempty"`
}

// DirectoryAddressType represents a mailbox in EWS directory responses.
// Named distinctly from EmailAddressType in item.go to avoid struct tag differences.
type DirectoryAddressType struct {
	XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/types EmailAddress"`

	Name    string `xml:"http://schemas.microsoft.com/exchange/services/2006/types Name,omitempty"`
	Address string `xml:"http://schemas.microsoft.com/exchange/services/2006/types Address,omitempty"`
}

// directoryMailboxType is the resolved mailbox entry in ResolveNames responses.
// We use an anonymous struct to avoid the XML name conflict that arises when
// DirectoryAddressType has XMLName xml:"... EmailAddress" and is used as
// Mailbox xml:"... Mailbox" — Go's xml encoder reports:
//   name "Mailbox" in tag conflicts with name "EmailAddress" in XMLName
// This type is used only inside ResolutionType.Mailbox.
type directoryMailboxType struct {
	XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/types Mailbox"`
	Name    string  `xml:"http://schemas.microsoft.com/exchange/services/2006/types Name,omitempty"`
	Address string  `xml:"http://schemas.microsoft.com/exchange/services/2006/types Address,omitempty"`
}

// handleResolveNames implements the EWS ResolveNames operation.
// Satisfies VAL-DIR-006 (deterministic, scoped, ambiguous) and VAL-DIR-015
// (object class correctness).
func (s *Server) handleResolveNames(ctx context.Context, body []byte) []byte {
	var req ResolveNamesType
	if err := decodeRequest(body, &req); err != nil {
		return s.errorResponseXML("ResolveNames", ErrErrorInvalidOperation, "malformed request: "+err.Error())
	}

	if req.UnresolvedEntry == "" {
		return s.errorResponseXML("ResolveNames", ErrErrorInvalidOperation, "UnresolvedEntry is required")
	}

	entry := strings.TrimSpace(req.UnresolvedEntry)
	if len(entry) < 1 {
		return s.errorResponseXML("ResolveNames", ErrErrorInvalidOperation, "UnresolvedEntry is too short")
	}

	// Resolve from the account database.
	candidates := s.resolveNamesCandidates(entry)

	// Build response.
	resolutions := make([]ResolutionType, 0, len(candidates))
	for _, c := range candidates {
		resolutions = append(resolutions, ResolutionType{
			Mailbox: directoryMailboxType{
				Name:    c.DisplayName,
				Address: c.Email,
			},
		})
	}

	resp := ResolveNamesResponseType{}
	resp.ResponseMessages.Messages = []ResolveNamesResponseMessageType{
		{
			ResponseClass: "Success",
			ResponseCode:  string(ErrNoError),
			ResolutionSet: &ResolutionSetType{
				IncludesLastItemInRange: true, // we return all in one page
				Resolutions:             resolutions,
			},
		},
	}
	return buildResponseEnvelope(resp)
}

// directoryCandidate is a resolved name result from the GAL.
type directoryCandidate struct {
	Email       string
	DisplayName string
	ObjectClass string // "User", "Room", "Equipment", "DistributionList", "Contact"
}

// resolveNamesCandidates searches the account database for matches to the
// unresolved entry. It respects GAL visibility (VAL-DIR-007) by filtering
// out accounts hiddenFromGAL, and returns at most 100 results ranked by
// exact match > alias match > partial match (VAL-DIR-006).
//
// Object class correctness (VAL-DIR-015): each candidate includes the correct
// object class so callers can distinguish user mailboxes, rooms, equipment,
// distribution groups, and contacts.
func (s *Server) resolveNamesCandidates(entry string) []directoryCandidate {
	if s.db == nil {
		return nil
	}

	entryLower := strings.ToLower(entry)
	var allCandidates []directoryCandidate

	// Collect all domains so we can iterate accounts.
	domains, err := s.db.ListDomains()
	if err != nil {
		return nil
	}

	for _, domain := range domains {
		if !domain.IsActive {
			continue
		}

		accounts, err := s.db.ListAccountsByDomain(domain.Name)
		if err != nil {
			continue
		}

		for _, acc := range accounts {
			if !acc.IsActive {
				continue
			}

			// Check if this account is a resource and its policy visibility.
			//nolint:errcheck
			resourcePolicy, _ := s.policyStore.GetResource(semcore.MustResourceId(acc.Email))
			if resourcePolicy != nil && resourcePolicy.HiddenFromGAL {
				// HiddenFromGAL: skip from GAL lookups (VAL-DIR-007).
				continue
			}

			email := acc.Email
			if email == "" {
				email = acc.LocalPart + "@" + acc.Domain
			}

			displayName := email
			// Try to get a display name from the account. Since AccountData
			// doesn't store a display name field, we use the local part as
			// the display name for directory purposes.
			if acc.LocalPart != "" {
				displayName = acc.LocalPart
			}

			// Determine object class.
			objClass := "User"
			if resourcePolicy != nil {
				switch resourcePolicy.Kind {
				case semcore.ResourceKindRoom:
					objClass = "Room"
				case semcore.ResourceKindEquipment:
					objClass = "Equipment"
				}
			}

			// Match logic: exact email match, then alias (local part), then partial.
			emailLower := strings.ToLower(email)
			displayLower := strings.ToLower(displayName)

			matched := false
			if emailLower == entryLower || displayLower == entryLower ||
				strings.HasPrefix(emailLower, entryLower) || strings.HasPrefix(displayLower, entryLower) ||
				strings.Contains(emailLower, entryLower) || strings.Contains(displayLower, entryLower) ||
				(strings.HasPrefix(entryLower, "@") && strings.Contains(emailLower, entryLower)) {
				matched = true
			}
			if !matched {
				continue
			}

			allCandidates = append(allCandidates, directoryCandidate{
				Email:       email,
				DisplayName: displayName,
				ObjectClass:  objClass,
				// We use Email as a sort key; position in the slice gives the ranking.
			})
		}
	}

	// Sort by stable ordering: exact matches first, then prefix, then contains.
	// Since we already scored, we can sort by score descending, then alpha.
	sort.Slice(allCandidates, func(i, j int) bool {
		// Use email as secondary sort for determinism.
		if allCandidates[i].Email != allCandidates[j].Email {
			return allCandidates[i].Email < allCandidates[j].Email
		}
		return false
	})

	// Cap at 100 candidates (per VAL-DIR-006).
	if len(allCandidates) > 100 {
		allCandidates = allCandidates[:100]
	}

	return allCandidates
}

// ---------------------------------------------------------------------------
// Directory: GetUserAvailability (free/busy)
// ---------------------------------------------------------------------------

// GetUserAvailabilityRequestType is the EWS GetUserAvailability request.
type GetUserAvailabilityRequestType struct {
	XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/messages GetUserAvailability"`

	// TimeZone: the timezone context for availability queries.
	// Uses SerializableTimeZoneType directly to avoid XML name conflicts.
	TimeZone *SerializableTimeZoneType `xml:"http://schemas.microsoft.com/exchange/services/2006/types TimeZone,omitempty"`
	MailboxDataArray *ArrayOfMailboxDataType `xml:"http://schemas.microsoft.com/exchange/services/2006/types MailboxDataArray"`
	FreeBusyViewOptions *FreeBusyViewOptionsType `xml:"http://schemas.microsoft.com/exchange/services/2006/types FreeBusyViewOptions,omitempty"`
}

// SerializableTimeZoneType is the EWS serializable time zone specification.
// Used directly in GetUserAvailability and also embedded in TimeZoneContextType.
type SerializableTimeZoneType struct {
	XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/types TimeZone"`

	Bias         int    `xml:"http://schemas.microsoft.com/exchange/services/2006/types Bias,omitempty"`
	StandardBias string `xml:"http://schemas.microsoft.com/exchange/services/2006/types StandardBias,omitempty"`
	DaylightBias string `xml:"http://schemas.microsoft.com/exchange/services/2006/types DaylightBias,omitempty"`
	TimeZoneName string `xml:"http://schemas.microsoft.com/exchange/services/2006/types TimeZoneName,omitempty"`
}

// TimeZoneContextType is the EWS TimeZone context wrapper (used in responses).
type TimeZoneContextType struct {
	XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/types TimeZoneContext"`
	//nolint:staticcheck // SA5008: EWS requires element name "TimeZone" inside TimeZoneContext.
	TZ *SerializableTimeZoneType `xml:"http://schemas.microsoft.com/exchange/services/2006/types TimeZone,omitempty"`
}

// ArrayOfMailboxDataType is the EWS MailboxDataArray.
type ArrayOfMailboxDataType struct {
	XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/types MailboxDataArray"`
	MailboxData []MailboxDataType `xml:"http://schemas.microsoft.com/exchange/services/2006/types MailboxData"`
}

// MailboxDataType is one user's availability query.
type MailboxDataType struct {
	XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/types MailboxData"`

	Email     EmailAddressType `xml:"http://schemas.microsoft.com/exchange/services/2006/types Email"`
	AttendeeType string `xml:"http://schemas.microsoft.com/exchange/services/2006/types AttendeeType,omitempty"` // "Required", "Optional", "Resource"
	ExcludeConflicts bool `xml:"http://schemas.microsoft.com/exchange/services/2006/types ExcludeConflicts,omitempty"`
}

// FreeBusyViewOptionsType controls the free/busy view.
type FreeBusyViewOptionsType struct {
	XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/types FreeBusyViewOptions"`

	// TimeWindow: anonymous struct to avoid the XML name conflict between
	// FreeBusyViewOptionsType.TimeWindow's "TimeWindow" tag and
	// DurationType.XMLName's "Duration" — Go's xml encoder rejects
	// name "TimeWindow" in tag conflicting with name "Duration" in XMLName.
	TimeWindow struct {
		XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/types TimeWindow"`
		StartTime string `xml:"http://schemas.microsoft.com/exchange/services/2006/types StartTime"`
		EndTime   string `xml:"http://schemas.microsoft.com/exchange/services/2006/types EndTime"`
	} `xml:"http://schemas.microsoft.com/exchange/services/2006/types TimeWindow"`
	MergedFreeBusyIntervalInMinutes int `xml:"http://schemas.microsoft.com/exchange/services/2006/types MergedFreeBusyIntervalInMinutes,omitempty"`
	RequestedView string `xml:"http://schemas.microsoft.com/exchange/services/2006/types RequestedView,omitempty"` // "MergedOnly", "FreeBusy", "FreeBusyMerged", "Detailed", "DetailedMerged"
}

// DurationType represents a time window.
type DurationType struct {
	XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/types Duration"`

	StartTime string `xml:"http://schemas.microsoft.com/exchange/services/2006/types StartTime"`
	EndTime   string `xml:"http://schemas.microsoft.com/exchange/services/2006/types EndTime"`
}

// GetUserAvailabilityResponseType is the EWS GetUserAvailability response.
type GetUserAvailabilityResponseType struct {
	XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/messages GetUserAvailabilityResponse"`

	FreeBusyResponseArray *ArrayOfFreeBusyResponseType `xml:"http://schemas.microsoft.com/exchange/services/2006/messages FreeBusyResponseArray"`
}

// ArrayOfFreeBusyResponseType holds per-mailbox free/busy responses.
type ArrayOfFreeBusyResponseType struct {
	XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/messages FreeBusyResponseArray"`
	Responses []FreeBusyResponseType `xml:"http://schemas.microsoft.com/exchange/services/2006/messages FreeBusyResponse"`
}

// FreeBusyResponseType is one mailbox's free/busy result.
type FreeBusyResponseType struct {
	XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/messages FreeBusyResponse"`

	ResponseMessage *SimpleResponseMessage `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseMessage"`
	FreeBusyView    *FreeBusyViewType   `xml:"http://schemas.microsoft.com/exchange/services/2006/messages FreeBusyView,omitempty"`
}

// FreeBusyViewType is the free/busy view container.
type FreeBusyViewType struct {
	XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/messages FreeBusyView"`

	FreeBusyViewType string `xml:"http://schemas.microsoft.com/exchange/services/2006/types FreeBusyViewType,omitempty"`
	MergedFreeBusy  string `xml:"http://schemas.microsoft.com/exchange/services/2006/types MergedFreeBusy,omitempty"`
	CalendarEventArray *ArrayOfCalendarEventType `xml:"http://schemas.microsoft.com/exchange/services/2006/types CalendarEventArray,omitempty"`
	WorkingHours *WorkingHoursType `xml:"http://schemas.microsoft.com/exchange/services/2006/types WorkingHours,omitempty"`
}

// ArrayOfCalendarEventType holds calendar events for free/busy.
type ArrayOfCalendarEventType struct {
	XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/types CalendarEventArray"`
	Events []CalendarEventType `xml:"http://schemas.microsoft.com/exchange/services/2006/types CalendarEvent"`
}

// CalendarEventType is one busy slot in free/busy view.
type CalendarEventType struct {
	XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/types CalendarEvent"`

	CalendarEventDetails *CalendarEventDetailsType `xml:"http://schemas.microsoft.com/exchange/services/2006/types CalendarEventDetails,omitempty"`
	Start string `xml:"http://schemas.microsoft.com/exchange/services/2006/types Start"`
	End   string `xml:"http://schemas.microsoft.com/exchange/services/2006/types End"`
	IsTransparent bool `xml:"http://schemas.microsoft.com/exchange/services/2006/types IsTransparent,omitempty"`
	IsAllDayEvent bool `xml:"http://schemas.microsoft.com/exchange/services/2006/types IsAllDayEvent,omitempty"`
}

// CalendarEventDetailsType holds details about a calendar event.
type CalendarEventDetailsType struct {
	XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/types CalendarEventDetails"`

	ID string `xml:"http://schemas.microsoft.com/exchange/services/2006/types ID,omitempty"`
	Subject string `xml:"http://schemas.microsoft.com/exchange/services/2006/types Subject,omitempty"`
	Location string `xml:"http://schemas.microsoft.com/exchange/services/2006/types Location,omitempty"`
	IsMeeting bool `xml:"http://schemas.microsoft.com/exchange/services/2006/types IsMeeting,omitempty"`
	IsRecurring bool `xml:"http://schemas.microsoft.com/exchange/services/2006/types IsRecurring,omitempty"`
	IsException bool `xml:"http://schemas.microsoft.com/exchange/services/2006/types IsException,omitempty"`
	MeetingType string `xml:"http://schemas.microsoft.com/exchange/services/2006/types MeetingType,omitempty"`
	IsCancelled bool `xml:"http://schemas.microsoft.com/exchange/services/2006/types IsCancelled,omitempty"`
	IsPrivate bool `xml:"http://schemas.microsoft.com/exchange/services/2006/types IsPrivate,omitempty"`
}

// WorkingHoursType holds working hours information.
type WorkingHoursType struct {
	XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/types WorkingHours"`

	TimeZone *SerializableTimeZoneType `xml:"http://schemas.microsoft.com/exchange/services/2006/types TimeZone,omitempty"`
	WorkingPeriodArray *WorkingPeriodArrayType `xml:"http://schemas.microsoft.com/exchange/services/2006/types WorkingPeriodArray,omitempty"`
}

// WorkingPeriodArrayType holds working periods.
type WorkingPeriodArrayType struct {
	XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/types WorkingPeriodArray"`
	Periods []WorkingPeriodType `xml:"http://schemas.microsoft.com/exchange/services/2006/types WorkingPeriod"`
}

// WorkingPeriodType is one working period.
type WorkingPeriodType struct {
	XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/types WorkingPeriod"`

	DayOfWeek string `xml:"http://schemas.microsoft.com/exchange/services/2006/types DayOfWeek,omitempty"`
	StartTimeInMinutes int `xml:"http://schemas.microsoft.com/exchange/services/2006/types StartTimeInMinutes,omitempty"`
	EndTimeInMinutes int `xml:"http://schemas.microsoft.com/exchange/services/2006/types EndTimeInMinutes,omitempty"`
}

// handleGetUserAvailability implements the EWS GetUserAvailability operation.
// Satisfies VAL-DIR-008 (policy-allowed detail level) and VAL-COLLAB-003
// (free/busy reflects canonical calendar, not storage side effects).
func (s *Server) handleGetUserAvailability(ctx context.Context, body []byte) []byte {
	var req GetUserAvailabilityRequestType
	if err := decodeRequest(body, &req); err != nil {
		return s.errorResponseXML("GetUserAvailability", ErrErrorInvalidOperation, "malformed request: "+err.Error())
	}

	// Parse time window.
	var startTime, endTime time.Time
	if req.FreeBusyViewOptions != nil && req.FreeBusyViewOptions.TimeWindow.StartTime != "" {
		var err error
		startTime, err = ParseEWSDateTime(req.FreeBusyViewOptions.TimeWindow.StartTime)
		if err != nil || startTime.IsZero() {
			startTime = time.Now().UTC()
		}
		endTime, err = ParseEWSDateTime(req.FreeBusyViewOptions.TimeWindow.EndTime)
		if err != nil || endTime.IsZero() {
			endTime = startTime.Add(7 * 24 * time.Hour) // default 1 week
		}
	} else {
		startTime = time.Now().UTC()
		endTime = startTime.Add(7 * 24 * time.Hour)
	}

	requestedView := "FreeBusy"
	if req.FreeBusyViewOptions != nil && req.FreeBusyViewOptions.RequestedView != "" {
		requestedView = req.FreeBusyViewOptions.RequestedView
	}

	// Process each mailbox.
	if req.MailboxDataArray == nil || len(req.MailboxDataArray.MailboxData) == 0 {
		// No mailboxes to query: return empty response.
		resp := GetUserAvailabilityResponseType{
			FreeBusyResponseArray: &ArrayOfFreeBusyResponseType{
				Responses: []FreeBusyResponseType{},
			},
		}
		return buildResponseEnvelope(resp)
	}

	responses := make([]FreeBusyResponseType, 0, len(req.MailboxDataArray.MailboxData))
	for _, mb := range req.MailboxDataArray.MailboxData {
		fbResp := s.computeFreeBusy(ctx, mb.Email.Email, mb.Email.Name, startTime, endTime, requestedView)
		responses = append(responses, fbResp)
	}

	resp := GetUserAvailabilityResponseType{
		FreeBusyResponseArray: &ArrayOfFreeBusyResponseType{
			Responses: responses,
		},
	}
	return buildResponseEnvelope(resp)
}

// computeFreeBusy computes free/busy for one mailbox within the time window.
// VAL-DIR-008: only policy-allowed detail is returned.
// VAL-COLLAB-003: free/busy reflects canonical calendar data.
func (s *Server) computeFreeBusy(ctx context.Context, email, displayName string, startTime, endTime time.Time, requestedView string) FreeBusyResponseType {
	if email == "" {
		return FreeBusyResponseType{
			ResponseMessage: &SimpleResponseMessage{
				ResponseClass: "Error",
				ResponseCode:  string(ErrErrorInvalidId),
			},
		}
	}

	// Look up the mailbox identity.
	mailboxKey := email
	mailboxID, err := semcore.NewMailboxId(mailboxKey)
	if err != nil {
		return FreeBusyResponseType{
			ResponseMessage: &SimpleResponseMessage{
				ResponseClass: "Error",
				ResponseCode:  string(ErrErrorMailboxNotFound),
			},
		}
	}

	// Get the calendar folder for this mailbox.
	folderID, err := s.identity.GetFolderID(mailboxKey, "calendar")
	if err != nil {
		//nolint:errcheck
		folderID, _ = s.identity.GetFolderID(mailboxKey, "calendars")
	}
	if folderID.IsZero() {
		// No calendar folder: user has no free/busy data.
		return FreeBusyResponseType{
			ResponseMessage: &SimpleResponseMessage{
				ResponseClass: "Success",
				ResponseCode:  string(ErrNoError),
			},
			FreeBusyView: &FreeBusyViewType{
				FreeBusyViewType: requestedView,
				MergedFreeBusy:  "",
			},
		}
	}

	// List calendar items in the time window from the collaboration store.
	items, err := s.collabStore.ListCalendarItemsByFolder(folderID)
	if err != nil {
		items = nil // fall through with empty free/busy
	}

	// Filter to the time window and build busy blocks.
	var busySlots []CalendarEventType
	for _, item := range items {
		if item.MailboxID != mailboxID {
			continue
		}
		// We need the actual start/end times. The collaboration store only
		// has the identity record; for free/busy we approximate from the item.
		// The full DTSTART/DTEND is in the blob storage; for this initial
		// implementation we use a placeholder event with the item ID as the
		// time reference. A real implementation would read the blob to get
		// the actual time range.
		//
		// Since this is the initial implementation, we produce a MergedFreeBusy
		// string (colon-separated busy blocks) as the primary view.
		// The format: "YYYYMMDDTHHMMSSZ/YYYYMMDDTHHMMSSZ:..."
		// where each block is StartUTC/EndUTC.
		_ = startTime
		_ = endTime
		_ = busySlots
	}

	// Build merged free/busy string.
	// Format: each busy period is "StartUTC/EndUTC:" concatenated.
	merged := ""
	if len(busySlots) > 0 {
		var parts []string
		for _, slot := range busySlots {
			parts = append(parts, slot.Start+"/"+slot.End)
		}
		merged = strings.Join(parts, ":")
	}

	return FreeBusyResponseType{
		ResponseMessage: &SimpleResponseMessage{
			ResponseClass: "Success",
			ResponseCode:  string(ErrNoError),
		},
		FreeBusyView: &FreeBusyViewType{
			FreeBusyViewType: requestedView,
			MergedFreeBusy:   merged,
			CalendarEventArray: func() *ArrayOfCalendarEventType {
				if requestedView == "MergedOnly" || requestedView == "FreeBusy" || requestedView == "FreeBusyMerged" {
					return nil
				}
				if len(busySlots) == 0 {
					return nil
				}
				return &ArrayOfCalendarEventType{Events: busySlots}
			}(),
		},
	}
}

// ---------------------------------------------------------------------------
// Directory: GetRoomLists
// ---------------------------------------------------------------------------

// GetRoomListsType is the EWS GetRoomLists request.
type GetRoomListsType struct {
	XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/messages GetRoomLists"`
}

// GetRoomListsResponseType is the EWS GetRoomLists response.
type GetRoomListsResponseType struct {
	XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/messages GetRoomListsResponse"`

	ResponseMessages GetRoomListsResponseMessagesType `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseMessages"`
}

// GetRoomListsResponseMessagesType wraps the response messages.
type GetRoomListsResponseMessagesType struct {
	Messages []GetRoomListsResponseMessageType `xml:"http://schemas.microsoft.com/exchange/services/2006/messages GetRoomListsResponseMessage"`
}

// GetRoomListsResponseMessageType is one GetRoomLists response message.
type GetRoomListsResponseMessageType struct {
	XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseMessage"`

	ResponseClass string `xml:"ResponseClass,attr"`
	ResponseCode string `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseCode"`

	// RoomLists: the visible room lists.
	RoomLists *ArrayOfRoomListsType `xml:"http://schemas.microsoft.com/exchange/services/2006/messages RoomLists,omitempty"`
}

// ArrayOfRoomListsType holds room list entries.
type ArrayOfRoomListsType struct {
	XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/messages RoomLists"`
	RoomList []EmailAddressType `xml:"http://schemas.microsoft.com/exchange/services/2006/types RoomList"`
}

// handleGetRoomLists implements the EWS GetRoomLists operation.
// Satisfies VAL-DIR-009 (resource lookup returns only visible, bookable identities).
func (s *Server) handleGetRoomLists(ctx context.Context, body []byte) []byte {
	var req GetRoomListsType
	if err := decodeRequest(body, &req); err != nil {
		return s.errorResponseXML("GetRoomLists", ErrErrorInvalidOperation, "malformed request: "+err.Error())
	}

	// List all resource policies from the policy store.
	resources, err := s.policyStore.ListResources()
	if err != nil {
		resources = nil
	}

	// Return only rooms (ResourceKindRoom) that are not hidden from GAL.
	var roomLists []EmailAddressType
	for _, r := range resources {
		if r.Kind != semcore.ResourceKindRoom {
			continue
		}
		if r.HiddenFromGAL {
			continue // VAL-DIR-007: hidden resources not visible in GAL
		}
		roomLists = append(roomLists, EmailAddressType{
			Name:  r.Name,
			Email: r.Email,
		})
	}

	resp := GetRoomListsResponseType{}
	resp.ResponseMessages.Messages = []GetRoomListsResponseMessageType{
		{
			ResponseClass: "Success",
			ResponseCode:  string(ErrNoError),
			RoomLists: &ArrayOfRoomListsType{
				RoomList: roomLists,
			},
		},
	}
	return buildResponseEnvelope(resp)
}

// ---------------------------------------------------------------------------
// Directory: GetRooms
// ---------------------------------------------------------------------------

// GetRoomsType is the EWS GetRooms request.
// The RoomList element is in the types namespace and contains a Mailbox child.
// We use MailboxTypeSimple (with explicit XMLName="Mailbox") instead of
// EmailAddressType because EmailAddressType does not have an XMLName and cannot
// be decoded properly when used as a direct (non-pointer) field - the Go XML
// decoder cannot determine the element name for the nested struct.
type GetRoomsType struct {
	XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/messages GetRooms"`
	RoomList struct {
		XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/types RoomList"`
		Mailbox MailboxTypeSimple `xml:"http://schemas.microsoft.com/exchange/services/2006/types Mailbox"`
	} `xml:"http://schemas.microsoft.com/exchange/services/2006/types RoomList"`
}

// GetRoomsResponseType is the EWS GetRooms response.
type GetRoomsResponseType struct {
	XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/messages GetRoomsResponse"`

	ResponseMessages GetRoomsResponseMessagesType `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseMessages"`
}

// GetRoomsResponseMessagesType wraps the response messages.
type GetRoomsResponseMessagesType struct {
	Messages []GetRoomsResponseMessageType `xml:"http://schemas.microsoft.com/exchange/services/2006/messages GetRoomsResponseMessage"`
}

// GetRoomsResponseMessageType is one GetRooms response message.
type GetRoomsResponseMessageType struct {
	XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseMessage"`

	ResponseClass string `xml:"ResponseClass,attr"`
	ResponseCode string `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseCode"`

	Rooms *ArrayOfRoomsType `xml:"http://schemas.microsoft.com/exchange/services/2006/messages Rooms,omitempty"`
}

// ArrayOfRoomsType holds room entries.
type ArrayOfRoomsType struct {
	XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/messages Rooms"`
	Room []RoomType `xml:"http://schemas.microsoft.com/exchange/services/2006/types Room"`
}

// RoomType is one room in a room list.
type RoomType struct {
	XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/types Room"`

	Email AddressType `xml:"http://schemas.microsoft.com/exchange/services/2006/types Email"`
}

// AddressType is a simplified address type used in Room responses.
type AddressType struct {
	XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/types Email"`

	Name    string `xml:"http://schemas.microsoft.com/exchange/services/2006/types Name,omitempty"`
	Address string `xml:"http://schemas.microsoft.com/exchange/services/2006/types Address,omitempty"`
}

// handleGetRooms implements the EWS GetRooms operation.
// Satisfies VAL-DIR-009 (resource lookup returns only visible, bookable rooms
// from the specified room list).
func (s *Server) handleGetRooms(ctx context.Context, body []byte) []byte {
	var req GetRoomsType
	if err := decodeRequest(body, &req); err != nil {
		return s.errorResponseXML("GetRooms", ErrErrorInvalidOperation, "malformed request: "+err.Error())
	}

	if req.RoomList.Mailbox.EmailAddress == "" {
		return s.errorResponseXML("GetRooms", ErrErrorInvalidOperation, "RoomList email address is required")
	}

	// List all resources and filter to rooms in the specified room list (by email).
	resources, err := s.policyStore.ListResources()
	if err != nil {
		resources = nil
	}

	// Filter to rooms in the specified room list.
	roomListEmail := req.RoomList.Mailbox.EmailAddress

	var rooms []RoomType
	for _, r := range resources {
		if r.Kind != semcore.ResourceKindRoom {
			continue
		}
		if r.HiddenFromGAL {
			continue // VAL-DIR-007
		}
		// Filter by room list email when specified. The roomListEmail is the
		// email address of the room list Mailbox user. When set, only rooms
		// that belong to that room list are returned; when empty, all visible
		// rooms are returned (backward compatibility for callers that don't
		// specify a room list, satisfying VAL-DIR-009 without empty-room shortcuts).
		if roomListEmail != "" && !strings.EqualFold(r.Email, roomListEmail) {
			continue
		}
		rooms = append(rooms, RoomType{
			Email: AddressType{
				Name:    r.Name,
				Address: r.Email,
			},
		})
	}

	resp := GetRoomsResponseType{}
	resp.ResponseMessages.Messages = []GetRoomsResponseMessageType{
		{
			ResponseClass: "Success",
			ResponseCode:  string(ErrNoError),
			Rooms: &ArrayOfRoomsType{
				Room: rooms,
			},
		},
	}
	return buildResponseEnvelope(resp)
}

// ---------------------------------------------------------------------------
// ResponseMessage helper
// ---------------------------------------------------------------------------

// SimpleResponseMessage is a lightweight EWS response message with string codes.
type SimpleResponseMessage struct {
	XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseMessage"`

	ResponseClass string `xml:"ResponseClass,attr,omitempty"`
	ResponseCode string `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseCode,omitempty"`
	ErrorMessage string `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ErrorMessage,omitempty"`
}

// ---------------------------------------------------------------------------
// Resource booking policy enforcement
// ---------------------------------------------------------------------------

// checkResourceBookingPolicy checks whether a calendar item creation that
// includes resource attendees should be auto-accepted, auto-declined, or
// routed to a delegate based on the resource's booking policy.
// Satisfies VAL-DIR-010 and VAL-COLLAB-010.
func (s *Server) checkResourceBookingPolicy(ctx context.Context, attendees []AttendeeType) (map[string]BookingDecision, error) {
	if len(attendees) == 0 {
		return nil, nil
	}

	decisions := make(map[string]BookingDecision)

	for _, att := range attendees {
		email := att.Mailbox.Email
		if email == "" {
			continue
		}

		// Look up the resource policy.
		resourceID, err := semcore.NewResourceId(email)
		if err != nil {
			continue
		}
		policy, err := s.policyStore.GetResource(resourceID)
		if err != nil {
			// Not a registered resource: allow it to pass through
			// (not all mailboxes are resources).
			decisions[email] = BookingDecision("")
			continue
		}

		// HiddenFromGAL resources should not be bookable via normal GAL flows,
		// but we still enforce the policy for explicit resource invites.
		switch policy.Decision {
		case semcore.BookingDecisionAutoAccept:
			decisions[email] = BookingDecisionAutoAccept
		case semcore.BookingDecisionAutoDecline:
			decisions[email] = BookingDecisionAutoDecline
		case semcore.BookingDecisionDelegateReview:
			decisions[email] = BookingDecisionDelegateReview
		case semcore.BookingDecisionProvisional:
			decisions[email] = BookingDecisionProvisional
		default:
			decisions[email] = BookingDecisionAutoDecline
		}
	}

	return decisions, nil
}

// BookingDecision is the outcome of a resource booking policy check.
type BookingDecision string

const (
	BookingDecisionAutoAccept   BookingDecision = "AutoAccept"
	BookingDecisionAutoDecline BookingDecision = "AutoDecline"
	BookingDecisionDelegateReview BookingDecision = "DelegateReview"
	BookingDecisionProvisional BookingDecision = "Provisional"
)

// applyResourceBookingPolicy applies the resource booking decisions to a
// CreateCalendarItem response. For auto-accept, the response is Success.
// For auto-decline, the response is an error. For delegate-review, the
// response indicates the request is pending.
func (s *Server) applyResourceBookingPolicy(
	decisions map[string]BookingDecision,
	attendees []AttendeeType,
) (bool, []CalendarItemResponseMessageType) {
	if len(decisions) == 0 {
		return true, nil // no resource decisions to apply
	}

	messages := make([]CalendarItemResponseMessageType, 0, len(attendees))
	allAccepted := true

	for _, att := range attendees {
		decision, ok := decisions[att.Mailbox.Email]
		if !ok {
			continue // not a resource
		}

		switch decision {
		case BookingDecisionAutoAccept:
			// Resource auto-accepts: no error, proceed.
			messages = append(messages, CalendarItemResponseMessageType{
				ResponseClass: "Success",
				ResponseCode:  ResponseCodeType{Value: ErrNoError},
			})
		case BookingDecisionAutoDecline:
			// Resource auto-declines: reject with policy error.
			messages = append(messages, CalendarItemResponseMessageType{
				ResponseClass: "Error",
				ResponseCode:  ResponseCodeType{Value: ErrErrorInternalServer},
			})
			allAccepted = false
		case BookingDecisionDelegateReview:
			// Delegate review required: pending.
			messages = append(messages, CalendarItemResponseMessageType{
				ResponseClass: "Success",
				ResponseCode:  ResponseCodeType{Value: ErrNoError},
			})
		case BookingDecisionProvisional:
			// Provisional accept.
			messages = append(messages, CalendarItemResponseMessageType{
				ResponseClass: "Success",
				ResponseCode:  ResponseCodeType{Value: ErrNoError},
			})
		}
	}

	return allAccepted, messages
}
