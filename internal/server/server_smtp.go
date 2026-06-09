package server

import (
	"fmt"
	"time"

	"github.com/umailserver/umailserver/internal/auth"
	"github.com/umailserver/umailserver/internal/av"
	"github.com/umailserver/umailserver/internal/smtp"
	"github.com/umailserver/umailserver/internal/spam"
)

// startSMTP creates and starts the inbound SMTP server with the message
// processing pipeline, plus the optional submission (587) and
// submission-TLS (465) servers.
func (s *Server) buildInboundSMTPPipeline() *smtp.Pipeline {
	pipeline := smtp.NewPipeline(smtp.NewPipelineLogger(s.logger))
	pipeline.SetTracingProvider(s.tracingProvider)

	resolver := smtp.NewNetDNSResolver()

	spfChecker := auth.NewSPFChecker(resolver)
	if ttl := s.cfg().Security.SPFCacheTTL.ToDuration(); ttl > 0 {
		spfChecker.SetCacheTTL(ttl)
	}
	dkimVerifier := auth.NewDKIMVerifier(resolver)
	dmarcEvaluator := auth.NewDMARCEvaluator(resolver)
	arcValidator := auth.NewARCValidator(resolver)

	dmarcStage := smtp.NewAuthDMARCStage(dmarcEvaluator, s.logger)

	dmarcInterval := 24 * time.Hour
	if s.cfg().DMARC.Interval != "" {
		parsedDMARCInterval, err := time.ParseDuration(s.cfg().DMARC.Interval)
		if err != nil {
			s.logger.Warn("Invalid DMARC interval, using default", "interval", s.cfg().DMARC.Interval, "error", err)
		} else {
			dmarcInterval = parsedDMARCInterval
		}
	}

	if s.cfg().DMARC.Enabled && s.cfg().DMARC.ReportEmail != "" {
		dmarcReporterConfig := auth.DMARCReporterConfig{
			OrgName:     s.cfg().DMARC.OrgName,
			FromEmail:   s.cfg().DMARC.FromEmail,
			ReportEmail: s.cfg().DMARC.ReportEmail,
			Interval:    dmarcInterval,
		}
		dmarcReporter := auth.NewDMARCReporter(resolver, s.logger, dmarcReporterConfig)
		dmarcStage.SetReporter(dmarcReporter)
		s.logger.Info("DMARC reporting enabled", "org", s.cfg().DMARC.OrgName)
	}

	pipeline.AddStage(smtp.NewAuthSPFStage(spfChecker, s.logger))
	pipeline.AddStage(smtp.NewAuthDKIMStage(dkimVerifier, s.logger))
	pipeline.AddStage(dmarcStage)
	pipeline.AddStage(smtp.NewAuthARCStage(arcValidator, s.logger))

	if s.rateLimiter != nil {
		pipeline.AddStage(smtp.NewRateLimitStage(s.rateLimiter))
	}

	if s.cfg().Spam.Enabled {
		if s.cfg().Spam.Greylisting.Enabled {
			pipeline.AddStage(smtp.NewGreylistStageWithDelay(s.cfg().Spam.Greylisting.Delay.ToDuration()))
		}
		if len(s.cfg().Spam.RBLServers) > 0 {
			pipeline.AddStage(smtp.NewRBLStage(s.cfg().Spam.RBLServers, smtp.NewRealRBLDNSResolver()))
		}
		pipeline.AddStage(smtp.NewHeuristicStage())

		var classifier *spam.Classifier
		if s.cfg().Spam.Bayesian.Enabled && s.spamStore != nil {
			candidate := spam.NewClassifier(s.spamStore)
			if err := candidate.Initialize(); err != nil {
				s.logger.Error("failed to initialize Bayesian classifier", "error", err)
			} else {
				classifier = candidate
				pipeline.AddStage(smtp.NewBayesianStage(candidate))
			}
		}

		autoTrain := s.cfg().Spam.Bayesian.AutoTrain && classifier != nil
		pipeline.AddStage(smtp.NewScoreStageWithOptions(
			s.cfg().Spam.RejectThreshold,
			s.cfg().Spam.QuarantineThreshold,
			s.cfg().Spam.JunkThreshold,
			classifier,
			autoTrain,
		))
	}

	if s.sieveManager != nil {
		sieveStage := smtp.NewSieveStage(s.sieveManager)
		sieveStage.SetVacationHandler(s.handleSieveVacation)
		pipeline.AddStage(sieveStage)
	}

	pipeline.AddStage(smtp.NewSMIMEStage(s.smimeKeystore))
	pipeline.AddStage(smtp.NewOpenPGPStage(s.openpgpKeystore))

	if s.cfg().AV.Enabled {
		avScanner := av.NewScanner(av.Config{
			Enabled: s.cfg().AV.Enabled,
			Addr:    s.cfg().AV.Addr,
			Timeout: s.cfg().AV.Timeout.ToDuration(),
			Action:  s.cfg().AV.Action,
		})
		pipeline.AddStage(smtp.NewAVStage(&avScannerAdapter{inner: avScanner}, s.cfg().AV.Action))
	}

	return pipeline
}

