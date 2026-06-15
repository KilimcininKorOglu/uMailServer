package wbxml

// This file holds the MS-ASWBXML code-page token tables. Each ActiveSync XML
// namespace is a numbered code page; every element name maps to a one-byte
// token within its page (MS-ASWBXML 2.1.2). The tables here are transcribed
// directly from the published MS-ASWBXML code-page pages (token values pulled
// from the spec tables, not from memory — an early hand-keyed AirSync/Email
// table carried spurious 2.5-era tokens that the spec omits).
//
// Code pages are added as the protocol surface that uses them lands: the codec
// ships the AirSync envelope (page 0) and the Email data class (page 2); the
// transport milestone adds FolderHierarchy (7), Provision (14), AirSyncBase
// (17) and GetItemEstimate (6). Calendar, Contacts, Tasks, Settings and the
// rest are registered in the phases that consume them, each pulled from its
// MS-ASWBXML page at that point.

// codePageTable maps element names to tokens (and back) for one code page.
type codePageTable struct {
	number byte
	ns     string
	byName map[string]byte
	byTok  map[byte]string
}

// token returns the WBXML token for an element name within this page.
func (c *codePageTable) token(name string) (byte, bool) {
	t, ok := c.byName[name]
	return t, ok
}

// name returns the element name for a token within this page.
func (c *codePageTable) name(tok byte) (string, bool) {
	n, ok := c.byTok[tok]
	return n, ok
}

// registry holds every known code page keyed by its page number.
var registry = map[byte]*codePageTable{}

// register builds a code-page table from a token->name map and indexes it by
// page number. Panicking on a duplicate name keeps a transcription slip (two
// element names colliding within a page) from silently shadowing a token.
func register(number byte, ns string, tokens map[byte]string) *codePageTable {
	cp := &codePageTable{
		number: number,
		ns:     ns,
		byName: make(map[string]byte, len(tokens)),
		byTok:  make(map[byte]string, len(tokens)),
	}
	for tok, name := range tokens {
		if _, dup := cp.byName[name]; dup {
			panic("wbxml: duplicate element name " + name + " in code page " + ns)
		}
		cp.byName[name] = tok
		cp.byTok[tok] = name
	}
	registry[number] = cp
	return cp
}

// codePage returns the table for a page number.
func codePage(n byte) (*codePageTable, bool) {
	cp, ok := registry[n]
	return cp, ok
}

// Code page numbers used so far (MS-ASWBXML 2.1.2.1).
const (
	PageAirSync         byte = 0  // Sync command envelope, shared by every data class
	PageContacts        byte = 1  // Contacts data class (MS-ASCNTC)
	PageEmail           byte = 2  // Email data class
	PageCalendar        byte = 4  // Calendar data class
	PageTasks           byte = 9  // Tasks data class (MS-ASTASK)
	PageContacts2       byte = 12 // Contacts data class extension (MS-ASCNTC)
	PageMove            byte = 5  // MoveItems command
	PageMeetingResponse byte = 8  // MeetingResponse command
	PageGetItemEstimate byte = 6  // GetItemEstimate command
	PageFolderHierarchy byte = 7  // FolderSync/FolderCreate/FolderDelete/FolderUpdate
	PageProvision       byte = 14 // Provision command + policy document
	PageAirSyncBase     byte = 17 // shared Body/Attachments/BodyPart (12.0+) + Location
	PageSettings        byte = 18 // Settings: DeviceInformation, OOF, UserInformation
	PageItemOperations  byte = 20 // ItemOperations: Fetch/EmptyFolderContents/Move
	PageComposeMail     byte = 21 // SendMail/SmartForward/SmartReply
	PagePing            byte = 13 // Ping command (long-poll change notification)
	PageSearch          byte = 15 // Search command (GAL + mailbox full-text)
	PageGAL             byte = 16 // Global Address List entry properties (Search results)
)

