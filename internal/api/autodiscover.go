package api

import (
	"encoding/xml"
	"net/http"
	"strings"

	"github.com/umailserver/umailserver/internal/db"
	"github.com/umailserver/umailserver/internal/semcore"
)

// AccountData is a type alias for the db account data, used in Autodiscover
// policy checks (VAL-OUTLOOK-002).
type AccountData = db.AccountData

// AutodiscoverRequest represents an Autodiscover request
type AutodiscoverRequest struct {
	XMLName  xml.Name `xml:"Autodiscover"`
	Requests []struct {
		EMailAddress  string `xml:"EMailAddress"`
		AcceptableDst string `xml:"AcceptableDst"`
	} `xml:"Request"`
}

// AutodiscoverResponse represents an Autodiscover response
type AutodiscoverResponse struct {
	XMLName  xml.Name `xml:"AutodiscoverResponse"`
	Space    string   `xml:"xmlns,attr"`
	Response struct {
		XMLName xml.Name `xml:"Response"`
		User    struct {
			XMLName      xml.Name `xml:"User"`
			DisplayName  string   `xml:"DisplayName"`
			EMailAddress string   `xml:"EMailAddress"`
		} `xml:"User"`
		Account struct {
			XMLName     xml.Name               `xml:"Account"`
			AccountType string                 `xml:"AccountType"`
			Action      string                 `xml:"Action"`
			Protocol    []AutodiscoverProtocol `xml:"Protocol"`
		} `xml:"Account"`
	} `xml:"Response"`
}

// AutodiscoverProtocol represents a protocol entry in the Autodiscover response.
// It uses a flexible field map to accommodate different protocol types (IMAP, SMTP,
// EWS, MAPI/HTTP, NSPI, OAB) that each require different subsets of fields.
type AutodiscoverProtocol struct {
	XMLName     xml.Name `xml:"Protocol"`
	Type        string   `xml:"Type"`
	Server      string   `xml:"Server,omitempty"`
	Port        int      `xml:"Port,omitempty"`
	LoginName   string   `xml:"LoginName,omitempty"`
	Domain      string   `xml:"Domain,omitempty"`
	SPA         string   `xml:"SPA,omitempty"`
	SSL         string   `xml:"SSL,omitempty"`
	Auth        string   `xml:"Auth,omitempty"`
	AuthPackage string   `xml:"AuthPackage,omitempty"`
	MapiHttp    string   `xml:"MapiHttp,omitempty"`
	MailboxDN   string   `xml:"MailboxDN,omitempty"`
	RedirectURL string   `xml:"RedirectUrl,omitempty"`
}

// handleAutodiscover handles Microsoft Autodiscover requests
// Path: /autodiscover/autodiscover.xml
func (s *Server) handleAutodiscover(w http.ResponseWriter, r *http.Request) {
	// Only allow GET and POST
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		s.sendAutodiscoverError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	var email string

	if r.Method == http.MethodGet {
		// GET request - extract email from query string or host
		email = r.URL.Query().Get("email")
		if email == "" {
			// Try to extract from Host header
			email = extractEmailFromHost(r.Host)
		}
	} else {
		// POST request - parse XML body
		email = s.parseAutodiscoverPOST(r)
	}

	if email == "" {
		// Return redirect to the main autodiscover endpoint
		s.sendAutodiscoverError(w, http.StatusBadRequest, "Email address required")
		return
	}

	// Extract domain from email
	domain := extractDomainFromEmail(email)
	if domain == "" {
		s.sendAutodiscoverError(w, http.StatusBadRequest, "Invalid email address")
		return
	}

	// Look up per-account compatibility tier for pilot cohort isolation.
	// If the account has an explicit TierExchange (1) stored, that takes
	// precedence over the global FeatureCanonicalIdentity gate.
	accountTier := uint8(0) // 0 means use global tier
	var accData *db.AccountData
	if s.db != nil {
		localPart := email
		if idx := strings.Index(email, "@"); idx != -1 {
			localPart = email[:idx]
		}
		if acc, err := s.db.GetAccount(domain, localPart); err == nil && acc != nil {
			accountTier = acc.CompatibilityTier
			accData = acc
		}
	}

	// VAL-OUTLOOK-002: Block policy-disabled accounts before partial provisioning.
	// If the account is inactive or flagged for required password change, fail
	// explicitly so Outlook cannot create a half-working Exchange profile.
	if accData != nil {
		if !accData.IsActive {
			s.sendAutodiscoverError(w, http.StatusForbidden, "Account is disabled")
			return
		}
		if accData.MustChangePassword {
			s.sendAutodiscoverError(w, http.StatusForbidden, "Account requires password change before access")
			return
		}
	}

	// Build response
	resp := s.buildAutodiscoverResponse(email, domain, accountTier)

	// Set headers
	w.Header().Set("Content-Type", "application/xml")
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")

	// Write response
	_ = xml.NewEncoder(w).Encode(resp)
}