// buildSubmissionSMTPPipeline builds the message pipeline for the authenticated
// submission listeners (587/465). Unlike the inbound pipeline it deliberately
// omits the receive-side checks (SPF/DKIM/DMARC verification, greylisting, RBL,
// spam scoring) — those are wrong for a tenant's own outbound mail. It carries
// only the rate-limit stage so per-user and per-domain (tenant) send limits are
// actually enforced on the canonical send path. The stage shares s.rateLimiter,
// so live config reloads retune it without a rebuild.
func (s *Server) buildSubmissionSMTPPipeline() *smtp.Pipeline {
	pipeline := smtp.NewPipeline(smtp.NewPipelineLogger(s.logger))
	pipeline.SetTracingProvider(s.tracingProvider)
	if s.rateLimiter != nil {
		pipeline.AddStage(smtp.NewRateLimitStage(s.rateLimiter))
	}
	return pipeline
}

func (s *Server) buildSubmissionSMTPConfig() *smtp.Config {
	allowInsecure := !s.cfg().SMTP.Submission.RequireTLS

	cfg := &smtp.Config{
		Hostname:       s.cfg().Server.Hostname,
		MaxMessageSize: int64(s.cfg().SMTP.Inbound.MaxMessageSize),
		MaxRecipients:  s.cfg().SMTP.Inbound.MaxRecipients,
		MaxConnections: s.cfg().SMTP.Submission.MaxConnections,
		ReadTimeout:    s.cfg().SMTP.Inbound.ReadTimeout.ToDuration(),
		WriteTimeout:   s.cfg().SMTP.Inbound.WriteTimeout.ToDuration(),
		AllowInsecure:  allowInsecure,
		RequireAuth:    s.cfg().SMTP.Submission.RequireAuth,
		RequireTLS:     s.cfg().SMTP.Submission.RequireTLS,
		IsSubmission:   true,

		FutureReleaseEnabled:    s.cfg().ScheduledSend.Enabled,
		FutureReleaseMaxSeconds: s.cfg().ScheduledSend.MaxHorizonDays * 24 * 60 * 60,
	}
	// Only advertise STARTTLS when a usable certificate is configured; otherwise
	// STARTTLS-requiring clients fail the handshake instead of falling back.
	if s.tlsManager.IsEnabled() {
		cfg.TLSConfig = s.tlsManager.GetTLSConfig()
	}
	return cfg
}

