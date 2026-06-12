package oab

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/umailserver/umailserver/internal/mapi/wire"
)

// Entry is one Global Address List entry the OAB serializes.
type Entry struct {
	Email       string
	DisplayName string
	ObjectClass string // "User", "Room", "Equipment", "DistributionList", "Contact"
}

// Directory is the GAL source the OAB reads. GAL returns the complete, uncapped
// address book; Sequence returns a monotonic version number that advances when
// the GAL content changes.
type Directory interface {
	GAL() []Entry
	Sequence() uint32
}

// Handler serves the Offline Address Book over HTTP (MS-OXWOAB): the manifest at
// oab.xml, the compressed Full Details file at <seq>.lzx, and the display
// template at lng0409-<seq>.lzx. It builds the files on demand from the live GAL
// and caches the last build per sequence, so the manifest's digests always match
// the bytes served for that sequence.
type Handler struct {
	dir Directory

	mu     sync.Mutex
	cached *bundle
}

// NewHandler returns an OAB handler reading from dir.
func NewHandler(dir Directory) *Handler { return &Handler{dir: dir} }

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

// current returns the OAB build for the directory's current sequence, rebuilding
// only when the sequence advances so the manifest and the files it references
// stay byte-consistent.
func (h *Handler) current() (*bundle, error) {
	seq := h.dir.Sequence()
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.cached != nil && h.cached.sequence == seq {
		return h.cached, nil
	}
	b, err := h.build(seq)
	if err != nil {
		return nil, err
	}
	h.cached = b
	return b, nil
}

// build generates a full OAB bundle for one sequence number.
func (h *Handler) build(seq uint32) (*bundle, error) {
	entries := h.dir.GAL()
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
