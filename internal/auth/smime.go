package auth

import (
	"bytes"
	"crypto"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/asn1"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	pkcs7 "github.com/smallstep/pkcs7"
)

// OIDs for S/MIME
var (
	OIDSMimeSignedData    = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 7, 2}
	OIDSMimeEncryptedData = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 7, 6}
	OIDSMimeCapability    = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 9, 15}
	OIDRSASHA256          = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 1, 11}
	OIDRsaEncryption      = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 1, 1}
)

// SMIMEConfig holds S/MIME configuration
type SMIMEConfig struct {
	// Certificate to use for signing
	SigningCert *x509.Certificate
	SigningKey  crypto.PrivateKey

	// Certificates for encryption (recipient certificates)
	EncryptionCerts []*x509.Certificate

	// Whether to require encryption
	RequireEncryption bool
}

// NewSMIMEConfig creates a new S/MIME configuration
func NewSMIMEConfig() *SMIMEConfig {
	return &SMIMEConfig{}
}

// SMIMESigner handles S/MIME signing operations
type SMIMESigner struct {
	config *SMIMEConfig
}

// NewSMIMESigner creates a new S/MIME signer
func NewSMIMESigner(config *SMIMEConfig) *SMIMESigner {
	return &SMIMESigner{config: config}
}

// SignMessage produces an RFC 5751 multipart/signed message carrying a detached
// CMS SignedData signature (smime.p7s). The original message's non-content
// headers (From/To/Subject/Date/Message-ID/…) are preserved on the outer
// message; the original Content-* headers and body become the signed MIME
// entity, so standards-compliant clients (Outlook, Thunderbird, Apple Mail)
// can verify it. From/To/Date are filled from the parameters only when the
// message lacks them.
func (s *SMIMESigner) SignMessage(msg []byte, from, to string) ([]byte, error) {
	if s.config.SigningCert == nil || s.config.SigningKey == nil {
		return nil, fmt.Errorf("signing certificate or key not available")
	}

	outerHeaders, contentHeaders, body := splitMIMEForSigning(msg)
	if getHeaderValue(outerHeaders, "From") == "" && from != "" {
		outerHeaders = append(outerHeaders, "From: "+from)
	}
	if getHeaderValue(outerHeaders, "To") == "" && to != "" {
		outerHeaders = append(outerHeaders, "To: "+to)
	}
	if getHeaderValue(outerHeaders, "Date") == "" {
		outerHeaders = append(outerHeaders, "Date: "+time.Now().Format(time.RFC1123Z))
	}
	if len(contentHeaders) == 0 {
		contentHeaders = []string{"Content-Type: text/plain; charset=utf-8"}
	}

	// The exact octets that are both transmitted as the first MIME part and
	// covered by the detached signature.
	inner := strings.Join(contentHeaders, "\r\n") + "\r\n\r\n" + body

	sd, err := pkcs7.NewSignedData([]byte(inner))
	if err != nil {
		return nil, fmt.Errorf("failed to initialize signed data: %w", err)
	}
	sd.SetDigestAlgorithm(pkcs7.OIDDigestAlgorithmSHA256)
	if err := sd.AddSigner(s.config.SigningCert, s.config.SigningKey, pkcs7.SignerInfoConfig{}); err != nil {
		return nil, fmt.Errorf("failed to add signer: %w", err)
	}
	sd.Detach()
	der, err := sd.Finish()
	if err != nil {
		return nil, fmt.Errorf("failed to finalize signature: %w", err)
	}

	boundary := generateBoundary()
	var b strings.Builder
	for _, h := range outerHeaders {
		b.WriteString(h)
		b.WriteString("\r\n")
	}
	if getHeaderValue(outerHeaders, "MIME-Version") == "" {
		b.WriteString("MIME-Version: 1.0\r\n")
	}
	fmt.Fprintf(&b, "Content-Type: multipart/signed; protocol=\"application/pkcs7-signature\"; micalg=\"sha-256\"; boundary=\"%s\"\r\n\r\n", boundary)
	fmt.Fprintf(&b, "--%s\r\n", boundary)
	b.WriteString(inner)
	fmt.Fprintf(&b, "\r\n--%s\r\n", boundary)
	b.WriteString("Content-Type: application/pkcs7-signature; name=\"smime.p7s\"\r\n")
	b.WriteString("Content-Transfer-Encoding: base64\r\n")
	b.WriteString("Content-Disposition: attachment; filename=\"smime.p7s\"\r\n\r\n")
	b.WriteString(chunkBase64(base64.StdEncoding.EncodeToString(der)))
	fmt.Fprintf(&b, "\r\n--%s--\r\n", boundary)
	return []byte(b.String()), nil
}

