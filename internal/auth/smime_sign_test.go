package auth

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"math/big"
	"strings"
	"testing"
	"time"

	pkcs7 "github.com/smallstep/pkcs7"
)

// selfSignedSMIME returns a self-signed cert whose public key matches key, so
// CMS verification against the embedded cert succeeds.
func selfSignedSMIME(t *testing.T) (*x509.Certificate, *rsa.PrivateKey) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	tmpl := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "qa.alice@local.test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageEmailProtection},
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("CreateCertificate: %v", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("ParseCertificate: %v", err)
	}
	return cert, key
}

// The signed output must (1) be RFC 5751 multipart/signed, (2) preserve the
// original message-level headers (Subject/Message-ID) on the outer message, and
// (3) carry a detached CMS SignedData that cryptographically verifies over the
// exact transmitted MIME entity — i.e. real clients can verify it.
func TestSMIMESigner_DetachedCMS_VerifiesAndPreservesHeaders(t *testing.T) {
	cert, key := selfSignedSMIME(t)
	signer := NewSMIMESigner(&SMIMEConfig{SigningCert: cert, SigningKey: key})

	orig := "From: Alice <qa.alice@local.test>\r\n" +
		"To: bob@local.test\r\n" +
		"Subject: hello\r\n" +
		"Message-ID: <m1@local.test>\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n" +
		"\r\n" +
		"body text\r\n"

	signed, err := signer.SignMessage([]byte(orig), "qa.alice@local.test", "bob@local.test")
	if err != nil {
		t.Fatalf("SignMessage: %v", err)
	}
	out := string(signed)

	if !strings.Contains(out, "multipart/signed") || !strings.Contains(out, "application/pkcs7-signature") {
		t.Fatalf("output is not S/MIME multipart/signed:\n%s", out)
	}
	if !strings.Contains(out, "Subject: hello") {
		t.Error("Subject header must be preserved on the outer message")
	}
	if !strings.Contains(out, "Message-ID: <m1@local.test>") {
		t.Error("Message-ID header must be preserved on the outer message")
	}
	// Content-Type of the original moves to the signed entity, not the outer.
	if strings.Count(out, "text/plain") != 1 {
		t.Errorf("inner content-type should appear exactly once, got %d", strings.Count(out, "text/plain"))
	}

	inner, der := extractSignedEntityAndSignature(t, out)

	p7, err := pkcs7.Parse(der)
	if err != nil {
		t.Fatalf("pkcs7.Parse: %v", err)
	}
	p7.Content = inner // detached: supply the signed content
	if err := p7.Verify(); err != nil {
		t.Fatalf("CMS signature must verify over the transmitted entity: %v", err)
	}
}

// extractSignedEntityAndSignature pulls the first MIME part (the signed entity,
// exactly as transmitted) and the DER-decoded detached signature out of a
// multipart/signed message.
func extractSignedEntityAndSignature(t *testing.T, out string) (inner []byte, der []byte) {
	t.Helper()
	bi := strings.Index(out, `boundary="`)
	if bi < 0 {
		t.Fatal("no boundary in Content-Type")
	}
	rest := out[bi+len(`boundary="`):]
	boundary := rest[:strings.Index(rest, `"`)]

	hdrEnd := strings.Index(out, "\r\n\r\n")
	body := out[hdrEnd+4:]
	segments := strings.Split(body, "--"+boundary)
	if len(segments) < 3 {
		t.Fatalf("expected 2 MIME parts, got %d segments", len(segments)-1)
	}

	// segments[1] = "\r\n" + inner + "\r\n"
	inner = []byte(strings.TrimPrefix(strings.TrimSuffix(segments[1], "\r\n"), "\r\n"))

	// segments[2] = signature part (its own headers + blank line + base64 body)
	sigPart := segments[2]
	if i := strings.Index(sigPart, "\r\n\r\n"); i >= 0 {
		b64 := strings.TrimSpace(sigPart[i+4:])
		b64 = strings.ReplaceAll(b64, "\r\n", "")
		decoded, err := base64.StdEncoding.DecodeString(b64)
		if err != nil {
			t.Fatalf("decode signature base64: %v", err)
		}
		der = decoded
	} else {
		t.Fatal("signature part has no body")
	}
	return inner, der
}