var _ = register(PageAirSync, "AirSync", map[byte]string{
	0x05: "Sync",
	0x06: "Responses",
	0x07: "Add",
	0x08: "Change",
	0x09: "Delete",
	0x0A: "Fetch",
	0x0B: "SyncKey",
	0x0C: "ClientId",
	0x0D: "ServerId",
	0x0E: "Status",
	0x0F: "Collection",
	0x10: "Class",
	0x12: "CollectionId",
	0x13: "GetChanges",
	0x14: "MoreAvailable",
	0x15: "WindowSize",
	0x16: "Commands",
	0x17: "Options",
	0x18: "FilterType",
	0x19: "Truncation",
	0x1B: "Conflict",
	0x1C: "Collections",
	0x1D: "ApplicationData",
	0x1E: "DeletesAsMoves",
	0x20: "Supported",
	0x21: "SoftDelete",
	0x22: "MIMESupport",
	0x23: "MIMETruncation",
	0x24: "Wait",
	0x25: "Limit",
	0x26: "Partial",
	0x27: "ConversationMode",
	0x28: "MaxItems",
	0x29: "HeartbeatInterval",
})

var _ = register(PageEmail, "Email", map[byte]string{
	0x05: "Attachment",
	0x06: "Attachments",
	0x07: "AttName",
	0x08: "AttSize",
	0x09: "Att0id",
	0x0A: "AttMethod",
	0x0C: "Body",
	0x0D: "BodySize",
	0x0E: "BodyTruncated",
	0x0F: "DateReceived",
	0x10: "DisplayName",
	0x11: "DisplayTo",
	0x12: "Importance",
	0x13: "MessageClass",
	0x14: "Subject",
	0x15: "Read",
	0x16: "To",
	0x17: "Cc",
	0x18: "From",
	0x19: "ReplyTo",
	0x1A: "AllDayEvent",
	0x1B: "Categories",
	0x1C: "Category",
	0x1D: "DtStamp",
	0x1E: "EndTime",
	0x1F: "InstanceType",
	0x20: "BusyStatus",
	0x21: "Location",
	0x22: "MeetingRequest",
	0x23: "Organizer",
	0x24: "RecurrenceId",
	0x25: "Reminder",
	0x26: "ResponseRequested",
	0x27: "Recurrences",
	0x28: "Recurrence",
	0x29: "Type",
	0x2A: "Until",
	0x2B: "Occurrences",
	0x2C: "Interval",
	0x2D: "DayOfWeek",
	0x2E: "DayOfMonth",
	0x2F: "WeekOfMonth",
	0x30: "MonthOfYear",
	0x31: "StartTime",
	0x32: "Sensitivity",
	0x33: "TimeZone",
	0x34: "GlobalObjId",
	0x35: "ThreadTopic",
	0x36: "MIMEData",
	0x37: "MIMETruncated",
	0x38: "MIMESize",
	0x39: "InternetCPID",
	0x3A: "Flag",
	0x3B: "Status",
	0x3C: "ContentClass",
	0x3D: "FlagType",
	0x3E: "CompleteTime",
	0x3F: "DisallowNewTimeProposal",
})

// Calendar data class (MS-ASWBXML 2.1.2.1.5, Code Page 4 / MS-ASCAL). The full
// token set is registered so a client's up-sync ApplicationData decodes whatever
// element it sends. Tokens 0x0B Body / 0x0C BodyTruncated are 2.5-only legacy;
// 16.x carries the body through AirSyncBase (page 17) instead. The Protocol-
// versions column of the spec table marks which tokens a given version uses; all
// names are registered regardless so older clients still decode.
var _ = register(PageCalendar, "Calendar", map[byte]string{
	0x05: "Timezone",
	0x06: "AllDayEvent",
	0x07: "Attendees",
	0x08: "Attendee",
	0x09: "Email",
	0x0A: "Name",
	0x0B: "Body",
	0x0C: "BodyTruncated",
	0x0D: "BusyStatus",
	0x0E: "Categories",
	0x0F: "Category",
	0x11: "DtStamp",
	0x12: "EndTime",
	0x13: "Exception",
	0x14: "Exceptions",
	0x15: "Deleted",
	0x16: "ExceptionStartTime",
	0x17: "Location",
	0x18: "MeetingStatus",
	0x19: "OrganizerEmail",
	0x1A: "OrganizerName",
	0x1B: "Recurrence",
	0x1C: "Type",
	0x1D: "Until",
	0x1E: "Occurrences",
	0x1F: "Interval",
	0x20: "DayOfWeek",
	0x21: "DayOfMonth",
	0x22: "WeekOfMonth",
	0x23: "MonthOfYear",
	0x24: "Reminder",
	0x25: "Sensitivity",
	0x26: "Subject",
	0x27: "StartTime",
	0x28: "UID",
	0x29: "AttendeeStatus",
	0x2A: "AttendeeType",
	0x33: "DisallowNewTimeProposal",
	0x34: "ResponseRequested",
	0x35: "AppointmentReplyTime",
	0x36: "ResponseType",
	0x37: "CalendarType",
	0x38: "IsLeapMonth",
	0x39: "FirstDayOfWeek",
	0x3A: "OnlineMeetingConfLink",
	0x3B: "OnlineMeetingExternalLink",
	0x3C: "ClientUid",
})