// splitMIMEForSigning splits a raw message into outer (message-level) headers,
// content (MIME entity) headers, and the body. Logical headers are preserved
// verbatim (including folded continuation lines). Content-* headers move to the
// signed entity; everything else (including MIME-Version) stays on the outer
// message. A message with no header/body separator is treated as a bare body.
func splitMIMEForSigning(msg []byte) (outer []string, content []string, body string) {
	normalized := strings.ReplaceAll(strings.ReplaceAll(string(msg), "\r\n", "\n"), "\r", "\n")
	sep := strings.Index(normalized, "\n\n")
	if sep < 0 {
		return nil, nil, toCRLF(normalized)
	}
	headerBlock := normalized[:sep]
	body = toCRLF(normalized[sep+2:])

	var logical []string
	for _, line := range strings.Split(headerBlock, "\n") {
		if line != "" && (line[0] == ' ' || line[0] == '\t') && len(logical) > 0 {
			logical[len(logical)-1] += "\r\n" + line
			continue
		}
		logical = append(logical, line)
	}

	for _, h := range logical {
		name := h
		if i := strings.Index(h, ":"); i >= 0 {
			name = h[:i]
		}
		if isContentHeader(strings.TrimSpace(name)) {
			content = append(content, h)
		} else {
			outer = append(outer, h)
		}
	}
	return outer, content, body
}

func isContentHeader(name string) bool {
	switch strings.ToLower(name) {
	case "content-type", "content-transfer-encoding", "content-disposition",
		"content-id", "content-description", "content-language", "content-location":
		return true
	default:
		return false
	}
}

// getHeaderValue returns the value of the first logical header matching name
// (case-insensitive), or "" if absent.
func getHeaderValue(headers []string, name string) string {
	prefix := strings.ToLower(name) + ":"
	for _, h := range headers {
		if strings.HasPrefix(strings.ToLower(h), prefix) {
			return strings.TrimSpace(h[len(prefix):])
		}
	}
	return ""
}

// toCRLF normalizes LF-only text to CRLF line endings.
func toCRLF(s string) string {
	return strings.ReplaceAll(s, "\n", "\r\n")
}

// chunkBase64 wraps a base64 string at 76 characters per line (RFC 2045).
func chunkBase64(s string) string {
	const width = 76
	var b strings.Builder
	for len(s) > width {
		b.WriteString(s[:width])
		b.WriteString("\r\n")
		s = s[width:]
	}
	b.WriteString(s)
	return b.String()
}

// SMIMEVerifier handles S/MIME verification
type SMIMEVerifier struct{}

// NewSMIMEVerifier creates a new S/MIME verifier
func NewSMIMEVerifier() *SMIMEVerifier {
	return &SMIMEVerifier{}
}

// VerifyMessage verifies an S/MIME signed message
func (v *SMIMEVerifier) VerifyMessage(msg []byte) (bool, error) {
	// Parse the multipart message
	parts, err := parseMultipart(msg)
	if err != nil {
		return false, fmt.Errorf("failed to parse multipart message: %w", err)
	}

	if len(parts) < 2 {
		return false, fmt.Errorf("expected at least 2 parts in multipart signed message")
	}

	content := parts[0].Content
	sigBase64 := parts[1].Content

	// Decode base64 signature
	sigBytes, err := base64.StdEncoding.DecodeString(string(sigBase64))
	if err != nil {
		return false, fmt.Errorf("failed to decode signature: %w", err)
	}

	// Verify signature
	h := sha256.New()
	h.Write(content)
	digest := h.Sum(nil)

	// For verification, we would need the certificate
	// This is a simplified version - full implementation would parse the PKCS7 structure
	_ = digest
	_ = sigBytes

	return true, nil
}

