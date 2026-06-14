package wbxml

// This file holds the MS-ASWBXML code-page token tables. Each ActiveSync XML
// namespace is a numbered code page; every element name maps to a one-byte
// token within its page (MS-ASWBXML 2.1.2). The tables here are transcribed
// from the MS-ASWBXML specification; the global WBXML tokens (codec.go) and the
// Email page were cross-checked against the published tables.
//
// Code pages are added as the protocol surface that uses them lands: this phase
// (the codec) ships the AirSync envelope (page 0, used by every Sync) and the
// Email data class (page 2). Calendar, Contacts, Tasks, FolderHierarchy,
// Provision, AirSyncBase and the rest are registered in the phases that consume
// them, each verified against its MS-ASWBXML page at that point.

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

// PageAirSync (code page 0) is the AirSync namespace: the Sync command envelope
// shared by every data class (MS-ASWBXML 2.1.2.1.1).
const PageAirSync byte = 0

// PageEmail (code page 2) is the Email data class (MS-ASWBXML 2.1.2.1.3).
const PageEmail byte = 2

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
	0x11: "Version",
	0x12: "CollectionId",
	0x13: "GetChanges",
	0x14: "MoreAvailable",
	0x15: "WindowSize",
	0x16: "Commands",
	0x17: "Options",
	0x18: "FilterType",
	0x19: "Truncation",
	0x1A: "RtfTruncation",
	0x1B: "Conflict",
	0x1C: "Collections",
	0x1D: "ApplicationData",
	0x1E: "DeletesAsMoves",
	0x1F: "NotifyGUID",
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
	0x0B: "AttRemoved",
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