// Contacts data class (MS-ASWBXML 2.1.2.1.2, Code Page 1 / MS-ASCNTC). The full
// token set is registered so a client's up-sync ApplicationData decodes whatever
// element it sends. Tokens 0x09 Body / 0x0A BodySize / 0x0B BodyTruncated are
// 2.5-only legacy; 16.x carries the body through AirSyncBase (page 17) instead.
// 0x3B is unassigned in the spec table (it jumps from 0x3A to 0x3C).
var _ = register(PageContacts, "Contacts", map[byte]string{
	0x05: "Anniversary",
	0x06: "AssistantName",
	0x07: "AssistantPhoneNumber",
	0x08: "Birthday",
	0x09: "Body",
	0x0A: "BodySize",
	0x0B: "BodyTruncated",
	0x0C: "Business2PhoneNumber",
	0x0D: "BusinessAddressCity",
	0x0E: "BusinessAddressCountry",
	0x0F: "BusinessAddressPostalCode",
	0x10: "BusinessAddressState",
	0x11: "BusinessAddressStreet",
	0x12: "BusinessFaxNumber",
	0x13: "BusinessPhoneNumber",
	0x14: "CarPhoneNumber",
	0x15: "Categories",
	0x16: "Category",
	0x17: "Children",
	0x18: "Child",
	0x19: "CompanyName",
	0x1A: "Department",
	0x1B: "Email1Address",
	0x1C: "Email2Address",
	0x1D: "Email3Address",
	0x1E: "FileAs",
	0x1F: "FirstName",
	0x20: "Home2PhoneNumber",
	0x21: "HomeAddressCity",
	0x22: "HomeAddressCountry",
	0x23: "HomeAddressPostalCode",
	0x24: "HomeAddressState",
	0x25: "HomeAddressStreet",
	0x26: "HomeFaxNumber",
	0x27: "HomePhoneNumber",
	0x28: "JobTitle",
	0x29: "LastName",
	0x2A: "MiddleName",
	0x2B: "MobilePhoneNumber",
	0x2C: "OfficeLocation",
	0x2D: "OtherAddressCity",
	0x2E: "OtherAddressCountry",
	0x2F: "OtherAddressPostalCode",
	0x30: "OtherAddressState",
	0x31: "OtherAddressStreet",
	0x32: "PagerNumber",
	0x33: "RadioPhoneNumber",
	0x34: "Spouse",
	0x35: "Suffix",
	0x36: "Title",
	0x37: "WebPage",
	0x38: "YomiCompanyName",
	0x39: "YomiFirstName",
	0x3A: "YomiLastName",
	0x3C: "Picture",
})

// Contacts2 data class (MS-ASWBXML 2.1.2.1.13, Code Page 12 / MS-ASCNTC). These
// are the extended contact fields a 16.x client may carry on its own page; the
// table is registered so an up-sync that includes them decodes rather than
// failing on an unknown page.
var _ = register(PageContacts2, "Contacts2", map[byte]string{
	0x05: "CustomerId",
	0x06: "GovernmentId",
	0x07: "IMAddress",
	0x08: "IMAddress2",
	0x09: "IMAddress3",
	0x0A: "ManagerName",
	0x0B: "CompanyMainPhone",
	0x0C: "AccountName",
	0x0D: "NickName",
	0x0E: "MMS",
})

