package api

import (
	"encoding/base64"
	"strings"
	"testing"
)

// TestBuildMultipartBody verifies the multipart/mixed assembly includes the text
// body and a base64-encoded attachment with its filename and disposition.
func TestBuildMultipartBody(t *testing.T) {
	raw := []byte("hello file")
	encoded := base64.StdEncoding.EncodeToString(raw)

	body, ctype, err := buildMultipartBody("the message", []Attachment{
		{Filename: "a.txt", ContentType: "text/plain", Content: encoded},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasPrefix(ctype, "multipart/mixed; boundary=") {
		t.Errorf("expected multipart/mixed content type, got %q", ctype)
	}
	if !strings.Contains(body, "the message") {
		t.Errorf("body should contain the text part")
	}
	if !strings.Contains(body, `filename="a.txt"`) {
		t.Errorf("body should contain the attachment filename")
	}
	if !strings.Contains(body, encoded) {
		t.Errorf("body should contain the base64-encoded attachment")
	}
}

// TestBuildMultipartBody_InvalidEncoding rejects attachments that are not valid
// base64 instead of producing a corrupt message.
func TestBuildMultipartBody_InvalidEncoding(t *testing.T) {
	if _, _, err := buildMultipartBody("x", []Attachment{{Filename: "bad", Content: "!!!not-base64!!!"}}); err == nil {
		t.Errorf("expected an error for invalid base64 attachment")
	}
}
