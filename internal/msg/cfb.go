// Package msg imports Outlook .msg files (the OXMSG message format) into raw
// RFC 5322 byte blobs the canonical store can file, so a saved Outlook message
// becomes a normal message visible across IMAP/POP3/JMAP/EWS/webmail.
//
// An .msg file is a Compound File Binary Format (CFBF / OLE2, MS-CFB) container
// whose streams hold MAPI properties per MS-OXMSG. This file implements the
// minimal CFBF reader: it parses the header, the FAT/mini-FAT sector chains, and
// the directory tree, and exposes stream bytes by directory entry. It is a
// read-only decoder — it never writes CFBF.
package msg

import (
	"encoding/binary"
	"errors"
	"fmt"
	"unicode/utf16"
)

// CFBF special FAT sector values (MS-CFB §2.2). A chain ends at endOfChain; any
// value <= maxRegSect is a real sector index.
const (
	maxRegSect = 0xFFFFFFFA
	difSect    = 0xFFFFFFFC
	fatSect    = 0xFFFFFFFD
	endOfChain = 0xFFFFFFFE
	freeSect   = 0xFFFFFFFF
	noStream   = 0xFFFFFFFF // a directory entry's missing sibling/child id
)

// Directory entry object types (MS-CFB §2.6.1).
const (
	objUnknown = 0
	objStorage = 1
	objStream  = 2
	objRoot    = 5
)

// cfbSig is the 8-byte CFBF header signature (MS-CFB §2.2).
var cfbSig = []byte{0xD0, 0xCF, 0x11, 0xE0, 0xA1, 0xB1, 0x1A, 0xE1}

// dirEntry is one parsed directory entry (a storage, a stream, or the root).
type dirEntry struct {
	name      string
	objType   byte
	left      uint32 // left sibling id in the red-black tree (noStream = none)
	right     uint32 // right sibling id
	child     uint32 // first child id (storages/root only)
	startSect uint32 // starting sector of the stream/mini-stream
	size      uint64 // stream size in bytes
}

// cfbReader is a parsed CFBF container ready for stream reads.
type cfbReader struct {
	data       []byte
	sectorSize int
	miniShift  uint   // mini sector size = 1<<miniShift
	miniCutoff uint32 // streams smaller than this live in the mini stream
	fat        []uint32
	miniFAT    []uint32
	entries    []dirEntry
	miniStream []byte // the root entry's stream, container of all mini sectors
}

// openCFB parses a CFBF container from b. It validates the signature and reads
// the FAT, directory, and mini-FAT so streams can be read by directory index.
func openCFB(b []byte) (*cfbReader, error) {
	if len(b) < 512 {
		return nil, errors.New("msg: not a compound file (too short)")
	}
	if string(b[:8]) != string(cfbSig) {
		return nil, errors.New("msg: not a compound file (bad signature)")
	}

	// Header fields (little-endian) per MS-CFB §2.2.
	sectorShift := binary.LittleEndian.Uint16(b[30:32])
	sectorSize := 1 << sectorShift
	if sectorSize != 512 && sectorSize != 4096 {
		return nil, fmt.Errorf("msg: unsupported sector size shift %d", sectorShift)
	}
	r := &cfbReader{
		data:       b,
		sectorSize: sectorSize,
		miniShift:  uint(binary.LittleEndian.Uint16(b[32:34])),
		miniCutoff: binary.LittleEndian.Uint32(b[56:60]),
	}
	numFATSect := binary.LittleEndian.Uint32(b[44:48])
	firstDirSect := binary.LittleEndian.Uint32(b[48:52])
	firstMiniFATSect := binary.LittleEndian.Uint32(b[60:64])
	numMiniFATSect := binary.LittleEndian.Uint32(b[64:68])
	firstDIFATSect := binary.LittleEndian.Uint32(b[68:72])
	numDIFATSect := binary.LittleEndian.Uint32(b[72:76])

	if err := r.readFAT(firstDIFATSect, numDIFATSect, numFATSect); err != nil {
		return nil, err
	}
	if err := r.readMiniFAT(firstMiniFATSect, numMiniFATSect); err != nil {
		return nil, err
	}
	if err := r.readDirectory(firstDirSect); err != nil {
		return nil, err
	}
	// The root entry's stream is the mini-stream container; read it via the FAT
	// (it is always a regular-sector chain regardless of its size).
	if len(r.entries) == 0 {
		return nil, errors.New("msg: empty directory")
	}
	root := r.entries[0]
	miniStream, err := r.readChain(root.startSect, root.size)
	if err != nil {
		return nil, fmt.Errorf("msg: read mini stream: %w", err)
	}
	r.miniStream = miniStream
	return r, nil
}