// Tasks data class (MS-ASWBXML 2.1.2.1.10, Code Page 9 / MS-ASTASK). The full
// token set is registered so a client's up-sync ApplicationData decodes whatever
// element it sends. Tokens 0x05 Body / 0x06 BodySize / 0x07 BodyTruncated are
// 2.5-only legacy; 16.x carries the body through AirSyncBase (page 17) instead.
// 0x21 is unassigned in the spec table (it jumps from 0x20 to 0x22).
var _ = register(PageTasks, "Tasks", map[byte]string{
	0x05: "Body",
	0x06: "BodySize",
	0x07: "BodyTruncated",
	0x08: "Categories",
	0x09: "Category",
	0x0A: "Complete",
	0x0B: "DateCompleted",
	0x0C: "DueDate",
	0x0D: "UtcDueDate",
	0x0E: "Importance",
	0x0F: "Recurrence",
	0x10: "Type",
	0x11: "Start",
	0x12: "Until",
	0x13: "Occurrences",
	0x14: "Interval",
	0x15: "DayOfMonth",
	0x16: "DayOfWeek",
	0x17: "WeekOfMonth",
	0x18: "MonthOfYear",
	0x19: "Regenerate",
	0x1A: "DeadOccur",
	0x1B: "ReminderSet",
	0x1C: "ReminderTime",
	0x1D: "Sensitivity",
	0x1E: "StartDate",
	0x1F: "UtcStartDate",
	0x20: "Subject",
	0x22: "OrdinalDate",
	0x23: "SubOrdinalDate",
})

var _ = register(PageMove, "Move", map[byte]string{
	0x05: "MoveItems",
	0x06: "Move",
	0x07: "SrcMsgId",
	0x08: "SrcFldId",
	0x09: "DstFldId",
	0x0A: "Response",
	0x0B: "Status",
	0x0C: "DstMsgId",
})

// MeetingResponse command (MS-ASWBXML 2.1.2.1.9, Code Page 8). CollectionId and
// RequestId are this page's own tokens (not the AirSync page's), so the request
// carries them on page 8.
var _ = register(PageMeetingResponse, "MeetingResponse", map[byte]string{
	0x05: "CalendarId",
	0x06: "CollectionId",
	0x07: "MeetingResponse",
	0x08: "RequestId",
	0x09: "Request",
	0x0A: "Result",
	0x0B: "Status",
	0x0C: "UserResponse",
	0x0E: "InstanceId",
	0x10: "ProposedStartTime",
	0x11: "ProposedEndTime",
	0x12: "SendResponse",
})

var _ = register(PageGetItemEstimate, "GetItemEstimate", map[byte]string{
	0x05: "GetItemEstimate",
	0x07: "Collections",
	0x08: "Collection",
	0x09: "Class",
	0x0A: "CollectionId",
	0x0C: "Estimate",
	0x0D: "Response",
	0x0E: "Status",
})

var _ = register(PageFolderHierarchy, "FolderHierarchy", map[byte]string{
	0x05: "Folders",
	0x06: "Folder",
	0x07: "DisplayName",
	0x08: "ServerId",
	0x09: "ParentId",
	0x0A: "Type",
	0x0C: "Status",
	0x0E: "Changes",
	0x0F: "Add",
	0x10: "Delete",
	0x11: "Update",
	0x12: "SyncKey",
	0x13: "FolderCreate",
	0x14: "FolderDelete",
	0x15: "FolderUpdate",
	0x16: "FolderSync",
	0x17: "Count",
})

