package jmap

import "testing"

func TestEnrichEmailFromMIME_FullMultipart(t *testing.T) {
	raw := "From: Alice Example <alice@local.test>\r\n" +
		"To: \"Bob\" <bob@local.test>\r\n" +
		"Cc: carol@local.test\r\n" +
		"Bcc: dave@local.test\r\n" +
		"Sender: secretary@local.test\r\n" +
		"Reply-To: alice-replies@local.test\r\n" +
		"Subject: hi\r\n" +
		"Message-ID: <root@local.test>\r\n" +
		"In-Reply-To: <parent@local.test>\r\n" +
		"References: <gp@local.test> <parent@local.test>\r\n" +
		"Date: Mon, 02 Jan 2006 15:04:05 +0000\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: multipart/mixed; boundary=\"b1\"\r\n" +
		"\r\n" +
		"--b1\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n" +
		"\r\n" +
		"body text\r\n" +
		"--b1\r\n" +
		"Content-Type: application/octet-stream\r\n" +
		"Content-Disposition: attachment; filename=\"doc.bin\"\r\n" +
		"\r\n" +
		"PAYLOAD\r\n" +
		"--b1--\r\n"

	var e Email
	enrichEmailFromMIME(&e, []byte(raw))

	if len(e.From) != 1 || e.From[0].Email != "alice@local.test" || e.From[0].Name != "Alice Example" {
		t.Errorf("From = %+v, want alice@local.test with name", e.From)
	}
	if len(e.To) != 1 || e.To[0].Email != "bob@local.test" {
		t.Errorf("To = %+v", e.To)
	}
	if len(e.CC) != 1 || e.CC[0].Email != "carol@local.test" {
		t.Errorf("CC = %+v, want carol", e.CC)
	}
	if len(e.BCC) != 1 || e.BCC[0].Email != "dave@local.test" {
		t.Errorf("BCC = %+v, want dave", e.BCC)
	}
	if len(e.Sender) != 1 || e.Sender[0].Email != "secretary@local.test" {
		t.Errorf("Sender = %+v", e.Sender)
	}
	if len(e.ReplyTo) != 1 || e.ReplyTo[0].Email != "alice-replies@local.test" {
		t.Errorf("ReplyTo = %+v", e.ReplyTo)
	}
	if len(e.MessageID) != 1 || e.MessageID[0] != "root@local.test" {
		t.Errorf("MessageID = %+v", e.MessageID)
	}
	if len(e.InReplyTo) != 1 || e.InReplyTo[0] != "parent@local.test" {
		t.Errorf("InReplyTo = %+v", e.InReplyTo)
	}
	if len(e.References) != 2 || e.References[1] != "parent@local.test" {
		t.Errorf("References = %+v", e.References)
	}
	if e.SentAt == "" {
		t.Error("SentAt should be populated from Date header")
	}
	if !e.HasAttachment {
		t.Error("HasAttachment should be true for a message with an attachment part")
	}
}

func TestEnrichEmailFromMIME_PlainNoAttachment(t *testing.T) {
	raw := "From: alice@local.test\r\n" +
		"To: bob@local.test\r\n" +
		"Subject: plain\r\n" +
		"Content-Type: text/plain\r\n" +
		"\r\n" +
		"just text\r\n"

	var e Email
	enrichEmailFromMIME(&e, []byte(raw))

	if e.HasAttachment {
		t.Error("HasAttachment should be false for a plain-text message")
	}
	if len(e.CC) != 0 {
		t.Errorf("CC should be empty, got %+v", e.CC)
	}
	if len(e.From) != 1 || e.From[0].Email != "alice@local.test" {
		t.Errorf("From = %+v", e.From)
	}
}

func TestEnrichEmailFromMIME_MalformedIsResilient(t *testing.T) {
	var e Email
	e.Subject = "kept"
	enrichEmailFromMIME(&e, []byte("this is not a valid mime message"))
	if e.Subject != "kept" {
		t.Error("malformed input must leave the email unchanged")
	}
}
