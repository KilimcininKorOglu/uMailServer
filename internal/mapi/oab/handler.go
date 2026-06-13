package oab

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/umailserver/umailserver/internal/mapi/wire"
)

// Entry is one Global Address List entry the OAB serializes.
type Entry struct {
	Email       string
	DisplayName string
	ObjectClass string // "User", "Room", "Equipment", "DistributionList", "Contact"
}

// Directory is the GAL source the OAB reads: the complete, uncapped address book.
// The handler derives the OAB sequence from the content itself, so a directory
// only needs to expose its current entries.
type Directory interface {
	GAL() []Entry
}

// Handler serves the Offline Address Book over HTTP (MS-OXWOAB): the manifest at
// oab.xml, the compressed Full Details file at <seq>.lzx, and the display
// template at lng0409-<seq>.lzx. It builds the files on demand from the live GAL
// and caches the last build, keyed by a hash of the visible GAL, so the
// manifest's digests always match the bytes served and a content change (an add,
// edit, removal, or a hidden recipient) is reflected immediately.
type Handler struct {
	dir Directory

	mu     sync.Mutex
	cached *cacheEntry
}

// NewHandler returns an OAB handler reading from dir.
func NewHandler(dir Directory) *Handler { return &Handler{dir: dir} }

// cacheEntry is the last OAB generation together with the content hash it was
// built from, so an unchanged directory returns identical bytes and a changed
// one triggers a rebuild.
type cacheEntry struct {
	hash   [32]byte
	bundle *bundle
}

// bundle is one consistent OAB generation: the manifest plus the compressed
// files it references, all for one sequence number.
type bundle struct {
	sequence uint32
	manifest string
	full     []byte
	template []byte
}

// ServeHTTP routes an OAB request by the file named after the mount prefix.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	file := r.URL.Path
	if i := strings.LastIndexByte(file, '/'); i >= 0 {
		file = file[i+1:]
	}

	b, err := h.current()
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	switch {
	case strings.EqualFold(file, "oab.xml"):
		h.write(w, r, "text/xml; charset=utf-8", []byte(b.manifest))
	case file == FullFileName(b.sequence):
		h.write(w, r, "application/octet-stream", b.full)
	case file == TemplateFileName(b.sequence):
		h.write(w, r, "application/octet-stream", b.template)
	default:
		// A stale sequence (the GAL changed since the manifest was fetched) or an
		// unknown file: 404 makes Outlook re-fetch the manifest.
		http.NotFound(w, r)
	}
}

func (h *Handler) write(w http.ResponseWriter, r *http.Request, contentType string, body []byte) {
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodHead {
		return
	}
	if _, err := w.Write(body); err != nil {
		return
	}
}

// current returns the OAB build for the directory's current contents, rebuilding
// only when the visible GAL changes so the manifest and the files it references
// stay byte-consistent.
func (h *Handler) current() (*bundle, error) {
	entries := h.dir.GAL()
	hash := hashEntries(entries)

	h.mu.Lock()
	defer h.mu.Unlock()
	if h.cached != nil && h.cached.hash == hash {
		return h.cached.bundle, nil
	}

	// The content changed: assign a new sequence. Use the wall clock but never
	// let it move backwards — a content revert (such as un-hiding a recipient)
	// would otherwise pick a lower value — so Outlook always sees a higher
	// sequence after any change and re-downloads the OAB.
	//
	// The sequence and the in-memory build are per process. In multi-node HA a
	// client must fetch the manifest and the files it names from one node, so the
	// load balancer pins an OAB download by source affinity (see
	// config/haproxy.cfg, backend be_http).
	seq := uint32(time.Now().Unix()) & 0x7FFFFFFF
	if h.cached != nil && seq <= h.cached.bundle.sequence {
		seq = h.cached.bundle.sequence + 1
	}
	b, err := h.build(seq, entries)
	if err != nil {
		return nil, err
	}
	h.cached = &cacheEntry{hash: hash, bundle: b}
	return b, nil
}

// hashEntries returns a content hash of the visible GAL that changes whenever any
// entry is added, removed, hidden, or edited. Each field is length-prefixed so
// two distinct entry lists can never hash alike.
func hashEntries(entries []Entry) [32]byte {
	h := sha256.New()
	var n [8]byte
	for _, e := range entries {
		for _, field := range []string{e.Email, e.DisplayName, e.ObjectClass} {
			binary.LittleEndian.PutUint64(n[:], uint64(len(field)))
			h.Write(n[:])
			h.Write([]byte(field))
		}
	}
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

// build generates a full OAB bundle for one sequence number from the given GAL
// entries.
func (h *Handler) build(seq uint32, entries []Entry) (*bundle, error) {
	guid := containerGUID()
	records := make([]Record, len(entries))
	for i, e := range entries {
		records[i] = Record{
			X500DN:      wire.BuildESSDN(localPart(e.Email)),
			SMTP:        e.Email,
			DisplayName: e.DisplayName,
			ObjectType:  objectType(e.ObjectClass),
			DisplayType: displayType(e.ObjectClass),
		}
	}

	rawFull := BuildFullDetails(records, seq, guid, "/")
	full := Compress(rawFull)
	rawTemplate := BuildTemplate()
	template := Compress(rawTemplate)

	manifest, err := BuildManifest(ManifestInput{
		ContainerGUID:      guid,
		OABDN:              "/",
		Sequence:           seq,
		FullCompressed:     full,
		FullRawSize:        len(rawFull),
		TemplateCompressed: template,
		TemplateRawSize:    len(rawTemplate),
	})
	if err != nil {
		return nil, err
	}
	return &bundle{sequence: seq, manifest: manifest, full: full, template: template}, nil
}

// Object and display types (MS-OXCDATA §2.11.1.5).
const (
	objMailUser uint32 = 6
	objDistList uint32 = 8
	dtMailUser  uint32 = 0
	dtDistList  uint32 = 1
)

func objectType(objectClass string) uint32 {
	if objectClass == "DistributionList" {
		return objDistList
	}
	return objMailUser
}

func displayType(objectClass string) uint32 {
	if objectClass == "DistributionList" {
		return dtDistList
	}
	return dtMailUser
}

// localPart returns the part of an email address before the @.
func localPart(email string) string {
	lp, _, _ := strings.Cut(email, "@")
	return lp
}

// containerGUID returns the stable OAB container GUID, derived from the
// organization's address-book DN so every generation reports the same identity
// across restarts. It is a version-4 UUID string, used verbatim in the manifest
// and the OAB header record.
func containerGUID() string {
	sum := sha256.Sum256([]byte("oab-container:" + wire.BuildESSDN("")))
	b := sum[:16]
	b[6] = (b[6] & 0x0F) | 0x40
	b[8] = (b[8] & 0x3F) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