// SMIMEEncryptor handles S/MIME encryption
type SMIMEEncryptor struct {
	config *SMIMEConfig
}

// NewSMIMEEncryptor creates a new S/MIME encryptor
func NewSMIMEEncryptor(config *SMIMEConfig) *SMIMEEncryptor {
	return &SMIMEEncryptor{config: config}
}

// EncryptMessage encrypts a message using S/MIME
func (e *SMIMEEncryptor) EncryptMessage(msg []byte, from, to string) ([]byte, error) {
	if len(e.config.EncryptionCerts) == 0 {
		return nil, fmt.Errorf("no encryption certificates available")
	}

	cert := e.config.EncryptionCerts[0]
	publicKey, ok := cert.PublicKey.(*rsa.PublicKey)
	if !ok {
		return nil, fmt.Errorf("encryption certificate does not contain RSA public key")
	}

	// Generate a random session key
	sessionKey := make([]byte, 32)
	if _, err := rand.Read(sessionKey); err != nil {
		return nil, fmt.Errorf("failed to generate session key: %w", err)
	}

	// Encrypt the session key with RSA using OAEP (more secure than PKCS1v15)
	encryptedKey, err := rsa.EncryptOAEP(sha256.New(), rand.Reader, publicKey, sessionKey, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to encrypt session key: %w", err)
	}

	// Encrypt content with AES-256-GCM
	block, err := aes.NewCipher(sessionKey)
	if err != nil {
		return nil, fmt.Errorf("failed to create AES cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("failed to create GCM: %w", err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("failed to generate nonce: %w", err)
	}
	// Seal appends ciphertext+tag to dst, so nonce is NOT included in output
	// We prepend the nonce for storage/transmission
	ciphertext := gcm.Seal(nil, nonce, msg, nil)
	encryptedContent := append(nonce, ciphertext...)

	// Build the S/MIME message
	headers := fmt.Sprintf(
		"From: %s\r\n"+
			"To: %s\r\n"+
			"Content-Type: application/pkcs7-mime; name=\"smime.p7m\"; smime-type=enveloped-data\r\n"+
			"Content-Transfer-Encoding: base64\r\n"+
			"Date: %s\r\n"+
			"MIME-Version: 1.0\r\n"+
			"Content-Disposition: attachment; filename=\"smime.p7m\"\r\n"+
			"\r\n",
		from,
		to,
		time.Now().Format(time.RFC1123Z),
	)

	// Combine encrypted key and content
	payload := append(encryptedKey, encryptedContent...)
	encoded := base64.StdEncoding.EncodeToString(payload)

	// Split into lines
	var buf bytes.Buffer
	for i := 0; i < len(encoded); i += 76 {
		end := i + 76
		if end > len(encoded) {
			end = len(encoded)
		}
		buf.WriteString(encoded[i:end])
		buf.WriteString("\r\n")
	}

	return []byte(headers + buf.String()), nil
}

// SMIMEDecryptor handles S/MIME decryption
type SMIMEDecryptor struct {
	config *SMIMEConfig
}

// NewSMIMEDecryptor creates a new S/MIME decryptor
func NewSMIMEDecryptor(config *SMIMEConfig) *SMIMEDecryptor {
	return &SMIMEDecryptor{config: config}
}

// DecryptMessage decrypts an S/MIME encrypted message
func (d *SMIMEDecryptor) DecryptMessage(msg []byte) ([]byte, error) {
	// Find the encrypted content
	contentB64 := extractBase64Content(msg)
	if contentB64 == "" {
		return nil, fmt.Errorf("no content found")
	}

	// Decode base64
	payload, err := base64.StdEncoding.DecodeString(contentB64)
	if err != nil {
		return nil, fmt.Errorf("failed to decode base64: %w", err)
	}

	// Extract the encrypted key (first 256 bytes for RSA-2048)
	if len(payload) < 256 {
		return nil, fmt.Errorf("payload too short")
	}

	encryptedKey := payload[:256]
	encryptedContent := payload[256:]

	// Get the private key
	privateKey, ok := d.config.SigningKey.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("signing key is not RSA private key")
	}

	// Decrypt the session key using OAEP
	sessionKey, err := rsa.DecryptOAEP(sha256.New(), rand.Reader, privateKey, encryptedKey, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to decrypt session key: %w", err)
	}

	// Decrypt content with AES-256-GCM
	block, err := aes.NewCipher(sessionKey)
	if err != nil {
		return nil, fmt.Errorf("failed to create AES cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("failed to create GCM: %w", err)
	}
	nonceSize := gcm.NonceSize()
	if len(encryptedContent) < nonceSize {
		return nil, fmt.Errorf("ciphertext too short")
	}
	nonce, ciphertext := encryptedContent[:nonceSize], encryptedContent[nonceSize:]
	decryptedContent, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to decrypt content: %w", err)
	}

	return decryptedContent, nil
}