func (s *Server) startSMTP() {
	if s.cfg().SMTP.Inbound.Enabled {
		smtpAddr := fmt.Sprintf("%s:%d", s.cfg().SMTP.Inbound.Bind, s.cfg().SMTP.Inbound.Port)
		smtpCfg := &smtp.Config{
			Hostname:       s.cfg().Server.Hostname,
			MaxMessageSize: int64(s.cfg().SMTP.Inbound.MaxMessageSize),
			MaxRecipients:  s.cfg().SMTP.Inbound.MaxRecipients,
			MaxConnections: s.cfg().SMTP.Inbound.MaxConnections,
			ReadTimeout:    s.cfg().SMTP.Inbound.ReadTimeout.ToDuration(),
			WriteTimeout:   s.cfg().SMTP.Inbound.WriteTimeout.ToDuration(),
		}
		// Only advertise STARTTLS when a usable certificate is configured;
		// otherwise STARTTLS-requiring clients fail the handshake.
		if s.tlsManager.IsEnabled() {
			smtpCfg.TLSConfig = s.tlsManager.GetTLSConfig()
		}

		smtpServer := smtp.NewServer(smtpCfg, s.logger)
		smtpServer.SetAuthHandler(s.authenticate)
		smtpServer.SetDeliveryHandlerWithSieve(s.deliverMessageWithSieve)
		smtpServer.SetLocalDomainFunc(s.isLocalDomainName)
		smtpServer.SetRecipientPolicyFunc(s.checkRecipientPolicy)
		// CRAM-MD5 disabled: HMAC-MD5 is cryptographically broken (CVE-2022-37454, etc.)
		// smtpServer.SetUserSecretHandler(s.getUserSecret)
		smtpServer.SetLoginResultHandler(s.protoLoginHandler("smtp"))
		smtpServer.SetAuthLimits(s.cfg().Security.MaxLoginAttempts, time.Duration(s.cfg().Security.LockoutDuration))
		smtpServer.SetLegacyRateLimits(s.cfg().Security.RateLimit.SMTPPerMinute, s.cfg().Security.RateLimit.SMTPPerHour)
		smtpServer.SetTracingProvider(s.tracingProvider)

		smtpServer.SetPipeline(s.buildInboundSMTPPipeline())

		go func() {
			if err := smtpServer.ListenAndServe(smtpAddr); err != nil {
				s.logger.Error("SMTP server error", "error", err)
			}
		}()
		s.smtpServer = smtpServer
		s.logger.Info("SMTP server started", "addr", smtpAddr)
	} else {
		s.logger.Info("SMTP inbound server disabled")
	}

	// Submission SMTP server (port 587, STARTTLS)
	if s.cfg().SMTP.Submission.Enabled {
		submissionAddr := fmt.Sprintf("%s:%d", s.cfg().SMTP.Submission.Bind, s.cfg().SMTP.Submission.Port)
		submissionCfg := s.buildSubmissionSMTPConfig()

		submissionServer := smtp.NewServer(submissionCfg, s.logger)
		submissionServer.SetAuthHandler(s.authenticate)
		// Route through submitMessageWithSieve so the RECIPIENT's Sieve script
		// (fileinto rules, OOF vacation) runs for locally delivered submissions,
		// matching the API/JMAP/EWS send path. The submission pipeline has no
		// Sieve stage, so the session's action list is always empty here.
		submissionServer.SetDeliveryHandlerWithSieve(func(from string, to []string, data []byte, _ []string) error {
			return s.submitMessageWithSieve(from, to, data)
		})
		// FUTURERELEASE (RFC 4865): a future HOLDFOR/HOLDUNTIL records the message
		// in the canonical scheduled store (owner = authenticated sender), released
		// at its time. fileSent files a Sent copy on release.
		submissionServer.SetScheduleHandler(func(from string, to []string, data []byte, sendAt time.Time) (string, error) {
			return s.scheduleSend(from, from, to, data, sendAt, "smtp", true)
		})
		submissionServer.SetLocalDomainFunc(s.isLocalDomainName)
		submissionServer.SetRecipientPolicyFunc(s.checkRecipientPolicy)
		// CRAM-MD5 disabled: HMAC-MD5 is cryptographically broken
		// submissionServer.SetUserSecretHandler(s.getUserSecret)
		submissionServer.SetAuthLimits(s.cfg().Security.MaxLoginAttempts, time.Duration(s.cfg().Security.LockoutDuration))
		submissionServer.SetLegacyRateLimits(s.cfg().Security.RateLimit.SMTPPerMinute, s.cfg().Security.RateLimit.SMTPPerHour)
		submissionServer.SetTracingProvider(s.tracingProvider)
		submissionServer.SetPipeline(s.buildSubmissionSMTPPipeline())

		go func() {
			if err := submissionServer.ListenAndServe(submissionAddr); err != nil {
				s.logger.Error("Submission server error", "error", err)
			}
		}()
		s.submissionServer = submissionServer
		s.logger.Info("Submission server started", "addr", submissionAddr)
	}

	// Submission TLS SMTP server (port 465, implicit TLS)
	if s.cfg().SMTP.SubmissionTLS.Enabled {
		submissionTLSAddr := fmt.Sprintf("%s:%d", s.cfg().SMTP.SubmissionTLS.Bind, s.cfg().SMTP.SubmissionTLS.Port)
		submissionTLSCfg := &smtp.Config{
			Hostname:       s.cfg().Server.Hostname,
			MaxMessageSize: int64(s.cfg().SMTP.Inbound.MaxMessageSize),
			MaxRecipients:  s.cfg().SMTP.Inbound.MaxRecipients,
			MaxConnections: s.cfg().SMTP.SubmissionTLS.MaxConnections,
			ReadTimeout:    s.cfg().SMTP.Inbound.ReadTimeout.ToDuration(),
			WriteTimeout:   s.cfg().SMTP.Inbound.WriteTimeout.ToDuration(),
			TLSConfig:      s.tlsManager.GetTLSConfig(),
			RequireAuth:    s.cfg().SMTP.SubmissionTLS.RequireAuth,
			RequireTLS:     false, // Already on TLS
			IsSubmission:   true,

			FutureReleaseEnabled:    s.cfg().ScheduledSend.Enabled,
			FutureReleaseMaxSeconds: s.cfg().ScheduledSend.MaxHorizonDays * 24 * 60 * 60,
		}

		submissionTLSServer := smtp.NewServer(submissionTLSCfg, s.logger)
		submissionTLSServer.SetAuthHandler(s.authenticate)
		// Same recipient-Sieve routing as the 587 submission listener.
		submissionTLSServer.SetDeliveryHandlerWithSieve(func(from string, to []string, data []byte, _ []string) error {
			return s.submitMessageWithSieve(from, to, data)
		})
		// FUTURERELEASE (RFC 4865), same as the 587 listener.
		submissionTLSServer.SetScheduleHandler(func(from string, to []string, data []byte, sendAt time.Time) (string, error) {
			return s.scheduleSend(from, from, to, data, sendAt, "smtp", true)
		})
		submissionTLSServer.SetLocalDomainFunc(s.isLocalDomainName)
		submissionTLSServer.SetRecipientPolicyFunc(s.checkRecipientPolicy)
		// CRAM-MD5 disabled: HMAC-MD5 is cryptographically broken
		// submissionTLSServer.SetUserSecretHandler(s.getUserSecret)
		submissionTLSServer.SetAuthLimits(s.cfg().Security.MaxLoginAttempts, time.Duration(s.cfg().Security.LockoutDuration))
		submissionTLSServer.SetLegacyRateLimits(s.cfg().Security.RateLimit.SMTPPerMinute, s.cfg().Security.RateLimit.SMTPPerHour)
		submissionTLSServer.SetTracingProvider(s.tracingProvider)
		submissionTLSServer.SetPipeline(s.buildSubmissionSMTPPipeline())

		tlsConfig := s.tlsManager.GetTLSConfig()
		go func() {
			if err := submissionTLSServer.ListenAndServeTLS(submissionTLSAddr, tlsConfig); err != nil {
				s.logger.Error("Submission TLS server error", "error", err)
			}
		}()
		s.submissionTLSServer = submissionTLSServer
		s.logger.Info("Submission TLS server started", "addr", submissionTLSAddr)
	}
}

