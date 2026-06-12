package emsmdb

import (
	"github.com/umailserver/umailserver/internal/mapi/wire"
	"github.com/umailserver/umailserver/internal/storage"
)

// msgFlagRead marks a message the user has read (MS-OXCMSG 2.2.1.6,
// PidTagMessageFlags). Only the read bit is derived on the online read path.
const msgFlagRead uint32 = 0x00000001

// messageID builds a message id (MID) from a message uid, reusing the replica id
// and 48-bit-counter layout of a folder id (MS-OXCDATA 2.2.1.2). The uid is
// stable per mailbox, so the MID round-trips back to the uid.
func messageID(uid uint32) uint64 {
	return makeFID(fidReplID, uint64(uid))
}

// instanceKey is a table row's unique key (PidTagInstanceKey): the 4-byte
// little-endian uid, unique within the folder snapshot.
func instanceKey(uid uint32) []byte {
	return []byte{byte(uid), byte(uid >> 8), byte(uid >> 16), byte(uid >> 24)}
}

// messageFlags derives PidTagMessageFlags from the stored IMAP flags.
func messageFlags(m *storage.MessageMetadata) uint32 {
	var f uint32
	for _, fl := range m.Flags {
		if fl == "\\Seen" {
			f |= msgFlagRead
		}
	}
	return f
}

// messageProperty returns the value of one property for a message and whether it
// is available. Unmapped columns report unavailable so the row can flag them.
// The value's Go type matches the column's property type (MS-OXCDATA 2.11.2).
func messageProperty(tag wire.PropTag, m *storage.MessageMetadata) (any, bool) {
	switch tag {
	case wire.PidTagMid:
		return messageID(m.UID), true
	case wire.PidTagInstanceKey:
		return instanceKey(m.UID), true
	case wire.PidTagSubject:
		return m.Subject, true
	case wire.PidTagMessageClass:
		return "IPM.Note", true
	case wire.PidTagMessageDeliveryTime:
		return wire.FileTimeFromTime(m.InternalDate), true
	case wire.PidTagMessageSize:
		return uint32(m.Size), true
	case wire.PidTagMessageFlags:
		return messageFlags(m), true
	default:
		return nil, false
	}
}
