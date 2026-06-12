// Package nspi implements the binary NSPI address-book connector (MS-OXNSPI)
// over the MS-OXCMAPIHTTP AddressBook transport. It serves the global address
// list to Outlook from the same canonical directory the EWS ResolveNames and
// webmail GAL surfaces use, replacing the earlier non-spec JSON bridge.
package nspi

import "github.com/umailserver/umailserver/internal/mapi/wire"

// Stat is the NSPI state block (MS-OXNSPI 2.2.8): it carries which address-book
// table the client is positioned in and where, plus its locale. Every field is a
// 32-bit value, so the block is a fixed 36 bytes.
type Stat struct {
	SortType       uint32
	ContainerID    uint32
	CurrentRec     uint32
	Delta          int32
	NumPos         uint32
	TotalRec       uint32
	CodePage       uint32
	TemplateLocale uint32
	SortLocale     uint32
}

// PullStat reads a Stat block.
func PullStat(p *wire.Pull) Stat {
	return Stat{
		SortType:       p.Uint32(),
		ContainerID:    p.Uint32(),
		CurrentRec:     p.Uint32(),
		Delta:          int32(p.Uint32()),
		NumPos:         p.Uint32(),
		TotalRec:       p.Uint32(),
		CodePage:       p.Uint32(),
		TemplateLocale: p.Uint32(),
		SortLocale:     p.Uint32(),
	}
}

// Push writes a Stat block.
func (s Stat) Push(p *wire.Push) {
	p.Uint32(s.SortType)
	p.Uint32(s.ContainerID)
	p.Uint32(s.CurrentRec)
	p.Uint32(uint32(s.Delta))
	p.Uint32(s.NumPos)
	p.Uint32(s.TotalRec)
	p.Uint32(s.CodePage)
	p.Uint32(s.TemplateLocale)
	p.Uint32(s.SortLocale)
}
