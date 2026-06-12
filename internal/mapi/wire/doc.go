// Package wire implements the binary MAPI wire codec shared by the server's
// MAPI/HTTP surfaces: the mailbox connector (emsmdb, MS-OXCROPS over
// MS-OXCMAPIHTTP), the address book (NSPI, MS-OXNSPI), and the offline address
// book (MS-OXOAB). It provides little-endian push/pull buffers plus the MAPI
// property data model (MS-OXCDATA) — property tags, property values, rows, row
// sets, property-tag arrays, restrictions, and entry IDs — that those protocols
// serialize.
//
// # Encoding
//
// All integers are little-endian. Strings are either NUL-terminated UTF-8 or
// NUL-terminated UTF-16LE, selected per buffer by FlagUTF16 (the MS-OXCMAPIHTTP
// transport carries Unicode strings as UTF-16LE). Length-counted binary uses a
// 16- or 32-bit count selected by FlagWCount, matching the EXT serialization
// variants the ROP and address-book transports use.
//
// # Verification limitation (read this)
//
// This codec is verified by (a) round-trip — Push then Pull yields equal
// values — and (b) conformance to the documented MS-OXCDATA / MS-OXCROPS
// structure layouts. It is NOT verified against a real Microsoft Outlook or
// Exchange server: none is available in this environment. Real-client
// interoperability is therefore best-effort and not guaranteed.
package wire
