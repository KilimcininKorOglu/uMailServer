package api

import (
	"net/mail"
	"strings"
)

// buildOutboundFromName builds the From display name for an outbound message
// from the sender domain's From templates. It picks the external template when
// any recipient leaves the organization (else the internal one), expands it with
// the sender's account fields plus the domain's company name, and returns the
// result. It returns "" when there is no usable name (no account display name,
// or the relevant template is empty) so the caller can fall back to the plain
// DisplayName / bare address.
func (s *Server) buildOutboundFromName(senderEmail string, recipients []string) string {
	if s.db == nil {
		return ""
	}
	localPart, domainName, ok := strings.Cut(senderEmail, "@")
	if !ok || localPart == "" || domainName == "" {
		return ""
	}
	account, err := s.db.GetAccount(domainName, localPart)
	if err != nil || account == nil || account.DisplayName == "" {
		return ""
	}
	dom, err := s.db.GetDomain(domainName)
	if err != nil || dom == nil {
		return ""
	}

	tmpl := dom.FromTemplateInternal
	if s.anyRecipientExternal(recipients) {
		tmpl = dom.FromTemplateExternal
	}
	if strings.TrimSpace(tmpl) == "" {
		return ""
	}

	return expandFromTemplate(tmpl, map[string]string{
		"name":       account.DisplayName,
		"title":      account.Title,
		"department": account.Department,
		"company":    dom.CompanyName,
		"email":      senderEmail,
	})
}

// anyRecipientExternal reports whether any recipient address is not hosted on a
// local, active domain (i.e. the message leaves the organization). Unknown or
// inactive domains count as external. A single From header is sent for the whole
// message, so any external recipient makes the whole message external.
func (s *Server) anyRecipientExternal(recipients []string) bool {
	for _, r := range recipients {
		addr := strings.TrimSpace(r)
		if addr == "" {
			continue
		}
		if parsed, perr := mail.ParseAddress(addr); perr == nil {
			addr = parsed.Address
		}
		_, domainName, ok := strings.Cut(addr, "@")
		if !ok || domainName == "" {
			continue
		}
		d, derr := s.db.GetDomain(domainName)
		if derr != nil || d == nil || !d.IsActive {
			return true
		}
	}
	return false
}