// sector returns the byte slice of regular sector n, or an error when out of
// range. Sector n begins at file offset (n+1)*sectorSize (sector 0 follows the
// 512-byte header).
func (r *cfbReader) sector(n uint32) ([]byte, error) {
	off := (int(n) + 1) * r.sectorSize
	if off < 0 || off+r.sectorSize > len(r.data) {
		return nil, fmt.Errorf("msg: sector %d out of range", n)
	}
	return r.data[off : off+r.sectorSize], nil
}

// readFAT assembles the File Allocation Table from the DIFAT: the first 109 FAT
// sector locations live in the header, the rest in the DIFAT sector chain.
func (r *cfbReader) readFAT(firstDIFATSect, numDIFATSect, numFATSect uint32) error {
	fatSectors := make([]uint32, 0, numFATSect)
	for i := 0; i < 109; i++ {
		off := 76 + i*4
		s := binary.LittleEndian.Uint32(r.data[off : off+4])
		if s <= maxRegSect {
			fatSectors = append(fatSectors, s)
		}
	}
	// DIFAT continuation sectors (rare for small .msg): each holds (sectorSize/4 - 1)
	// FAT locations plus a trailing pointer to the next DIFAT sector.
	next := firstDIFATSect
	for i := uint32(0); i < numDIFATSect && next <= maxRegSect; i++ {
		sec, err := r.sector(next)
		if err != nil {
			return err
		}
		perSector := r.sectorSize/4 - 1
		for j := 0; j < perSector; j++ {
			s := binary.LittleEndian.Uint32(sec[j*4 : j*4+4])
			if s <= maxRegSect {
				fatSectors = append(fatSectors, s)
			}
		}
		next = binary.LittleEndian.Uint32(sec[perSector*4 : perSector*4+4])
	}

	r.fat = make([]uint32, 0, len(fatSectors)*(r.sectorSize/4))
	for _, fs := range fatSectors {
		sec, err := r.sector(fs)
		if err != nil {
			return err
		}
		for j := 0; j < r.sectorSize; j += 4 {
			r.fat = append(r.fat, binary.LittleEndian.Uint32(sec[j:j+4]))
		}
	}
	return nil
}

// readMiniFAT assembles the mini-FAT (the allocation table for the mini stream)
// from its own regular-sector chain.
func (r *cfbReader) readMiniFAT(first, count uint32) error {
	r.miniFAT = make([]uint32, 0, count*uint32(r.sectorSize/4))
	sect := first
	for i := uint32(0); i < count && sect <= maxRegSect; i++ {
		sec, err := r.sector(sect)
		if err != nil {
			return err
		}
		for j := 0; j < r.sectorSize; j += 4 {
			r.miniFAT = append(r.miniFAT, binary.LittleEndian.Uint32(sec[j:j+4]))
		}
		sect = r.nextFAT(sect)
	}
	return nil
}

// nextFAT returns the next sector in a regular-sector chain, or endOfChain when
// the index is past the table.
func (r *cfbReader) nextFAT(sect uint32) uint32 {
	if int(sect) >= len(r.fat) {
		return endOfChain
	}
	return r.fat[sect]
}

// readChain reads a regular-sector chain starting at start, truncated to size
// bytes. A guard bounds the hop count to the table length to stop a cyclic FAT.
func (r *cfbReader) readChain(start uint32, size uint64) ([]byte, error) {
	var out []byte
	sect := start
	for hops := 0; sect <= maxRegSect && hops <= len(r.fat); hops++ {
		sec, err := r.sector(sect)
		if err != nil {
			return nil, err
		}
		out = append(out, sec...)
		sect = r.nextFAT(sect)
	}
	if uint64(len(out)) < size {
		return nil, fmt.Errorf("msg: chain shorter (%d) than declared size (%d)", len(out), size)
	}
	return out[:size], nil
}