// parseAutodiscoverPOST parses the POST body for email address
func (s *Server) parseAutodiscoverPOST(r *http.Request) string {
	// For POST requests, we would normally parse the XML body
	// For now, extract email from request
	email := r.URL.Query().Get("email")
	if email == "" {
		email = extractEmailFromHost(r.Host)
	}
	return email
}

// buildAutodiscoverResponse builds the Autodiscover response.
// The response advertises only the endpoints supported by the account's
// active compatibility tier. When FeatureEWS is enabled, accounts in the
// Exchange tier receive the EWS/Exchange.asmx protocol entry, while
// TierIMAPOnly accounts receive only IMAP and SMTP settings.
// accountTier is the stored per-account CompatibilityTier (0 = use global gate).
//
// In TierOutlook (modern Windows Outlook), the response additionally includes
// MAPI/HTTP (Outlook connector), NSPI (directory address book), and OAB
// (offline address book) protocol entries so that Outlook can provision in
// Exchange mode without falling back to IMAP/SMTP.
func (s *Server) buildAutodiscoverResponse(email, domain string, accountTier uint8) *AutodiscoverResponse {
	resp := &AutodiscoverResponse{
		Space: "http://schemas.microsoft.com/exchange/autodiscover/responseschema/2006",
	}

	resp.Response.User.DisplayName = email
	resp.Response.User.EMailAddress = email
	resp.Response.Account.AccountType = "email"
	resp.Response.Account.Action = "settings"

	// Determine the effective compatibility tier.
	// If accountTier is non-zero, it overrides the global gate (pilot cohort isolation).
	// Zero falls back to the global FeatureCanonicalIdentity decision.
	tier := semcore.AccountCompatibilityTier(accountTier)
	ewsgate := semcore.Gate().IsEnabled(semcore.FeatureEWS)
	mapihTTPGate := semcore.Gate().IsEnabled(semcore.FeatureMAPIHTTP)

	// Add IMAP protocol (available in all tiers)
	imapProtocol := newProtocol("IMAP", "mail."+domain, 993, email, domain)
	resp.Response.Account.Protocol = append(resp.Response.Account.Protocol, imapProtocol)

	// Add SMTP protocol (available in all tiers)
	smtpProtocol := newProtocol("SMTP", "mail."+domain, 465, email, domain)
	resp.Response.Account.Protocol = append(resp.Response.Account.Protocol, smtpProtocol)

	// Add EWS/Exchange protocol only when in Exchange or Outlook tier with EWS gate enabled
	if (tier == semcore.TierExchange || tier == semcore.TierOutlook) && ewsgate {
		ewsProtocol := newProtocol("EWS", s.serverHost(), 443, email, domain)
		resp.Response.Account.Protocol = append(resp.Response.Account.Protocol, ewsProtocol)
	}

	// Add Outlook-specific protocol entries in TierOutlook with MAPI/HTTP gate enabled.
	// Modern Windows Outlook uses these to provision in Exchange mode without IMAP fallback.
	if tier == semcore.TierOutlook && mapihTTPGate {
		// MAPI/HTTP protocol entry — tells Outlook to use the Outlook connector / MAPI-over-HTTP path.
		// Server points to the same host as EWS since MAPI/HTTP sessions are routed there.
		mapiProtocol := AutodiscoverProtocol{
			Type:        "MAPI",
			Server:      s.serverHost(),
			SSL:         "on",
			AuthPackage: "ntlm",
		}
		resp.Response.Account.Protocol = append(resp.Response.Account.Protocol, mapiProtocol)

		// NSPI (Name Service Provider Interface) protocol entry — provides the Outlook
		// directory / GAL address-book lookup endpoint. The RedirectUrl is the NSPI
		// endpoint that Outlook resolves for address-book searches.
		nspiProtocol := AutodiscoverProtocol{
			Type:        "NSPI",
			Server:      s.serverHost(),
			SSL:         "on",
			RedirectURL: "http://" + s.serverHost() + "/mapi/nspi",
		}
		resp.Response.Account.Protocol = append(resp.Response.Account.Protocol, nspiProtocol)

		// OAB (Offline Address Book) protocol entry — provides the OAB download URL
		// so Outlook can download a local copy for offline address-book resolution.
		oabProtocol := AutodiscoverProtocol{
			Type:        "OAB",
			Server:      s.serverHost(),
			SSL:         "on",
			AuthPackage: "ntlm",
		}
		resp.Response.Account.Protocol = append(resp.Response.Account.Protocol, oabProtocol)
	}

	return resp
}

