// Package rpch implements the MS-RPCH (RPC over HTTP v2) tunnel that carries the
// EMSMDB RPC interface for Outlook Anywhere. A client opens two HTTP channels to
// /rpc/rpcproxy.dll — RPC_OUT_DATA (server-to-client) and RPC_IN_DATA
// (client-to-server) — and establishes a virtual connection with the CONN/A1,
// CONN/B1, CONN/A3, CONN/C2 RTS handshake before tunneling DCERPC PDUs.
//
// This file holds the RTS (Request To Send) PDU codec: parsing the client's
// CONN/A1 and CONN/B1 setup PDUs and building the server's CONN/A3 and CONN/C2
// replies. The PDU framing (common header) is shared with the dcerpc package.
package rpch

import (
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/umailserver/umailserver/internal/mapi/dcerpc"
)

// RTS command types (MS-RPCH 2.2.3.5).
const (
	cmdReceiveWindowSize  uint32 = 0x00
	cmdConnectionTimeout  uint32 = 0x02
	cmdCookie             uint32 = 0x03
	cmdChannelLifetime    uint32 = 0x04
	cmdClientKeepalive    uint32 = 0x05
	cmdVersion            uint32 = 0x06
	cmdAssociationGroupID uint32 = 0x0C
)

// RTS PDU flags (MS-RPCH 2.2.3.6.1).
const (
	rtsFlagNone uint16 = 0x0000
	rtsFlagPing uint16 = 0x0001
)

const (
	rtsHeaderLen        = 16  // DCERPC common header preceding the RTS flags/count
	defaultReceiveWindow = 262144 // 256 KiB advertised in CONN/C2
	connectionTimeoutMS = 120000
	rtsProtocolVersion  = 1
)

var errShortRTS = errors.New("rpch: truncated RTS PDU")

// cookie is an opaque 16-byte RTS cookie (MS-RPCH 2.2.3.1). The virtual
// connection cookie pairs the IN and OUT channels; per-channel cookies identify
// each channel.
type cookie [16]byte

// connCookies holds the two cookies a CONN/A1 or CONN/B1 PDU carries: the
// virtual connection cookie shared by both channels and the per-channel cookie.
type connCookies struct {
	virtualConnection cookie
	channel           cookie
}

// rtsCommandPayloadSize returns the byte length following a command's 4-byte
// type for the commands the CONN/A1 and CONN/B1 setup PDUs use.
func rtsCommandPayloadSize(cmdType uint32) (int, error) {
	switch cmdType {
	case cmdCookie, cmdAssociationGroupID:
		return 16, nil
	case cmdReceiveWindowSize, cmdConnectionTimeout, cmdChannelLifetime,
		cmdClientKeepalive, cmdVersion:
		return 4, nil
	default:
		return 0, fmt.Errorf("rpch: unexpected RTS command %#x in connection setup", cmdType)
	}
}

// parseConnSetup extracts the virtual-connection and channel cookies from a
// CONN/A1 (OUT channel) or CONN/B1 (IN channel) RTS PDU. Both list the virtual
// connection cookie first and the channel cookie second, so the first two
// Cookie commands give the pair.
func parseConnSetup(pdu []byte) (connCookies, error) {
	pkt, err := dcerpc.Pull(pdu)
	if err != nil {
		return connCookies{}, err
	}
	if pkt.Type != dcerpc.PktRTS {
		return connCookies{}, fmt.Errorf("rpch: expected RTS PDU, got packet type %d", pkt.Type)
	}
	body := pdu[rtsHeaderLen:int(pkt.FragLength)]
	if len(body) < 4 {
		return connCookies{}, errShortRTS
	}
	numCmds := int(binary.LittleEndian.Uint16(body[2:4]))
	off := 4
	var cookies []cookie
	for range numCmds {
		if off+4 > len(body) {
			return connCookies{}, errShortRTS
		}
		cmdType := binary.LittleEndian.Uint32(body[off:])
		off += 4
		size, err := rtsCommandPayloadSize(cmdType)
		if err != nil {
			return connCookies{}, err
		}
		if off+size > len(body) {
			return connCookies{}, errShortRTS
		}
		if cmdType == cmdCookie {
			var c cookie
			copy(c[:], body[off:off+16])
			cookies = append(cookies, c)
		}
		off += size
	}
	if len(cookies) < 2 {
		return connCookies{}, fmt.Errorf("rpch: connection setup carried %d cookies, want at least 2", len(cookies))
	}
	return connCookies{virtualConnection: cookies[0], channel: cookies[1]}, nil
}

// rtsFlags returns the RTS PDU flags (MS-RPCH 2.2.3.6.1) without fully parsing
// the command list, so the tunnel can tell a setup PDU from a Ping or
// flow-control PDU.
func rtsFlags(pdu []byte) (uint16, error) {
	if len(pdu) < rtsHeaderLen+2 {
		return 0, errShortRTS
	}
	return binary.LittleEndian.Uint16(pdu[rtsHeaderLen : rtsHeaderLen+2]), nil
}

func putCmd(b []byte, cmdType, value uint32) []byte {
	b = binary.LittleEndian.AppendUint32(b, cmdType)
	return binary.LittleEndian.AppendUint32(b, value)
}

// buildConnA3 builds the CONN/A3 RTS PDU the outbound proxy sends on the OUT
// channel right after CONN/A1, carrying the connection timeout (MS-RPCH
// 2.2.4.4).
func buildConnA3() []byte {
	cmds := putCmd(nil, cmdConnectionTimeout, connectionTimeoutMS)
	return dcerpc.EncodeRTS(rtsFlagNone, 1, cmds)
}

// buildConnC2 builds the CONN/C2 RTS PDU the outbound proxy sends on the OUT
// channel once the IN channel's CONN/B1 has arrived, completing the virtual
// connection (MS-RPCH 2.2.4.9).
func buildConnC2() []byte {
	cmds := putCmd(nil, cmdVersion, rtsProtocolVersion)
	cmds = putCmd(cmds, cmdReceiveWindowSize, defaultReceiveWindow)
	cmds = putCmd(cmds, cmdConnectionTimeout, connectionTimeoutMS)
	return dcerpc.EncodeRTS(rtsFlagNone, 3, cmds)
}
