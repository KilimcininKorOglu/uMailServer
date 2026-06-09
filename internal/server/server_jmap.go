package server

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/umailserver/umailserver/internal/caldav"
	"github.com/umailserver/umailserver/internal/carddav"
	"github.com/umailserver/umailserver/internal/jmap"
	"github.com/umailserver/umailserver/internal/semcore"
	"github.com/umailserver/umailserver/internal/sieve"
)

// recompileSieveForEmail rebuilds and installs a mailbox's active Sieve script
// from its canonical policy (inbox rules + OOF), mirroring the EWS and webmail
// recompile paths. It lets the JMAP VacationResponse handler make an OOF change
// take effect at delivery. No-op when the Sieve manager or canonical store is
// absent.
func (s *Server) recompileSieveForEmail(email string) error {
	if s.sieveManager == nil || s.semcoreStore == nil {
		return nil
	}
	mbid, err := semcore.NewMailboxId(email)
	if err != nil {
		return err
	}
	var oof *semcore.OOFPolicy
	if oofID, oerr := semcore.NewOOFId(mbid.String()); oerr == nil {
		oof, _ = s.semcoreStore.Policy().GetOOF(oofID) //nolint:errcheck // absent OOF is fine
	}
	// Admin global rules are compiled in ahead of the user's own rules.
	script := semcore.CompileEffectivePolicy(s.semcoreStore.Policy(), mbid, oof)
	ids := []string{email}
	if lp, _, ok := strings.Cut(email, "@"); ok && lp != "" && lp != email {
		ids = append(ids, lp)
	}
	for _, id := range ids {
		if serr := s.sieveManager.StoreScript(id, sieve.ManagedScriptName, script); serr != nil {
			return serr
		}
		if serr := s.sieveManager.SetActiveScriptByName(id, sieve.ManagedScriptName); serr != nil {
			return serr
		}
		s.sieveManager.CleanupLegacyManagedScript(id)
	}
	return nil
}

// startJMAP creates and starts the JMAP server
func (s *Server) startJMAP() {
	if !s.cfg().JMAP.Enabled {
		return
	}

	addr := fmt.Sprintf("%s:%d", s.cfg().JMAP.Bind, s.cfg().JMAP.Port)

	jmapConfig := jmap.Config{
		JWTSecret:   s.cfg().Security.JWTSecret,
		TokenExpiry: 24 * time.Hour,
		CorsOrigins: s.cfg().JMAP.CorsOrigins,
		AuthorizeUser: func(email string) error {
			user, domain := parseEmail(email)
			account, err := s.database.GetAccount(domain, user)
			if err != nil {
				return err
			}
			if !account.IsActive {
				return fmt.Errorf("account is not active")
			}
			if account.MustChangePassword {
				return fmt.Errorf("password change required")
			}
			// A suspended tenant blocks its accounts on JMAP too.
			if dom, derr := s.database.GetDomain(domain); derr == nil && dom.TenantID != "" {
				if tenant, terr := s.database.GetTenant(dom.TenantID); terr == nil && !tenant.IsActive {
					return fmt.Errorf("tenant is suspended")
				}
			}
			return nil
		},
	}

	jmapServer := jmap.NewServer(s.storageDB, s.msgStore, s.logger, jmapConfig)
	jmapServer.SetTracingProvider(s.tracingProvider)
	// Route JMAP EmailSubmission/set through the same Sieve+delivery path as EWS
	// so subaddressing/Sieve/OOF/conversation-id apply uniformly across protocols.
	jmapServer.SetSubmitMessageFunc(s.submitMessageWithSieve)
	// A future EmailSubmission sendAt routes to the canonical scheduled store
	// (source "jmap", owner = account) instead of submitting now; fileSent files
	// a Sent copy on release.
	jmapServer.SetScheduleMessageFunc(func(owner, from string, to []string, data []byte, sendAt time.Time, fileSent bool) (string, error) {
		return s.scheduleSend(owner, from, to, data, sendAt, "jmap", fileSent)
	})
	// Back JMAP VacationResponse with the canonical OOF policy store (shared with
	// EWS and webmail) and the Sieve recompiler, so a vacation reply set over
	// JMAP is the same one every surface shows and fires at delivery.
	if s.semcoreStore != nil {
		jmapServer.SetVacationStores(s.semcoreStore.Policy(), s.recompileSieveForEmail)
		// Back JMAP Calendar and Contacts with the same canonical collaboration
		// store EWS, CalDAV/CardDAV, and webmail use, so an event or contact is
		// identical across every surface.
		jmapServer.SetCollabStores(
			caldav.NewCollabStore(s.semcoreStore.Collaboration(), s.semcoreStore.Identity()),
			carddav.NewCollabStore(s.semcoreStore.Collaboration(), s.semcoreStore.Identity()),
		)
		// Back the JMAP Note type with the semcore identity store + mutation
		// pipeline so a JMAP-created note reaches the same Notes folder
		// EWS/IMAP/webmail share.
		jmapServer.SetNotesStore(s.semcoreStore.Identity(), s.mutationPipe)
	}

	s.jmapServer = jmapServer

	srv := &http.Server{
		Addr:              addr,
		Handler:           jmapServer,
		ReadTimeout:       30 * time.Second,
		ReadHeaderTimeout: 10 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	s.jmapHTTPServer = srv

	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			s.logger.Error("JMAP server error", "error", err)
		}
	}()

	s.logger.Info("JMAP server started", "addr", addr)
}
