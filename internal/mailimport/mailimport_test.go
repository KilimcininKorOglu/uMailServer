package mailimport

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func TestReadMboxSplitsAndUnescapes(t *testing.T) {
	mbox := "From alice@example.com Mon Jan  1 00:00:00 2024\r\n" +
		"Subject: First\r\n" +
		"\r\n" +
		"Body line one\r\n" +
		">From the escaped line\r\n" +
		"\r\n" +
		"From bob@example.com Mon Jan  1 00:01:00 2024\r\n" +
		"Subject: Second\r\n" +
		"\r\n" +
		"Second body\r\n"

	msgs, err := ReadMbox(strings.NewReader(mbox))
	if err != nil {
		t.Fatalf("ReadMbox: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("messages = %d, want 2", len(msgs))
	}
	first := string(msgs[0].Raw)
	if !strings.Contains(first, "Subject: First") {
		t.Errorf("first message missing its subject: %q", first)
	}
	// mboxrd ">From " is unescaped to "From " and is NOT treated as a separator
	// (it is mid-body, not after a blank line).
	if !strings.Contains(first, "From the escaped line") || strings.Contains(first, ">From") {
		t.Errorf("first message did not unescape >From: %q", first)
	}
	second := string(msgs[1].Raw)
	if !strings.Contains(second, "Subject: Second") || !strings.Contains(second, "Second body") {
		t.Errorf("second message malformed: %q", second)
	}
	for _, m := range msgs {
		if m.Folder != "" {
			t.Errorf("mbox message should have empty folder, got %q", m.Folder)
		}
	}
}

func TestReadMboxSingleMessage(t *testing.T) {
	mbox := "From x@y Mon Jan  1 00:00:00 2024\nSubject: only\n\nhi\n"
	msgs, err := ReadMbox(strings.NewReader(mbox))
	if err != nil {
		t.Fatalf("ReadMbox: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("messages = %d, want 1", len(msgs))
	}
	if !strings.Contains(string(msgs[0].Raw), "Subject: only") {
		t.Errorf("unexpected body: %q", msgs[0].Raw)
	}
}

func TestReadEMLFileAndDir(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("a.eml", "Subject: A\r\n\r\nbody a")
	write("b.eml", "Subject: B\r\n\r\nbody b")
	write("notes.txt", "ignored")

	one, err := ReadEMLFile(filepath.Join(dir, "a.eml"))
	if err != nil {
		t.Fatalf("ReadEMLFile: %v", err)
	}
	if !strings.Contains(string(one.Raw), "Subject: A") {
		t.Errorf("eml file: %q", one.Raw)
	}

	msgs, err := ReadEMLDir(dir)
	if err != nil {
		t.Fatalf("ReadEMLDir: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("eml dir messages = %d, want 2 (.txt ignored)", len(msgs))
	}
}

func TestReadMaildirPreservesFolders(t *testing.T) {
	root := filepath.Join(t.TempDir(), "Maildir")
	mk := func(p string) {
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write := func(p, content string) {
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	mk(filepath.Join(root, "cur"))
	mk(filepath.Join(root, "new"))
	mk(filepath.Join(root, ".Sent", "cur"))
	write(filepath.Join(root, "new", "1700000000.M1.host"), "Subject: inbox msg\r\n\r\nhi")
	write(filepath.Join(root, ".Sent", "cur", "1700000001.M2.host:2,S"), "Subject: sent msg\r\n\r\nbye")

	msgs, err := ReadMaildir(root)
	if err != nil {
		t.Fatalf("ReadMaildir: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("messages = %d, want 2", len(msgs))
	}
	byFolder := map[string]string{}
	for _, m := range msgs {
		byFolder[m.Folder] = string(m.Raw)
	}
	if !strings.Contains(byFolder[""], "inbox msg") {
		t.Errorf("root message missing or mis-foldered: %v", byFolder)
	}
	if !strings.Contains(byFolder["Sent"], "sent msg") {
		t.Errorf("Sent message missing or mis-foldered: %v", byFolder)
	}
}

func TestMaildirFolderNameHierarchy(t *testing.T) {
	cases := map[string]string{
		".Sent":          "Sent",
		".Archive.2024":  "Archive/2024",
		".Work.Projects": "Work/Projects",
	}
	keys := make([]string, 0, len(cases))
	for k := range cases {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, in := range keys {
		if got := maildirFolderName(in); got != cases[in] {
			t.Errorf("maildirFolderName(%q) = %q, want %q", in, got, cases[in])
		}
	}
}

func TestNormalizeCRLF(t *testing.T) {
	in := []byte("a\nb\r\nc\rd")
	want := "a\r\nb\r\nc\r\nd"
	if got := string(NormalizeCRLF(in)); got != want {
		t.Errorf("NormalizeCRLF = %q, want %q", got, want)
	}
	// Idempotent: already-CRLF stays CRLF (no doubling).
	if got := string(NormalizeCRLF([]byte(want))); got != want {
		t.Errorf("NormalizeCRLF not idempotent: %q", got)
	}
}
