package api

import (
	"crypto/sha256"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/umailserver/umailserver/internal/auth"
	"github.com/umailserver/umailserver/internal/db"
)

// smimeCertInfo is the GET response shape for certificate metadata.
type smimeCertInfo struct {
	Subject     string `json:"subject"`
	Issuer      string `json:"issuer"`
	NotBefore   string `json:"notBefore"`
	NotAfter    string `json:"notAfter"`
	SerialNum   string `json:"serialNumber"`
	Fingerprint string `json:"fingerprint"`
	HasPrivateKey bool `json:"hasPrivateKey"`
}

// smimeUploadRequest is the POST body for uploading a certificate and private key.
type smimeUploadRequest struct {
	Cert string `json:"cert"` // PEM-encoded certificate (required)
	Key  string `json:"key"`  // PEM-encoded private key (required for signing)
}

// handleSMIMECertificate handles GET (fetch metadata), POST (upload), and
// DELETE (remove) on /api/v1/smime/certificate.
func (s *Server) handleSMIMECertificate(w http.ResponseWriter, r *http.Request) {
	user := r.Context().Value("user")
	userEmail, ok := user.(string)
	if !ok {
		s.sendError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}

	switch r.Method {
	case http.MethodGet:
		s.getSMIMECertificate(w, r, userEmail)
	case http.MethodPost:
		s.uploadSMIMECertificate(w, r, userEmail)
	case http.MethodDelete:
		s.deleteSMIMECertificate(w, r, userEmail)
	default:
		s.sendError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// getSMIMECertificate returns the stored certificate metadata for the user.
func (s *Server) getSMIMECertificate(w http.ResponseWriter, r *http.Request, userEmail string) {
	certPEM, _, err := s.db.GetSMIMEKeys(userEmail)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			s.sendJSON(w, http.StatusOK, map[string]bool{"hasKeys": false})
			return
		}
		s.sendError(w, http.StatusInternalServerError, "failed to load certificate")
		return
	}
	info, err := parseCertInfo(certPEM, true)
	if err != nil {
		s.sendError(w, http.StatusInternalServerError, "failed to parse certificate")
		return
	}
	s.sendJSON(w, http.StatusOK, info)
}

// uploadSMIMECertificate stores the user's S/MIME certificate and private key.
func (s *Server) uploadSMIMECertificate(w http.ResponseWriter, r *http.Request, userEmail string) {
	var req smimeUploadRequest
	if err := decodeJSON(r, &req); err != nil {
		s.sendError(w, http.StatusBadRequest, "invalid request")
		return
	}
	if req.Cert == "" || req.Key == "" {
		s.sendError(w, http.StatusBadRequest, "cert and key are required")
		return
	}
	// Validate certificate PEM block
	block, _ := pem.Decode([]byte(req.Cert))
	if block == nil {
		s.sendError(w, http.StatusBadRequest, "invalid certificate PEM")
		return
	}
	_, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		s.sendError(w, http.StatusBadRequest, "invalid certificate: "+err.Error())
		return
	}
	// Validate key PEM block
	keyBlock, _ := pem.Decode([]byte(req.Key))
	if keyBlock == nil {
		s.sendError(w, http.StatusBadRequest, "invalid key PEM")
		return
	}
	if err := s.db.SetSMIMEKeys(userEmail, req.Cert, req.Key); err != nil {
		s.sendError(w, http.StatusInternalServerError, "failed to store keys")
		return
	}
	info, err := parseCertInfo(req.Cert, true)
	if err != nil {
		s.sendError(w, http.StatusInternalServerError, "failed to parse certificate")
		return
	}
	s.sendJSON(w, http.StatusOK, info)
}