var _ = register(PageProvision, "Provision", map[byte]string{
	0x05: "Provision",
	0x06: "Policies",
	0x07: "Policy",
	0x08: "PolicyType",
	0x09: "PolicyKey",
	0x0A: "Data",
	0x0B: "Status",
	0x0C: "RemoteWipe",
	0x0D: "EASProvisionDoc",
	0x0E: "DevicePasswordEnabled",
	0x0F: "AlphanumericDevicePasswordRequired",
	0x10: "RequireStorageCardEncryption",
	0x11: "PasswordRecoveryEnabled",
	0x13: "AttachmentsEnabled",
	0x14: "MinDevicePasswordLength",
	0x15: "MaxInactivityTimeDeviceLock",
	0x16: "MaxDevicePasswordFailedAttempts",
	0x17: "MaxAttachmentSize",
	0x18: "AllowSimpleDevicePassword",
	0x19: "DevicePasswordExpiration",
	0x1A: "DevicePasswordHistory",
	0x1B: "AllowStorageCard",
	0x1C: "AllowCamera",
	0x1D: "RequireDeviceEncryption",
	0x1E: "AllowUnsignedApplications",
	0x1F: "AllowUnsignedInstallationPackages",
	0x20: "MinDevicePasswordComplexCharacters",
	0x21: "AllowWiFi",
	0x22: "AllowTextMessaging",
	0x23: "AllowPOPIMAPEmail",
	0x24: "AllowBluetooth",
	0x25: "AllowIrDA",
	0x26: "RequireManualSyncWhenRoaming",
	0x27: "AllowDesktopSync",
	0x28: "MaxCalendarAgeFilter",
	0x29: "AllowHTMLEmail",
	0x2A: "MaxEmailAgeFilter",
	0x2B: "MaxEmailBodyTruncationSize",
	0x2C: "MaxEmailHTMLBodyTruncationSize",
	0x2D: "RequireSignedSMIMEMessages",
	0x2E: "RequireEncryptedSMIMEMessages",
	0x2F: "RequireSignedSMIMEAlgorithm",
	0x30: "RequireEncryptionSMIMEAlgorithm",
	0x31: "AllowSMIMEEncryptionAlgorithmNegotiation",
	0x32: "AllowSMIMESoftCerts",
	0x33: "AllowBrowser",
	0x34: "AllowConsumerEmail",
	0x35: "AllowRemoteDesktop",
	0x36: "AllowInternetSharing",
	0x37: "UnapprovedInROMApplicationList",
	0x38: "ApplicationName",
	0x39: "ApprovedApplicationList",
	0x3A: "Hash",
	0x3B: "AccountOnlyRemoteWipe",
})

var _ = register(PageAirSyncBase, "AirSyncBase", map[byte]string{
	0x05: "BodyPreference",
	0x06: "Type",
	0x07: "TruncationSize",
	0x08: "AllOrNone",
	0x0A: "Body",
	0x0B: "Data",
	0x0C: "EstimatedDataSize",
	0x0D: "Truncated",
	0x0E: "Attachments",
	0x0F: "Attachment",
	0x10: "DisplayName",
	0x11: "FileReference",
	0x12: "Method",
	0x13: "ContentId",
	0x14: "ContentLocation",
	0x15: "IsInline",
	0x16: "NativeBodyType",
	0x17: "ContentType",
	0x18: "Preview",
	0x19: "BodyPartPreference",
	0x1A: "BodyPart",
	0x1B: "Status",
	0x1C: "Add",
	0x1D: "Delete",
	0x1E: "ClientId",
	0x1F: "Content",
	0x20: "Location",
	0x21: "Annotation",
	0x22: "Street",
	0x23: "City",
	0x24: "State",
	0x25: "Country",
	0x26: "PostalCode",
	0x27: "Latitude",
	0x28: "Longitude",
	0x29: "Accuracy",
	0x2A: "Altitude",
	0x2B: "AltitudeAccuracy",
	0x2C: "LocationUri",
	0x2D: "InstanceId",
})

var _ = register(PageItemOperations, "ItemOperations", map[byte]string{
	0x05: "ItemOperations",
	0x06: "Fetch",
	0x07: "Store",
	0x08: "Options",
	0x09: "Range",
	0x0A: "Total",
	0x0B: "Properties",
	0x0C: "Data",
	0x0D: "Status",
	0x0E: "Response",
	0x0F: "Version",
	0x10: "Schema",
	0x11: "Part",
	0x12: "EmptyFolderContents",
	0x13: "DeleteSubFolders",
	0x14: "UserName",
	0x15: "Password",
})

