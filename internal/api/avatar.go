package api

import (
	"encoding/base64"
	"errors"
	"net/http"
	"strings"
	"time"
)

// maxAvatarBytes caps the stored profile photo size. Avatars live inline in the
// account record, so they must stay small; clients are expected to downscale.
const maxAvatarBytes = 1 << 20 // 1 MiB

// allowedAvatarTypes is the set of image MIME types accepted for profile photos.
var allowedAvatarTypes = map[string]bool{
	"image/png":  true,
	"image/jpeg": true,
	"image/gif":  true,
	"image/webp": true,
}

// parseAvatarDataURL decodes a "data:<mime>;base64,<payload>" URL into its MIME
// type and raw bytes, enforcing the allowed types and size cap.
func parseAvatarDataURL(dataURL string) (string, []byte, error) {
	if !strings.HasPrefix(dataURL, "data:") {
		return "", nil, errors.New("avatar must be a data URL")
	}
	comma := strings.IndexByte(dataURL, ',')
	if comma < 0 {
		return "", nil, errors.New("malformed data URL")
	}
	header := dataURL[len("data:"):comma]
	if !strings.Contains(header, ";base64") {
		return "", nil, errors.New("avatar must be base64-encoded")
	}
	mime := strings.ToLower(strings.TrimSpace(strings.TrimSuffix(header, ";base64")))
	if !allowedAvatarTypes[mime] {
		return "", nil, errors.New("unsupported image type")
	}
	raw, err := base64.StdEncoding.DecodeString(dataURL[comma+1:])
	if err != nil {
		return "", nil, errors.New("invalid base64 data")
	}
	if len(raw) == 0 {
		return "", nil, errors.New("empty image")
	}
	if len(raw) > maxAvatarBytes {
		return "", nil, errors.New("image exceeds the 1 MB limit")
	}
	return mime, raw, nil
}

// handleAvatarGet serves a user's profile photo. Any authenticated caller may
// fetch an avatar within their own domain (the GAL scope), so colleagues' photos
// can be shown in the directory, availability views, and message headers.
// GET /api/v1/avatar?email=<email> (email defaults to the caller).
func (s *Server) handleAvatarGet(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.sendError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	caller, ok := r.Context().Value("user").(string)
	if !ok || caller == "" {
		s.sendError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	email := strings.TrimSpace(r.URL.Query().Get("email"))
	if email == "" {
		email = caller
	}
	user, domain := parseEmail(email)
	_, callerDomain := parseEmail(caller)
	// Restrict to the caller's own domain, matching the directory's GAL scope.
	if !strings.EqualFold(domain, callerDomain) {
		s.sendError(w, http.StatusForbidden, "access denied")
		return
	}
	account, err := s.db.GetAccount(domain, user)
	if err != nil || account == nil || len(account.Avatar) == 0 {
		s.sendError(w, http.StatusNotFound, "no avatar")
		return
	}
	contentType := account.AvatarType
	if contentType == "" {
		contentType = "image/png"
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "private, max-age=300")
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(account.Avatar); err != nil {
		s.logger.Warn("failed to write avatar response", "email", email, "error", err)
	}
}

// handleProfileAvatar lets a signed-in user manage their own profile photo.
// PUT  /api/v1/profile/avatar  body {"avatar": "data:image/png;base64,..."}
// DELETE /api/v1/profile/avatar removes it.
func (s *Server) handleProfileAvatar(w http.ResponseWriter, r *http.Request) {
	caller, ok := r.Context().Value("user").(string)
	if !ok || caller == "" {
		s.sendError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	user, domain := parseEmail(caller)
	account, err := s.db.GetAccount(domain, user)
	if err != nil || account == nil {
		s.sendError(w, http.StatusNotFound, "account not found")
		return
	}

	switch r.Method {
	case http.MethodPut:
		var req struct {
			Avatar string `json:"avatar"`
		}
		if err := decodeJSON(r, &req); err != nil {
			s.sendError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		mime, raw, perr := parseAvatarDataURL(req.Avatar)
		if perr != nil {
			s.sendError(w, http.StatusBadRequest, perr.Error())
			return
		}
		account.Avatar = raw
		account.AvatarType = mime
		account.UpdatedAt = time.Now()
		if err := s.db.UpdateAccount(account); err != nil {
			s.sendError(w, http.StatusInternalServerError, "failed to save avatar")
			return
		}
		s.sendJSON(w, http.StatusOK, map[string]any{"has_avatar": true})
	case http.MethodDelete:
		account.Avatar = nil
		account.AvatarType = ""
		account.UpdatedAt = time.Now()
		if err := s.db.UpdateAccount(account); err != nil {
			s.sendError(w, http.StatusInternalServerError, "failed to remove avatar")
			return
		}
		s.sendJSON(w, http.StatusOK, map[string]any{"has_avatar": false})
	default:
		s.sendError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}
