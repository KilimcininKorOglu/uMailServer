package msg

import (
	"encoding/binary"
	"testing"
	"unicode/utf16"
)

// buildMiniCFB synthesizes a minimal but spec-faithful v3 (512-byte sector)
// compound file holding two small streams ("alpha", "beta") in the mini stream,
// as direct children of the root. It is written independently of the reader so
// the round-trip test is not self-referential on a shared code path. Layout:
//
//	header (512) | sector0=directory | sector1=mini stream | sector2=mini-FAT | sector3=FAT
func buildMiniCFB(alpha, beta []byte) []byte {
	const sectorSize = 512
	buf := make([]byte, 512+4*sectorSize)

	// --- Header ---
	copy(buf[0:8], cfbSig)
	binary.LittleEndian.PutUint16(buf[26:28], 3)      // major version
	binary.LittleEndian.PutUint16(buf[28:30], 0xFFFE) // byte order
	binary.LittleEndian.PutUint16(buf[30:32], 9)      // sector shift -> 512
	binary.LittleEndian.PutUint16(buf[32:34], 6)      // mini sector shift -> 64
	binary.LittleEndian.PutUint32(buf[44:48], 1)      // number of FAT sectors
	binary.LittleEndian.PutUint32(buf[48:52], 0)      // first directory sector
	binary.LittleEndian.PutUint32(buf[56:60], 4096)   // mini stream cutoff
	binary.LittleEndian.PutUint32(buf[60:64], 2)      // first mini-FAT sector
	binary.LittleEndian.PutUint32(buf[64:68], 1)      // number of mini-FAT sectors
	binary.LittleEndian.PutUint32(buf[68:72], endOfChain)
	binary.LittleEndian.PutUint32(buf[72:76], 0) // number of DIFAT sectors
	// DIFAT array: entry 0 points at the FAT sector (sector 3); the rest free.
	binary.LittleEndian.PutUint32(buf[76:80], 3)
	for i := 1; i < 109; i++ {
		binary.LittleEndian.PutUint32(buf[76+i*4:80+i*4], freeSect)
	}

	sectorAt := func(n int) []byte { return buf[512+n*sectorSize : 512+(n+1)*sectorSize] }

	// --- Sector 3: FAT (128 entries) ---
	fat := sectorAt(3)
	for i := 0; i < 128; i++ {
		binary.LittleEndian.PutUint32(fat[i*4:i*4+4], freeSect)
	}
	binary.LittleEndian.PutUint32(fat[0:4], endOfChain)  // dir
	binary.LittleEndian.PutUint32(fat[4:8], endOfChain)  // mini stream
	binary.LittleEndian.PutUint32(fat[8:12], endOfChain) // mini-FAT
	binary.LittleEndian.PutUint32(fat[12:16], fatSect)   // the FAT itself

	// --- Sector 2: mini-FAT (128 entries) ---
	mfat := sectorAt(2)
	for i := 0; i < 128; i++ {
		binary.LittleEndian.PutUint32(mfat[i*4:i*4+4], freeSect)
	}
	binary.LittleEndian.PutUint32(mfat[0:4], endOfChain) // alpha (1 mini sector)
	binary.LittleEndian.PutUint32(mfat[4:8], endOfChain) // beta (1 mini sector)

	// --- Sector 1: mini stream (8 mini-sectors of 64) ---
	mini := sectorAt(1)
	copy(mini[0:64], alpha)
	copy(mini[64:128], beta)

	// --- Sector 0: directory (4 entries of 128) ---
	dir := sectorAt(0)
	writeEntry(dir[0:128], "Root Entry", objRoot, noStream, noStream, 1, 1, 512)
	writeEntry(dir[128:256], "alpha", objStream, noStream, 2, noStream, 0, uint64(len(alpha)))
	writeEntry(dir[256:384], "beta", objStream, noStream, noStream, noStream, 1, uint64(len(beta)))
	// entry 3 left zeroed -> objUnknown -> skipped by the reader.
	return buf
}

// writeEntry encodes one 128-byte directory entry.
func writeEntry(e []byte, name string, objType byte, left, right, child, startSect uint32, size uint64) {
	u16 := utf16.Encode([]rune(name))
	for i, c := range u16 {
		binary.LittleEndian.PutUint16(e[i*2:i*2+2], c)
	}
	nameLen := len(u16)*2 + 2 // include the terminating NUL
	binary.LittleEndian.PutUint16(e[64:66], uint16(nameLen))
	e[66] = objType
	binary.LittleEndian.PutUint32(e[68:72], left)
	binary.LittleEndian.PutUint32(e[72:76], right)
	binary.LittleEndian.PutUint32(e[76:80], child)
	binary.LittleEndian.PutUint32(e[116:120], startSect)
	binary.LittleEndian.PutUint64(e[120:128], size)
}

func TestOpenCFB_ReadsStreamsAndChildren(t *testing.T) {
	alpha := []byte("the subject line")
	beta := []byte("the body text of the message")
	r, err := openCFB(buildMiniCFB(alpha, beta))
	if err != nil {
		t.Fatalf("openCFB: %v", err)
	}

	// Root (index 0) has exactly the two streams as children, in tree order.
	kids := r.children(0)
	if len(kids) != 2 {
		t.Fatalf("children(root) = %d entries, want 2", len(kids))
	}
	names := map[string]int{}
	for _, k := range kids {
		names[r.entries[k].name] = k
	}
	if _, ok := names["alpha"]; !ok {
		t.Fatalf("missing child 'alpha'; got %v", names)
	}
	if _, ok := names["beta"]; !ok {
		t.Fatalf("missing child 'beta'; got %v", names)
	}

	got, err := r.streamData(names["alpha"])
	if err != nil {
		t.Fatalf("streamData(alpha): %v", err)
	}
	if string(got) != string(alpha) {
		t.Errorf("alpha = %q, want %q", got, alpha)
	}
	got, err = r.streamData(names["beta"])
	if err != nil {
		t.Fatalf("streamData(beta): %v", err)
	}
	if string(got) != string(beta) {
		t.Errorf("beta = %q, want %q", got, beta)
	}
}

func TestOpenCFB_RejectsNonCompound(t *testing.T) {
	if _, err := openCFB([]byte("not a compound file at all, just text padding......")); err == nil {
		t.Fatal("expected error for non-CFBF input")
	}
	if _, err := openCFB(make([]byte, 16)); err == nil {
		t.Fatal("expected error for too-short input")
	}
}
