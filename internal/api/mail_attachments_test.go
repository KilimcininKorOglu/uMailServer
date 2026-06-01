package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/umailserver/umailserver/internal/storage"
)

const msgWithAttachment = "From: alice@test.com\r\n" +
	"To: admin@test.com\r\n" +
	"Subject: Report\r\n" +
	"MIME-Version: 1.0\r\n" +
	"Content-Type: multipart/mixed; boundary=\"sep\"\r\n" +
	"\r\n" +
	"--sep\r\n" +
	"Content-Type: text/plain; charset=utf-8\r\n" +
	"\r\n" +
	"See the attached report.\r\n" +
	"--sep\r\n" +
	"Content-Type: text/csv; name=\"report.csv\"\r\n" +
	"Content-Disposition: attachment; filename=\"report.csv\"\r\n" +
	"Content-Transfer-Encoding: base64\r\n" +
	"\r\n" +
	"YSxiLGMKMSwyLDMK\r\n" + // "a,b,c\n1,2,3\n"
	"--sep--\r\n"

func TestListAttachments(t *testing.T) {
	infos := listAttachments([]byte(msgWithAttachment))
	if len(infos) != 1 {
		t.Fatalf("expected 1 attachment, got %d: %+v", len(infos), infos)
	}
	a := infos[0]
	if a.Filename != "report.csv" || a.ContentType != "text/csv" || a.Index != 0 {
		t.Errorf("attachment info wrong: %+v", a)
	}
	if a.Size != len("a,b,c\n1,2,3\n") {
		t.Errorf("decoded size wrong: got %d", a.Size)
	}
}

func TestListAttachments_PlainMessageHasNone(t *testing.T) {
	plain := "From: a@test.com\r\nTo: b@test.com\r\nSubject: hi\r\nContent-Type: text/plain\r\n\r\njust text\r\n"
	if infos := listAttachments([]byte(plain)); len(infos) != 0 {
		t.Errorf("plain message should have no attachments, got %+v", infos)
	}
}

func TestHandleMailAttachment_Download(t *testing.T) {
	mailDB, err := storage.OpenDatabase(t.TempDir() + "/mail.db")
	if err != nil {
		t.Fatalf("open mail db: %v", err)
	}
	t.Cleanup(func() {
		if err := mailDB.Close(); err != nil {
			t.Errorf("close mail db: %v", err)
		}
	})
	msgStore, err := storage.NewMessageStore(t.TempDir() + "/messages")
	if err != nil {
		t.Fatalf("create message store: %v", err)
	}
	t.Cleanup(func() {
		if err := msgStore.Close(); err != nil {
			t.Errorf("close message store: %v", err)
		}
	})

	const user = "admin@test.com"
	if err := mailDB.CreateMailbox(user, "INBOX"); err != nil {
		t.Fatalf("create mailbox: %v", err)
	}
	msgID, err := msgStore.StoreMessage(user, []byte(msgWithAttachment))
	if err != nil {
		t.Fatalf("store message: %v", err)
	}
	uid, err := mailDB.GetNextUID(user, "INBOX")
	if err != nil {
		t.Fatalf("next uid: %v", err)
	}
	if err := mailDB.StoreMessageMetadata(user, "INBOX", uid, &storage.MessageMetadata{MessageID: msgID, UID: uid, Subject: "Report"}); err != nil {
		t.Fatalf("store metadata: %v", err)
	}

	h := NewMailHandler()
	h.SetStorage(msgStore, mailDB)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/mail/attachment?id="+msgID+"&index=0", nil)
	//nolint:staticcheck // context key matches the handler's lookup
	req = req.WithContext(context.WithValue(req.Context(), "user", user))
	rec := httptest.NewRecorder()
	h.handleMailAttachment(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/csv" {
		t.Errorf("content-type wrong: %q", ct)
	}
	if cd := rec.Header().Get("Content-Disposition"); !strings.Contains(cd, "report.csv") {
		t.Errorf("content-disposition wrong: %q", cd)
	}
	if rec.Body.String() != "a,b,c\n1,2,3\n" {
		t.Errorf("downloaded bytes wrong: %q", rec.Body.String())
	}
}

func TestHandleMailAttachment_IndexOutOfRange(t *testing.T) {
	mailDB, err := storage.OpenDatabase(t.TempDir() + "/mail.db")
	if err != nil {
		t.Fatalf("open mail db: %v", err)
	}
	t.Cleanup(func() {
		if err := mailDB.Close(); err != nil {
			t.Errorf("close mail db: %v", err)
		}
	})
	msgStore, err := storage.NewMessageStore(t.TempDir() + "/messages")
	if err != nil {
		t.Fatalf("create message store: %v", err)
	}
	t.Cleanup(func() {
		if err := msgStore.Close(); err != nil {
			t.Errorf("close message store: %v", err)
		}
	})
	const user = "admin@test.com"
	if err := mailDB.CreateMailbox(user, "INBOX"); err != nil {
		t.Fatalf("create mailbox: %v", err)
	}
	msgID, err := msgStore.StoreMessage(user, []byte(msgWithAttachment))
	if err != nil {
		t.Fatalf("store message: %v", err)
	}
	uid, err := mailDB.GetNextUID(user, "INBOX")
	if err != nil {
		t.Fatalf("next uid: %v", err)
	}
	if err := mailDB.StoreMessageMetadata(user, "INBOX", uid, &storage.MessageMetadata{MessageID: msgID, UID: uid}); err != nil {
		t.Fatalf("store metadata: %v", err)
	}

	h := NewMailHandler()
	h.SetStorage(msgStore, mailDB)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/mail/attachment?id="+msgID+"&index=5", nil)
	//nolint:staticcheck // context key matches the handler's lookup
	req = req.WithContext(context.WithValue(req.Context(), "user", user))
	rec := httptest.NewRecorder()
	h.handleMailAttachment(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for out-of-range index, got %d", rec.Code)
	}
}
