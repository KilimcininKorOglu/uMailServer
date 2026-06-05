package api

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// staticCachePolicy returns the Cache-Control value for an embedded static
// asset and whether the response should carry a strong ETag.
//
//   - assets/* are Vite content-hashed bundle filenames (index-<hash>.js); the
//     URL changes whenever the content does, so they are immutable and need no
//     ETag (a conditional request would only waste a round-trip).
//   - index.html (the SPA shell) and sw.js (the PWA service worker) MUST be
//     revalidated on every load so a new deploy is picked up immediately;
//     "no-cache" means "cache but revalidate", paired with a strong ETag.
//   - everything else (favicon, icons, other root files) gets a modest TTL plus
//     an ETag for cheap revalidation.
func staticCachePolicy(name string) (cacheControl string, useETag bool) {
	base := name
	if i := strings.LastIndexByte(base, '/'); i >= 0 {
		base = base[i+1:]
	}
	switch {
	case base == "index.html" || base == "sw.js":
		return "no-cache", true
	case strings.HasPrefix(name, "assets/"):
		return "public, max-age=31536000, immutable", false
	default:
		return "public, max-age=3600", true
	}
}

// serveStaticContent writes an embedded static file with the cache headers from
// staticCachePolicy. For non-immutable files it sets a strong content ETag, and
// http.ServeContent then answers a matching If-None-Match with 304 Not Modified
// (it also handles Range, HEAD, and Content-Type detection when the caller has
// not already set it). A zero modTime is passed because embed.FS files carry no
// meaningful modification time, so revalidation is driven by the ETag, not by
// Last-Modified/If-Modified-Since.
func serveStaticContent(w http.ResponseWriter, r *http.Request, name string, file io.Reader) {
	data, err := io.ReadAll(file)
	if err != nil {
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	cacheControl, useETag := staticCachePolicy(name)
	w.Header().Set("Cache-Control", cacheControl)
	if useETag {
		// Tiny shell files only (index.html/sw.js/favicon/icons); immutable
		// bundles skip this, so per-request hashing stays negligible.
		w.Header().Set("ETag", fmt.Sprintf(`"%x"`, sha256.Sum256(data)))
	}
	http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(data))
}
