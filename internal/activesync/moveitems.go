package activesync

import (
	"errors"

	"github.com/umailserver/umailserver/internal/activesync/wbxml"
)

// MoveItems status codes (MS-ASCMD 2.2.3.177.10), reported per Move in the
// response. Note that success is 3, not 1 as in the Sync command.
const (
	moveStatusInvalidSource = "1" // source item or collection id is invalid/gone
	moveStatusSuccess       = "3"
	moveStatusSameFolders   = "4" // source and destination collections are the same
	moveStatusServerError   = "5"
)

// handleMoveItems relocates messages between collections (MS-ASCMD MoveItems).
// Each Move carries the source item id and the source/destination collection ids;
// the response echoes one Response per Move with its status and, on success, the
// destination item id. The EAS item id is the storage blob key, which is content-
// derived and so unchanged by a move, so the destination id equals the source id.
func (s *Server) handleMoveItems(ctx *Context) ([]byte, error) {
	if s.mutator == nil {
		return nil, errors.New("activesync: mutator not configured")
	}
	root, err := wbxml.Unmarshal(ctx.Body)
	if err != nil {
		return nil, err
	}
	resp := &wbxml.Element{Page: wbxml.PageMove, Name: "MoveItems"}
	for _, mv := range root.Children {
		if mv.Name != "Move" {
			continue
		}
		sid := textOf(mv.Sub("SrcMsgId"))
		status, dstID := s.applyMove(ctx.Email, textOf(mv.Sub("SrcFldId")), textOf(mv.Sub("DstFldId")), sid)
		children := []*wbxml.Element{
			{Page: wbxml.PageMove, Name: "SrcMsgId", Text: sid},
			{Page: wbxml.PageMove, Name: "Status", Text: status},
		}
		if dstID != "" {
			children = append(children, &wbxml.Element{Page: wbxml.PageMove, Name: "DstMsgId", Text: dstID})
		}
		resp.Children = append(resp.Children, &wbxml.Element{Page: wbxml.PageMove, Name: "Response", Children: children})
	}
	return wbxml.Marshal(resp)
}

// applyMove performs one move and maps the outcome to a MoveItems status; the
// destination id (the unchanged blob key) is returned only on success.
func (s *Server) applyMove(email, src, dst, sid string) (status, dstID string) {
	switch {
	case sid == "" || src == "" || dst == "":
		return moveStatusInvalidSource, ""
	case src == dst:
		return moveStatusSameFolders, ""
	}
	moved, err := s.mutator.Move(email, src, dst, sid)
	switch {
	case err != nil:
		return moveStatusServerError, ""
	case !moved:
		return moveStatusInvalidSource, ""
	default:
		return moveStatusSuccess, sid
	}
}
