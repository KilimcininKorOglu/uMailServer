// Package emsmdb implements the binary mailbox connector (emsmdb) over
// MAPI/HTTP: the MS-OXCMAPIHTTP Connect/Execute/Disconnect transport and the
// MS-OXCROPS ROP buffer framing that carries remote operations to and from the
// canonical mailbox store. The ROP semantics themselves (the individual remote
// operations) are layered on top of this transport.
package emsmdb

import (
	"errors"

	"github.com/umailserver/umailserver/internal/mapi/lzxpress"
	"github.com/umailserver/umailserver/internal/mapi/wire"
)

// ErrCorrupt indicates a malformed ROP buffer or RPC_HEADER_EXT frame.
var ErrCorrupt = errors.New("emsmdb: corrupt ROP buffer")

// obfuscationMask is the XOR byte applied when RHE_FLAG_XORMAGIC is set
// (MS-OXCRPC 3.1.4.1 obfuscation).
const obfuscationMask = 0xA5

// deobfuscate XORs every byte with the obfuscation mask in place.
func deobfuscate(b []byte) {
	for i := range b {
		b[i] ^= obfuscationMask
	}
}

// DecodeROPBuffer reverses the MS-OXCROPS request framing. The input is one or
// more RPC_HEADER_EXT-prefixed fragments (the last carrying RHE_FLAG_LAST);
// each fragment is de-obfuscated and decompressed as flagged, then the
// reassembled payload is split into the ROP list bytes (ropData) and the
// server-object handle table.
func DecodeROPBuffer(input []byte) (version uint16, ropData []byte, handles []uint32, err error) {
	p := wire.NewPull(input, 0)
	var payload []byte
	for {
		hdr := wire.PullRPCHeaderExt(p)
		if p.Err() != nil {
			return 0, nil, nil, ErrCorrupt
		}
		version = hdr.Version
		frag := p.Bytes(int(hdr.Size))
		if p.Err() != nil {
			return 0, nil, nil, ErrCorrupt
		}
		if hdr.Flags&wire.RHEFlagXorMagic != 0 {
			deobfuscate(frag)
		}
		if hdr.Flags&wire.RHEFlagCompressed != 0 {
			raw, derr := lzxpress.Decompress(frag, int(hdr.SizeActual))
			if derr != nil || len(raw) < int(hdr.SizeActual) {
				return 0, nil, nil, ErrCorrupt
			}
			payload = append(payload, raw...)
		} else {
			if len(frag) < int(hdr.SizeActual) {
				return 0, nil, nil, ErrCorrupt
			}
			payload = append(payload, frag[:hdr.SizeActual]...)
		}
		if hdr.Flags&wire.RHEFlagLast != 0 || p.Remaining() == 0 {
			break
		}
	}

	pp := wire.NewPull(payload, 0)
	size := int(pp.Uint16())
	if pp.Err() != nil || size < 2 || size > len(payload) {
		return 0, nil, nil, ErrCorrupt
	}
	ropData = payload[2:size]
	rest := payload[size:]
	n := len(rest) / 4
	handles = make([]uint32, n)
	hp := wire.NewPull(rest, 0)
	for i := range handles {
		handles[i] = hp.Uint32()
	}
	if hp.Err() != nil {
		return 0, nil, nil, ErrCorrupt
	}
	return version, ropData, handles, nil
}

// EncodeROPBuffer builds the MS-OXCROPS response framing: a RopListSize-prefixed
// payload of the ROP response bytes plus the server-object handle table, wrapped
// in a single RHE_FLAG_LAST RPC_HEADER_EXT. When compress is set and the payload
// shrinks, it is LZXPRESS-compressed and the compressed flag is set; otherwise
// the buffer is sent uncompressed.
func EncodeROPBuffer(version uint16, ropData []byte, handles []uint32, compress bool) []byte {
	ip := wire.NewPush(0)
	ip.Uint16(uint16(2 + len(ropData)))
	ip.Raw(ropData)
	for _, h := range handles {
		ip.Uint32(h)
	}
	inner := ip.Bytes()

	hdr := wire.RPCHeaderExt{
		Version:    version,
		Flags:      wire.RHEFlagLast,
		SizeActual: uint16(len(inner)),
		Size:       uint16(len(inner)),
	}
	body := inner
	if compress {
		comp := lzxpress.Compress(inner)
		if len(comp) > 0 && len(comp) < len(inner) {
			hdr.Flags |= wire.RHEFlagCompressed
			hdr.Size = uint16(len(comp))
			body = comp
		}
	}

	out := wire.NewPush(0)
	hdr.Push(out)
	out.Raw(body)
	return out.Bytes()
}