// newProtocol creates a Protocol struct for the given type, server, port, and credentials.
func newProtocol(protoType, server string, port int, loginName, domain string) AutodiscoverProtocol {
	return AutodiscoverProtocol{
		Type:      protoType,
		Server:    server,
		Port:      port,
		LoginName: loginName,
		Domain:    domain,
		SPA:       "off",
		SSL:       "on",
		Auth:      "password-encrypted",
	}
}

// serverHost returns the configured server hostname for EWS endpoint URLs.
// This is used to construct the EWS/Exchange.asmx URL in Autodiscover responses.
// The returned value should be the externally reachable server hostname.
func (s *Server) serverHost() string {
	// Use a sensible default. In production with TLS and a configured hostname,
	// this would be set via server configuration. For now, return a default that
	// works for the local development environment.
	return "localhost"
}

// extractEmailFromHost extracts email from Host header
func extractEmailFromHost(host string) string {
	// Remove port if present
	if idx := strings.Index(host, ":"); idx > 0 {
		host = host[:idx]
	}

	// Check if host looks like an email (user@domain)
	if strings.Contains(host, "@") {
		return strings.ToLower(host)
	}

	return ""
}

// extractDomainFromEmail extracts domain from email address
func extractDomainFromEmail(email string) string {
	if idx := strings.Index(email, "@"); idx > 0 {
		return strings.ToLower(email[idx+1:])
	}
	return ""
}

// sendAutodiscoverError sends an XML error response
func (s *Server) sendAutodiscoverError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/xml")
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	w.WriteHeader(status)

	resp := struct {
		XMLName xml.Name `xml:"AutodiscoverResponse"`
		Space   string   `xml:"xmlns,attr"`
		Error   struct {
			XMLName    xml.Name `xml:"Error"`
			Code       int      `xml:"Code"`
			Message    string   `xml:"Message"`
			XmlMessage string   `xml:"xml:lang"`
		} `xml:"Error"`
	}{
		Space: "http://schemas.microsoft.com/exchange/autodiscover/responseschema/2006",
	}
	resp.Error.Code = status
	resp.Error.Message = message
	resp.Error.XmlMessage = "en-US"

	_ = xml.NewEncoder(w).Encode(resp)
}
