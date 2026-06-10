// Package mailimport parses messages out of the common interchange formats
// (mbox, individual .eml files, and Maildir trees) into raw RFC 5322 byte
// blobs ready to be filed into the canonical store. It is pure parsing with no
// storage dependencies, so it is unit-testable in isolation; the caller (the
// `umailserver import` CLI) supplies the canonical filing.
//
// Maildir folder names are preserved (a Maildir++ ".Sent" subfolder yields
// folder "Sent"); mbox and single-/multi-.eml sources carry no folder of their
// own and leave Folder empty for the caller's target to apply.
package mailimport

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Message is one parsed message with the folder it should be filed into. An
// empty Folder means "use the caller's target folder" (mbox/.eml have no folder
// of their own).
type Message struct {
	Raw    []byte
	Folder string
}

// ReadMbox parses an mbox stream into messages. Messages are separated by a
// "From " line at the start of a line that begins the file or follows a blank
// line (the mboxo/mboxrd convention); the separator line itself is dropped.
// mboxrd ">From "/">>From " body escaping is reversed by stripping one leading
// ">".
func ReadMbox(r io.Reader) ([]Message, error) {
	br := bufio.NewReader(r)
	var (
		out       []Message
		cur       bytes.Buffer
		started   bool // a message is currently being accumulated
		prevBlank = true
	)
	flush := func() {
		if started {
			// Copy: cur.Bytes() aliases the buffer, which Reset reuses for the
			// next message.
			out = append(out, Message{Raw: bytes.Clone(trimTrailingNewline(cur.Bytes()))})
			cur.Reset()
			started = false
		}
	}
	for {
		line, err := br.ReadString('\n')
		if len(line) > 0 {
			body := strings.TrimRight(line, "\r\n")
			if prevBlank && strings.HasPrefix(body, "From ") {
				// Separator line: begin a new message, drop the "From " line.
				flush()
				started = true
			} else {
				started = true
				cur.WriteString(unescapeFromLine(line))
			}
			prevBlank = body == ""
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("mailimport: read mbox: %w", err)
		}
	}
	flush()
	// Drop a leading empty pseudo-message if the stream began with "From " on a
	// blank-prefixed start with no content.
	cleaned := out[:0]
	for _, m := range out {
		if len(bytes.TrimSpace(m.Raw)) > 0 {
			cleaned = append(cleaned, m)
		}
	}
	return cleaned, nil
}

// unescapeFromLine reverses mboxrd escaping: a line of one-or-more ">" followed
// by "From " loses exactly one leading ">". Other lines are returned unchanged.
func unescapeFromLine(line string) string {
	if len(line) > 0 && line[0] == '>' {
		if strings.HasPrefix(strings.TrimLeft(line, ">"), "From ") {
			return line[1:]
		}
	}
	return line
}

// trimTrailingNewline drops a single trailing CRLF/LF left by the line scan so
// messages do not accumulate a blank tail line.
func trimTrailingNewline(b []byte) []byte {
	b = bytes.TrimSuffix(b, []byte("\n"))
	b = bytes.TrimSuffix(b, []byte("\r"))
	return b
}

// ReadEMLFile reads a single .eml file as one message (no folder).
func ReadEMLFile(path string) (Message, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Message{}, fmt.Errorf("mailimport: read eml %q: %w", path, err)
	}
	return Message{Raw: data}, nil
}

// ReadEMLDir reads every *.eml file directly under dir as a message (no folder),
// in lexical order.
func ReadEMLDir(dir string) ([]Message, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("mailimport: read eml dir %q: %w", dir, err)
	}
	var out []Message
	for _, e := range entries {
		if e.IsDir() || !strings.EqualFold(filepath.Ext(e.Name()), ".eml") {
			continue
		}
		m, rerr := ReadEMLFile(filepath.Join(dir, e.Name()))
		if rerr != nil {
			return nil, rerr
		}
		out = append(out, m)
	}
	return out, nil
}

// ReadMaildir walks a Maildir tree, returning every message in the root's
// cur/new (folder empty, i.e. the caller's target) plus every Maildir++
// subfolder (a "." entry with its own cur/new), preserving folder names
// (".Sent" -> "Sent", ".Archive.2024" -> "Archive/2024").
func ReadMaildir(dir string) ([]Message, error) {
	if !isMaildir(dir) {
		return nil, fmt.Errorf("mailimport: %q is not a Maildir (no cur/ or new/)", dir)
	}
	var out []Message
	root, err := readMaildirMessages(dir, "")
	if err != nil {
		return nil, err
	}
	out = append(out, root...)

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("mailimport: read maildir %q: %w", dir, err)
	}
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), ".") || e.Name() == "." || e.Name() == ".." {
			continue
		}
		sub := filepath.Join(dir, e.Name())
		if !isMaildir(sub) {
			continue
		}
		folder := maildirFolderName(e.Name())
		msgs, merr := readMaildirMessages(sub, folder)
		if merr != nil {
			return nil, merr
		}
		out = append(out, msgs...)
	}
	return out, nil
}

// isMaildir reports whether dir has a cur/ or new/ subdirectory.
func isMaildir(dir string) bool {
	for _, sub := range []string{"cur", "new"} {
		if fi, err := os.Stat(filepath.Join(dir, sub)); err == nil && fi.IsDir() {
			return true
		}
	}
	return false
}

// readMaildirMessages reads message files from dir/cur and dir/new, tagging each
// with folder.
func readMaildirMessages(dir, folder string) ([]Message, error) {
	var out []Message
	for _, sub := range []string{"new", "cur"} {
		d := filepath.Join(dir, sub)
		entries, err := os.ReadDir(d)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("mailimport: read %q: %w", d, err)
		}
		for _, e := range entries {
			if e.IsDir() || strings.HasPrefix(e.Name(), ".") {
				continue
			}
			data, rerr := os.ReadFile(filepath.Join(d, e.Name()))
			if rerr != nil {
				return nil, fmt.Errorf("mailimport: read %q: %w", filepath.Join(d, e.Name()), rerr)
			}
			out = append(out, Message{Raw: data, Folder: folder})
		}
	}
	return out, nil
}

// maildirFolderName converts a Maildir++ subfolder directory name to a mailbox
// path: drop the leading ".", and map the "." hierarchy separator to "/".
func maildirFolderName(entry string) string {
	return strings.ReplaceAll(strings.TrimPrefix(entry, "."), ".", "/")
}

// NormalizeCRLF rewrites all line endings to CRLF so a message parsed from an
// LF-only source is stored in the RFC 5322 line-ending form the mail protocols
// serve. Existing CRLF pairs are preserved (not doubled).
func NormalizeCRLF(b []byte) []byte {
	b = bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n"))
	b = bytes.ReplaceAll(b, []byte("\r"), []byte("\n"))
	return bytes.ReplaceAll(b, []byte("\n"), []byte("\r\n"))
}