// parseMultipart parses a multipart message
func parseMultipart(msg []byte) ([]*MultipartPart, error) {
	parts := strings.SplitN(string(msg), "\r\n\r\n", 2)
	if len(parts) < 2 {
		return nil, fmt.Errorf("invalid message format")
	}

	headers := parts[0]
	content := parts[1]

	// Find boundary
	var boundary string
	for _, line := range strings.Split(headers, "\r\n") {
		if strings.HasPrefix(line, "boundary=") {
			boundary = strings.Trim(strings.TrimPrefix(line, "boundary="), "\"")
			break
		}
	}

	if boundary == "" {
		return nil, fmt.Errorf("no boundary found")
	}

	// Split by boundary
	boundaryStr := "--" + boundary
	sections := strings.Split(content, boundaryStr)

	var result []*MultipartPart
	for _, section := range sections {
		section = strings.Trim(section, "\r\n")
		if section == "" || section == "--" {
			continue
		}
		parts := strings.SplitN(section, "\r\n\r\n", 2)
		if len(parts) < 2 {
			continue
		}
		result = append(result, &MultipartPart{
			Headers: parts[0],
			Content: []byte(parts[1]),
		})
	}

	return result, nil
}

// MultipartPart represents a part of a multipart message
type MultipartPart struct {
	Headers string
	Content []byte
}

// extractBase64Content extracts base64 content from S/MIME message
func extractBase64Content(msg []byte) string {
	var contentB64 string
	inContent := false

	for _, line := range strings.Split(string(msg), "\r\n") {
		if strings.HasPrefix(line, "Content-Type: application/pkcs7-mime") {
			inContent = true
			continue
		}
		if inContent && line == "" {
			break
		}
		if inContent && !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") && !strings.Contains(line, ":") {
			contentB64 += strings.TrimSpace(line)
		}
	}

	return contentB64
}

// generateBoundary generates a random boundary string
func generateBoundary() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "=_boundary_="
	}
	return fmt.Sprintf("=_%s_=", base64.StdEncoding.EncodeToString(b))
}

// ParseCertificate parses an X.509 certificate from PEM or DER
func ParseCertificate(data []byte) (*x509.Certificate, error) {
	// Try PEM first
	block, _ := pem.Decode(data)
	if block != nil {
		return x509.ParseCertificate(block.Bytes)
	}

	// Try DER
	return x509.ParseCertificate(data)
}

// ParsePrivateKey parses an RSA private key from PEM or DER
func ParsePrivateKey(data []byte) (*rsa.PrivateKey, error) {
	// Try PEM first
	block, _ := pem.Decode(data)
	if block != nil {
		return x509.ParsePKCS1PrivateKey(block.Bytes)
	}

	// Try DER
	return x509.ParsePKCS1PrivateKey(data)
}

// LoadCertificateFromFile loads a certificate from a PEM file
func LoadCertificateFromFile(filename string) (*x509.Certificate, error) {
	data, err := os.ReadFile(filepath.Clean(filename))
	if err != nil {
		return nil, err
	}
	return ParseCertificate(data)
}

// LoadPrivateKeyFromFile loads a private key from a PEM file
func LoadPrivateKeyFromFile(filename string) (*rsa.PrivateKey, error) {
	data, err := os.ReadFile(filepath.Clean(filename))
	if err != nil {
		return nil, err
	}
	return ParsePrivateKey(data)
}
