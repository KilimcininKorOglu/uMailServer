package emsmdb

import "github.com/umailserver/umailserver/internal/mapi/wire"

// The MS-OXCMAPIHTTP request bodies for the mailbox endpoint. Each is decoded
// from the HTTP request body of the matching X-RequestType. The trailing
// auxiliary buffer is an opaque ROP-auxiliary blob this server does not act on.

// ConnectRequest is the body of a Connect request (MS-OXCMAPIHTTP 2.2.4.1.1).
type ConnectRequest struct {
	UserDN          string
	Flags           uint32
	DefaultCodePage uint32
	LcidSort        uint32
	LcidString      uint32
	AuxIn           []byte
}

// DecodeConnectRequest parses a Connect request body.
func DecodeConnectRequest(b []byte) (ConnectRequest, error) {
	p := wire.NewPull(b, 0)
	var r ConnectRequest
	r.UserDN = p.Str()
	r.Flags = p.Uint32()
	r.DefaultCodePage = p.Uint32()
	r.LcidSort = p.Uint32()
	r.LcidString = p.Uint32()
	r.AuxIn = readCounted(p)
	if p.Err() != nil {
		return ConnectRequest{}, ErrCorrupt
	}
	return r, nil
}

// ExecuteRequest is the body of an Execute request (MS-OXCMAPIHTTP 2.2.4.2.1).
// RopBuffer is the RPC_HEADER_EXT-framed ROP request buffer; MaxRopOut is the
// largest ROP response buffer the client will accept.
type ExecuteRequest struct {
	Flags     uint32
	RopBuffer []byte
	MaxRopOut uint32
	AuxIn     []byte
}

// DecodeExecuteRequest parses an Execute request body.
func DecodeExecuteRequest(b []byte) (ExecuteRequest, error) {
	p := wire.NewPull(b, 0)
	var r ExecuteRequest
	r.Flags = p.Uint32()
	r.RopBuffer = readCounted(p)
	r.MaxRopOut = p.Uint32()
	r.AuxIn = readCounted(p)
	if p.Err() != nil {
		return ExecuteRequest{}, ErrCorrupt
	}
	return r, nil
}

// DisconnectRequest is the body of a Disconnect request
// (MS-OXCMAPIHTTP 2.2.4.3.1); it carries only the auxiliary buffer.
type DisconnectRequest struct {
	AuxIn []byte
}

// DecodeDisconnectRequest parses a Disconnect request body.
func DecodeDisconnectRequest(b []byte) (DisconnectRequest, error) {
	p := wire.NewPull(b, 0)
	r := DisconnectRequest{AuxIn: readCounted(p)}
	if p.Err() != nil {
		return DisconnectRequest{}, ErrCorrupt
	}
	return r, nil
}

// NotificationWaitRequest is the body of a NotificationWait request
// (MS-OXCMAPIHTTP 2.2.4.4.1).
type NotificationWaitRequest struct {
	Flags uint32
	AuxIn []byte
}

// DecodeNotificationWaitRequest parses a NotificationWait request body.
func DecodeNotificationWaitRequest(b []byte) (NotificationWaitRequest, error) {
	p := wire.NewPull(b, 0)
	var r NotificationWaitRequest
	r.Flags = p.Uint32()
	r.AuxIn = readCounted(p)
	if p.Err() != nil {
		return NotificationWaitRequest{}, ErrCorrupt
	}
	return r, nil
}

// Every MS-OXCMAPIHTTP response body begins with StatusCode then, on success
// (StatusCode 0), ErrorCode followed by the success-specific fields
// (MS-OXCMAPIHTTP 2.2.4).

// ConnectResponse is the body of a Connect response (MS-OXCMAPIHTTP 2.2.4.1.2).
type ConnectResponse struct {
	StatusCode  uint32
	ErrorCode   uint32
	PollsMax    uint32
	RetryCount  uint32
	RetryDelay  uint32
	DnPrefix    string
	DisplayName string
	AuxOut      []byte
}

// Encode serializes the Connect response body. DisplayName is UTF-16LE.
func (r ConnectResponse) Encode() []byte {
	p := wire.NewPush(wire.FlagUTF16)
	p.Uint32(r.StatusCode)
	p.Uint32(r.ErrorCode)
	p.Uint32(r.PollsMax)
	p.Uint32(r.RetryCount)
	p.Uint32(r.RetryDelay)
	p.Str(r.DnPrefix)
	p.WStr(r.DisplayName)
	writeCounted(p, r.AuxOut)
	return p.Bytes()
}

// ExecuteResponse is the body of an Execute response (MS-OXCMAPIHTTP 2.2.4.2.2).
type ExecuteResponse struct {
	StatusCode uint32
	ErrorCode  uint32
	Flags      uint32
	RopBuffer  []byte
	AuxOut     []byte
}

// Encode serializes the Execute response body.
func (r ExecuteResponse) Encode() []byte {
	p := wire.NewPush(wire.FlagUTF16)
	p.Uint32(r.StatusCode)
	p.Uint32(r.ErrorCode)
	p.Uint32(r.Flags)
	writeCounted(p, r.RopBuffer)
	writeCounted(p, r.AuxOut)
	return p.Bytes()
}

// DisconnectResponse is the body of a Disconnect response
// (MS-OXCMAPIHTTP 2.2.4.3.2). It ends with a zero auxiliary-buffer size.
type DisconnectResponse struct {
	StatusCode uint32
	ErrorCode  uint32
}

// Encode serializes the Disconnect response body.
func (r DisconnectResponse) Encode() []byte {
	p := wire.NewPush(wire.FlagUTF16)
	p.Uint32(r.StatusCode)
	p.Uint32(r.ErrorCode)
	p.Uint32(0) // AuxiliaryBufferSize
	return p.Bytes()
}

// NotificationWaitResponse is the body of a NotificationWait response
// (MS-OXCMAPIHTTP 2.2.4.4.2).
type NotificationWaitResponse struct {
	StatusCode uint32
	ErrorCode  uint32
	FlagsOut   uint32
}

// Encode serializes the NotificationWait response body.
func (r NotificationWaitResponse) Encode() []byte {
	p := wire.NewPush(wire.FlagUTF16)
	p.Uint32(r.StatusCode)
	p.Uint32(r.ErrorCode)
	p.Uint32(r.FlagsOut)
	p.Uint32(0) // AuxiliaryBufferSize
	return p.Bytes()
}

// readCounted reads a 32-bit length followed by that many bytes.
func readCounted(p *wire.Pull) []byte {
	n := int(p.Uint32())
	if n == 0 {
		return nil
	}
	return p.Bytes(n)
}

// writeCounted writes a 32-bit length followed by the bytes.
func writeCounted(p *wire.Push, b []byte) {
	p.Uint32(uint32(len(b)))
	p.Raw(b)
}