// readDirectory parses the directory entries from their regular-sector chain.
// Each entry is a fixed 128 bytes (MS-CFB §2.6.1).
func (r *cfbReader) readDirectory(first uint32) error {
	raw, err := r.readChainAll(first)
	if err != nil {
		return fmt.Errorf("msg: read directory: %w", err)
	}
	for off := 0; off+128 <= len(raw); off += 128 {
		e := raw[off : off+128]
		nameLen := int(binary.LittleEndian.Uint16(e[64:66]))
		objType := e[66]
		if objType == objUnknown {
			continue
		}
		// Name is UTF-16LE including a terminating NUL counted in nameLen.
		var name string
		if nameLen >= 2 {
			u16 := make([]uint16, 0, nameLen/2)
			for i := 0; i+1 < nameLen; i += 2 {
				u16 = append(u16, binary.LittleEndian.Uint16(e[i:i+2]))
			}
			name = trimNUL(string(utf16.Decode(u16)))
		}
		r.entries = append(r.entries, dirEntry{
			name:      name,
			objType:   objType,
			left:      binary.LittleEndian.Uint32(e[68:72]),
			right:     binary.LittleEndian.Uint32(e[72:76]),
			child:     binary.LittleEndian.Uint32(e[76:80]),
			startSect: binary.LittleEndian.Uint32(e[116:120]),
			size:      binary.LittleEndian.Uint64(e[120:128]),
		})
	}
	if len(r.entries) == 0 || r.entries[0].objType != objRoot {
		return errors.New("msg: directory has no root entry")
	}
	return nil
}

// readChainAll reads a full regular-sector chain (no size truncation), used for
// the directory whose length is implied by its chain.
func (r *cfbReader) readChainAll(start uint32) ([]byte, error) {
	var out []byte
	sect := start
	for hops := 0; sect <= maxRegSect && hops <= len(r.fat); hops++ {
		sec, err := r.sector(sect)
		if err != nil {
			return nil, err
		}
		out = append(out, sec...)
		sect = r.nextFAT(sect)
	}
	return out, nil
}

// streamData returns the bytes of the stream at directory index idx. Streams
// smaller than the mini cutoff are stored in the mini stream via the mini-FAT;
// larger ones in regular sectors via the FAT.
func (r *cfbReader) streamData(idx int) ([]byte, error) {
	if idx < 0 || idx >= len(r.entries) {
		return nil, fmt.Errorf("msg: entry %d out of range", idx)
	}
	e := r.entries[idx]
	if e.objType != objStream {
		return nil, fmt.Errorf("msg: entry %q is not a stream", e.name)
	}
	if e.size >= uint64(r.miniCutoff) {
		return r.readChain(e.startSect, e.size)
	}
	return r.readMiniChain(e.startSect, e.size)
}

// readMiniChain reads a mini-sector chain from the mini stream, truncated to
// size. Mini sector n begins at offset n<<miniShift within the mini stream.
func (r *cfbReader) readMiniChain(start uint32, size uint64) ([]byte, error) {
	miniSize := 1 << r.miniShift
	var out []byte
	sect := start
	for hops := 0; sect <= maxRegSect && hops <= len(r.miniFAT); hops++ {
		off := int(sect) << r.miniShift
		if off < 0 || off+miniSize > len(r.miniStream) {
			return nil, fmt.Errorf("msg: mini sector %d out of range", sect)
		}
		out = append(out, r.miniStream[off:off+miniSize]...)
		if int(sect) >= len(r.miniFAT) {
			break
		}
		sect = r.miniFAT[sect]
	}
	if uint64(len(out)) < size {
		return nil, fmt.Errorf("msg: mini chain shorter (%d) than declared size (%d)", len(out), size)
	}
	return out[:size], nil
}

// children returns the directory indexes of the direct children of the storage
// (or root) at index idx, by an in-order walk of the red-black sibling tree.
func (r *cfbReader) children(idx int) []int {
	if idx < 0 || idx >= len(r.entries) {
		return nil
	}
	var out []int
	seen := make(map[uint32]bool)
	var walk func(id uint32)
	walk = func(id uint32) {
		if id == noStream || int(id) >= len(r.entries) || seen[id] {
			return
		}
		seen[id] = true
		e := r.entries[id]
		walk(e.left)
		out = append(out, int(id))
		walk(e.right)
	}
	walk(r.entries[idx].child)
	return out
}

// trimNUL drops a trailing NUL left by a UTF-16 name's terminator.
func trimNUL(s string) string {
	for len(s) > 0 && s[len(s)-1] == 0 {
		s = s[:len(s)-1]
	}
	return s
}
