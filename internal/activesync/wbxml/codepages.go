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
	PageEmail           byte = 2  // Email data class
	PageMove            byte = 5  // MoveItems command
	PageGetItemEstimate byte = 6  // GetItemEstimate command
	PageFolderHierarchy byte = 7  // FolderSync/FolderCreate/FolderDelete/FolderUpdate
	PageProvision       byte = 14 // Provision command + policy document
	PageAirSyncBase     byte = 17 // shared Body/Attachments/BodyPart (12.0+) + Location
	PageSettings        byte = 18 // Settings: DeviceInformation, OOF, UserInformation
	PageComposeMail     byte = 21 // SendMail/SmartForward/SmartReply
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