// isLocalDomainName reports whether the given domain is a locally hosted,
// active domain. It backs the SMTP anti-relay policy: unauthenticated sessions
// may only address local recipients.
func (s *Server) isLocalDomainName(domain string) bool {
	if domain == "" || s.database == nil {
		return false
	}
	d, err := s.database.GetDomain(domain)
	return err == nil && d != nil && d.IsActive
}

// senderRestrictedToInternal reports whether the account identified by email is
// configured to send only to locally hosted recipients (SendPolicy=="internal").
// Absent accounts and the default empty policy are unrestricted (fail open — the
// policy only ever narrows the default-open behavior).
func (s *Server) senderRestrictedToInternal(email string) bool {
	if s.database == nil {
		return false
	}
	_, _, acct, err := s.loadLocalAccount(email)
	return err == nil && acct != nil && acct.SendPolicy == "internal"
}

// recipientRestrictedToInternal reports whether the account identified by email
// only accepts mail from locally hosted senders (ReceivePolicy=="internal").
func (s *Server) recipientRestrictedToInternal(email string) bool {
	if s.database == nil {
		return false
	}
	_, _, acct, err := s.loadLocalAccount(email)
	return err == nil && acct != nil && acct.ReceivePolicy == "internal"
}

// checkRecipientPolicy enforces per-account internal-only send/receive scope at
// RCPT TO. On submission (authUser != "") it gates the authenticated account's
// SendPolicy: an "internal" account may only address locally hosted recipients,
// rejected per-recipient so the rest of the message still flows. On the inbound
// listener (authUser == "") it gates the recipient account's ReceivePolicy: an
// "internal" account rejects senders outside the local domains. It returns
// (true, "") whenever delivery is permitted.
func (s *Server) checkRecipientPolicy(authUser, from, recipient string) (bool, string) {
	// Submission: gate the authenticated sender's outbound scope.
	if authUser != "" {
		if s.senderRestrictedToInternal(authUser) {
			_, rdomain := parseEmail(recipient)
			if !s.isLocalDomainName(rdomain) {
				return false, "5.7.1 External recipients are not permitted for this account"
			}
		}
		return true, ""
	}
	// Inbound: gate the recipient's inbound scope against the envelope sender.
	if s.recipientRestrictedToInternal(recipient) && !s.isLocalSender(from) {
		return false, "5.7.1 Recipient accepts internal mail only"
	}
	return true, ""
}

// sendPolicyViolation reports a non-empty rejection reason when the sender
// account is internal-only and any recipient lies outside the locally hosted
// domains; "" means the submission is permitted. It backs the shared submission
// path (submitMessageWithSieve) so webmail/EWS/JMAP sends honor the same scope
// the SMTP RCPT check enforces per-recipient.
func (s *Server) sendPolicyViolation(from string, to []string) string {
	if !s.senderRestrictedToInternal(from) {
		return ""
	}
	for _, recipient := range to {
		_, rdomain := parseEmail(recipient)
		if !s.isLocalDomainName(rdomain) {
			return "550 5.7.1 External recipients are not permitted for this account"
		}
	}
	return ""
}
