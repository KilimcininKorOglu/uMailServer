package server

import "net/http"

// serveListener serves srv over the ACME-issued or configured certificate when
// the TLS manager has a usable one, otherwise plain HTTP. The JMAP, CalDAV, and
// CardDAV listeners build their own *http.Server (unlike the api package, which
// owns its listener and exposes SetTLSConfig), so they route through this to
// serve the same certificate as the web and admin listeners once TLS is enabled.
//
// HTTP/2 is left to normal ALPN negotiation: these surfaces are stateless per
// request and hold no connection-oriented state, unlike the NTLM-bearing api
// listener which must pin HTTP/1.1.
func (s *Server) serveListener(srv *http.Server) error {
	if s.tlsManager != nil && s.tlsManager.IsEnabled() {
		srv.TLSConfig = s.tlsManager.GetTLSConfig()
		return srv.ListenAndServeTLS("", "")
	}
	return srv.ListenAndServe()
}