var _ = register(PageComposeMail, "ComposeMail", map[byte]string{
	0x05: "SendMail",
	0x06: "SmartForward",
	0x07: "SmartReply",
	0x08: "SaveInSentItems",
	0x09: "ReplaceMime",
	0x0B: "Source",
	0x0C: "FolderId",
	0x0D: "ItemId",
	0x0E: "LongId",
	0x0F: "InstanceId",
	0x10: "Mime",
	0x11: "ClientId",
	0x12: "Status",
	0x15: "Forwardees",
	0x16: "Forwardee",
	0x17: "Name",
	0x18: "Email",
})

var _ = register(PageSettings, "Settings", map[byte]string{
	0x05: "Settings",
	0x06: "Status",
	0x07: "Get",
	0x08: "Set",
	0x09: "Oof",
	0x0A: "OofState",
	0x0B: "StartTime",
	0x0C: "EndTime",
	0x0D: "OofMessage",
	0x0E: "AppliesToInternal",
	0x0F: "AppliesToExternalKnown",
	0x10: "AppliesToExternalUnknown",
	0x11: "Enabled",
	0x12: "ReplyMessage",
	0x13: "BodyType",
	0x14: "DevicePassword",
	0x15: "Password",
	0x16: "DeviceInformation",
	0x17: "Model",
	0x18: "IMEI",
	0x19: "FriendlyName",
	0x1A: "OS",
	0x1B: "OSLanguage",
	0x1C: "PhoneNumber",
	0x1D: "UserInformation",
	0x1E: "EmailAddresses",
	0x1F: "SMTPAddress",
	0x20: "UserAgent",
	0x21: "EnableOutboundSMS",
	0x22: "MobileOperator",
	0x23: "PrimarySmtpAddress",
	0x24: "Accounts",
	0x25: "Account",
	0x26: "AccountId",
	0x27: "AccountName",
	0x28: "UserDisplayName",
	0x29: "SendDisabled",
	0x2B: "RightsManagementInformation",
})

// Code Page 13: Ping (MS-ASWBXML 2.1.2.14). 0x06 (AutdState) is retired and
// unassigned in the current spec, so it is intentionally absent.
var _ = register(PagePing, "Ping", map[byte]string{
	0x05: "Ping",
	0x07: "Status",
	0x08: "HeartbeatInterval",
	0x09: "Folders",
	0x0A: "Folder",
	0x0B: "Id",
	0x0C: "Class",
	0x0D: "MaxFolders",
})

// Code Page 15: Search (MS-ASWBXML 2.1.2.16). Drives the Search command for both
// the GAL store (recipient resolution) and the mailbox store (full-text search);
// mailbox hits are identified by LongId.
var _ = register(PageSearch, "Search", map[byte]string{
	0x05: "Search",
	0x07: "Store",
	0x08: "Name",
	0x09: "Query",
	0x0A: "Options",
	0x0B: "Range",
	0x0C: "Status",
	0x0D: "Response",
	0x0E: "Result",
	0x0F: "Properties",
	0x10: "Total",
	0x11: "EqualTo",
	0x12: "Value",
	0x13: "And",
	0x14: "Or",
	0x15: "FreeText",
	0x17: "DeepTraversal",
	0x18: "LongId",
	0x19: "RebuildResults",
	0x1A: "LessThan",
	0x1B: "GreaterThan",
	0x1C: "Schema",
	0x1D: "Supported",
	0x1E: "UserName",       // Since 12.1
	0x1F: "Password",       // Since 12.1
	0x20: "ConversationId", // Since 14.0
	0x21: "Picture",        // Since 14.1
	0x22: "MaxSize",        // Since 14.1
	0x23: "MaxPictures",    // Since 14.1
})

// Code Page 16: GAL (MS-ASWBXML 2.1.2.17). The property set returned for each GAL
// match under a Search Result's Properties element.
var _ = register(PageGAL, "GAL", map[byte]string{
	0x05: "DisplayName",
	0x06: "Phone",
	0x07: "Office",
	0x08: "Title",
	0x09: "Company",
	0x0A: "Alias",
	0x0B: "FirstName",
	0x0C: "LastName",
	0x0D: "HomePhone",
	0x0E: "MobilePhone",
	0x0F: "EmailAddress",
	0x10: "Picture", // Since 14.1
	0x11: "Status",  // Since 14.1
	0x12: "Data",    // Since 14.1
})
