package emsmdb

// MAPI/Exchange result codes (MS-OXCDATA 2.4) returned in transport responses.
// Additional ROP-level codes are added as the ROP layer needs them.
const (
	ecSuccess        uint32 = 0x00000000
	ecError          uint32 = 0x80004005 // general failure
	ecAccessDenied   uint32 = 0x80070005
	ecNullObject     uint32 = 0x000004B9 // input handle references no live object
	ecNotFound       uint32 = 0x8004010F // requested object does not exist
	ecNotImplemented uint32 = 0x80040FFF // ROP not supported by this server
)
