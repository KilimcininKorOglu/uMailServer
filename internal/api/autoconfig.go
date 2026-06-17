package api

import (
	"encoding/xml"
	"net/http"
	"strings"

	"github.com/umailserver/umailserver/internal/db"
)

// AutoconfigProvider represents an email provider in autoconfig
type AutoconfigProvider struct {
	XMLName         xml.Name           `xml:"emailProvider"`
	ID              string             `xml:"id,attr"`
	Domain          []string           `xml:"domain"`
	IncomingServers []AutoconfigServer `xml:"incomingServer"`
	OutgoingServers []AutoconfigServer `xml:"outgoingServer"`
}

// AutoconfigServer represents a server configuration
type AutoconfigServer struct {
	Type           string `xml:"type,attr"`
	Hostname       string `xml:"hostname"`
	Port           int    `xml:"port"`
	SocketType     string `xml:"socketType"`
	Username       string `xml:"username"`
	Authentication string `xml:"authentication"`
}

// AutoconfigClientConfig is the root element
type AutoconfigClientConfig struct {
	XMLName   xml.Name             `xml:"clientConfig"`
	Version   string              `xml:"version,attr"`
	Providers []AutoconfigProvider `xml:"emailProvider"`
}

// domainConfig holds the effective autoconfig for a single domain.
// Values come from DomainData.Settings with server-wide config defaults as fallback.
type domainConfig struct {
	IncomingHostname string
	IncomingPort     int
	IncomingSSL      bool
	OutgoingHostname string
	OutgoingPort    int
	OutgoingSSL     bool
}

// defaultsFromConfig returns the server-wide defaults from the API server config.
func defaultsFromConfig(cfg Config) domainConfig {
	return domainConfig{
		IncomingHostname: cfg.AutoconfigHostname,
		IncomingPort:     cfg.AutoconfigIncomingPort,
		IncomingSSL:      true,
		OutgoingHostname: cfg.AutoconfigHostname,
		OutgoingPort:     cfg.AutoconfigOutgoingPort,
		OutgoingSSL:      true,
	}
}

// effectiveConfig returns the domain-specific config, falling back to server-wide
// defaults when the domain Settings are not set.
func effectiveConfig(dom *db.DomainData, cfg Config) domainConfig {
	dc := defaultsFromConfig(cfg)

	if dom != nil && dom.Settings != nil {
		if v := dom.Settings["autoconfig.incoming_hostname"]; v != "" {
			dc.IncomingHostname = v
		}
		if v := dom.Settings["autoconfig.outgoing_hostname"]; v != "" {
			dc.OutgoingHostname = v
		}
	}

	return dc
}

// handleAutoconfig handles Mozilla-style autoconfig requests
// Path: /.well-known/autoconfig/mail/config-v1.1.xml
func (s *Server) handleAutoconfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.sendAutoconfigError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	domain := extractDomainFromRequest(r)
	if domain == "" {
		s.sendAutoconfigError(w, http.StatusBadRequest, "Domain required")
		return
	}

	var dom *db.DomainData
	if s.db != nil {
		dom, _ = s.db.GetDomain(domain) //nolint:errcheck
		// If the domain is not found, dom stays nil and defaults are served.
	}

	dc := effectiveConfig(dom, s.config)
	config := buildAutoconfigXML(dc)

	w.Header().Set("Content-Type", "application/xml")
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	if err := xml.NewEncoder(w).Encode(config); err != nil {
		// Client disconnected — nothing to do.
	}
}

// buildAutoconfigXML builds the autoconfig response from the effective domain config.
func buildAutoconfigXML(dc domainConfig) *AutoconfigClientConfig {
	return &AutoconfigClientConfig{
		Version: "1.1",
		Providers: []AutoconfigProvider{
			{
				ID:     dc.IncomingHostname,
				Domain: []string{dc.IncomingHostname},
				IncomingServers: []AutoconfigServer{
					{
						Type:           "imap",
						Hostname:       dc.IncomingHostname,
						Port:           dc.IncomingPort,
						SocketType:     socketType(dc.IncomingSSL),
						Username:       "%EMAILADDRESS%",
						Authentication: authMethod(dc.IncomingSSL),
					},
				},
				OutgoingServers: []AutoconfigServer{
					{
						Type:           "smtp",
						Hostname:       dc.OutgoingHostname,
						Port:           dc.OutgoingPort,
						SocketType:     socketType(dc.OutgoingSSL),
						Username:       "%EMAILADDRESS%",
						Authentication: authMethod(dc.OutgoingSSL),
					},
				},
			},
		},
	}
}

// socketType returns "SSL" for SSL/TLS, "STARTTLS" for opportunistic upgrade.
func socketType(ssl bool) string {
	if ssl {
		return "SSL"
	}
	return "STARTTLS"
}

// authMethod returns the appropriate authentication method identifier.
func authMethod(ssl bool) string {
	if ssl {
		return "password-encrypted"
	}
	return "password-cleartext"
}

// extractDomainFromRequest extracts the domain from the autoconfig request.
func extractDomainFromRequest(r *http.Request) string {
	host := r.Host
	if idx := strings.Index(host, ":"); idx > 0 {
		host = host[:idx]
	}
	if strings.Contains(host, ".") {
		return strings.ToLower(host)
	}
	return ""
}

// sendAutoconfigError sends an XML error response for autoconfig.
func (s *Server) sendAutoconfigError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/xml")
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	w.WriteHeader(status)

	resp := struct {
		XMLName xml.Name `xml:"clientConfig"`
		Version string   `xml:"version,attr"`
		Error   struct {
			XMLName  xml.Name `xml:"error"`
			Code     int      `xml:"code,attr"`
			Message  string   `xml:"message"`
			Language string   `xml:"language,attr"`
		} `xml:"error"`
	}{
		Version: "1.1",
	}
	resp.Error.Code = status
	resp.Error.Message = message
	resp.Error.Language = "en"

	_ = xml.NewEncoder(w).Encode(resp)
}