// deleteSMIMECertificate removes the user's stored S/MIME keys.
func (s *Server) deleteSMIMECertificate(w http.ResponseWriter, r *http.Request, userEmail string) {
	if err := s.db.DeleteSMIMEKeys(userEmail); err != nil {
		s.sendError(w, http.StatusInternalServerError, "failed to delete certificate")
		return
	}
	s.sendJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// smimeVerifyRequest is the POST body for verifying a signed message.
type smimeVerifyRequest struct {
	Message string `json:"message"` // raw MIME message to verify
}

// handleSMIMEVerify handles POST /api/v1/smime/verify.
func (s *Server) handleSMIMEVerify(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.sendError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req smimeVerifyRequest
	if err := decodeJSON(r, &req); err != nil {
		s.sendError(w, http.StatusBadRequest, "invalid request")
		return
	}
	verifier := auth.NewSMIMEVerifier()
	valid, err := verifier.VerifyMessage([]byte(req.Message))
	if err != nil {
		s.sendJSON(w, http.StatusOK, map[string]interface{}{
			"valid": false,
			"error": err.Error(),
		})
		return
	}
	s.sendJSON(w, http.StatusOK, map[string]interface{}{
		"valid": valid,
	})
}

// smimeDecryptRequest is the POST body for decrypting an encrypted message.
type smimeDecryptRequest struct {
	Message  string `json:"message"`  // raw encrypted MIME message
	User     string `json:"user"`    // email of the user whose key to use
}

// handleSMIMEDecrypt handles POST /api/v1/smime/decrypt.
func (s *Server) handleSMIMEDecrypt(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.sendError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	user := r.Context().Value("user")
	userEmail, ok := user.(string)
	if !ok {
		s.sendError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	var req smimeDecryptRequest
	if err := decodeJSON(r, &req); err != nil {
		s.sendError(w, http.StatusBadRequest, "invalid request")
		return
	}
	// Use the authenticated user's key, or the specified user if authorized
	targetUser := userEmail
	if req.User != "" && req.User != userEmail {
		// TODO: add admin check for decrypting other users' mail
		s.sendError(w, http.StatusForbidden, "cannot decrypt for other users")
		return
	}
	certPEM, keyPEM, err := s.db.GetSMIMEKeys(targetUser)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			s.sendError(w, http.StatusNotFound, "no S/MIME keys found for user")
			return
		}
		s.sendError(w, http.StatusInternalServerError, "failed to load keys")
		return
	}
	cfg, err := buildSMIMEConfig(certPEM, keyPEM, nil)
	if err != nil {
		s.sendError(w, http.StatusBadRequest, err.Error())
		return
	}
	decryptor := auth.NewSMIMEDecryptor(cfg)
	plain, err := decryptor.DecryptMessage([]byte(req.Message))
	if err != nil {
		s.sendError(w, http.StatusBadRequest, "decryption failed: "+err.Error())
		return
	}
	s.sendJSON(w, http.StatusOK, map[string]string{
		"message": string(plain),
	})
}

// parseCertInfo extracts metadata from a PEM-encoded certificate.
func parseCertInfo(certPEM string, hasPrivateKey bool) (*smimeCertInfo, error) {
	block, _ := pem.Decode([]byte(certPEM))
	if block == nil {
		return nil, &certError{"invalid PEM"}
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, err
	}
	return &smimeCertInfo{
		Subject:       cert.Subject.String(),
		Issuer:       cert.Issuer.String(),
		NotBefore:    cert.NotBefore.Format(time.RFC3339),
		NotAfter:     cert.NotAfter.Format(time.RFC3339),
		SerialNum:    cert.SerialNumber.String(),
		Fingerprint:   fingerprint(cert),
		HasPrivateKey: hasPrivateKey,
	}, nil
}

// buildSMIMEConfig constructs an auth.SMIMEConfig from PEM strings.
// encCerts may be nil for signing-only operations.
func buildSMIMEConfig(certPEM, keyPEM string, encCerts []*x509.Certificate) (*auth.SMIMEConfig, error) {
	block, _ := pem.Decode([]byte(certPEM))
	if block == nil {
		return nil, &certError{"invalid cert PEM"}
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, err
	}
	keyBlock, _ := pem.Decode([]byte(keyPEM))
	if keyBlock == nil {
		return nil, &certError{"invalid key PEM"}
	}
	key, err := auth.ParsePrivateKey(keyBlock.Bytes)
	if err != nil {
		return nil, err
	}
	return &auth.SMIMEConfig{
		SigningCert:     cert,
		SigningKey:      key,
		EncryptionCerts: encCerts,
	}, nil
}

type certError struct{ msg string }

func (e *certError) Error() string { return e.msg }

// fingerprint returns the SHA-256 fingerprint of a certificate in colon-separated hex.
func fingerprint(cert *x509.Certificate) string {
	sum := sha256.Sum256(cert.Raw)
	var b strings.Builder
	for i, c := range sum {
		if i > 0 {
			b.WriteByte(':')
		}
		fmt.Fprintf(&b, "%02X", c)
	}
	return b.String()
}
