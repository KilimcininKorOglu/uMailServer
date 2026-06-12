package emsmdb

// MAPI/Exchange result codes (MS-OXCDATA 2.4) returned in transport responses.
// Additional ROP-level codes are added as the ROP layer needs them.
const (
	ecError        uint32 = 0x80004005 // general failure
	ecAccessDenied uint32 = 0x80070005
)
