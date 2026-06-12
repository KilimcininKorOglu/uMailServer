package emsmdb

// MAPI/Exchange result codes (MS-OXCDATA 2.4) returned in transport responses.
// Additional ROP-level codes are added as the ROP layer needs them.
const (
	ecSuccess        uint32 = 0x00000000
	ecError          uint32 = 0x80004005 // general failure
	ecAccessDenied   uint32 = 0x80070005
	ecNotImplemented uint32 = 0x80040FFF // ROP not supported by this server
)
