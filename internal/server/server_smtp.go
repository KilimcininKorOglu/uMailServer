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
	if ttl := s.config.Security.SPFCacheTTL.ToDuration(); ttl > 0 {
		spfChecker.SetCacheTTL(ttl)
	}
	dkimVerifier := auth.NewDKIMVerifier(resolver)
	dmarcEvaluator := auth.NewDMARCEvaluator(resolver)
	arcValidator := auth.NewARCValidator(resolver)

	dmarcStage := smtp.NewAuthDMARCStage(dmarcEvaluator, s.logger)

	dmarcInterval := 24 * time.Hour
	if s.config.DMARC.Interval != "" {
		parsedDMARCInterval, err := time.ParseDuration(s.config.DMARC.Interval)
		if err != nil {
			s.logger.Warn("Invalid DMARC interval, using default", "interval", s.config.DMARC.Interval, "error", err)
		} else {
			dmarcInterval = parsedDMARCInterval
		}
	}

	if s.config.DMARC.Enabled && s.config.DMARC.ReportEmail != "" {
		dmarcReporterConfig := auth.DMARCReporterConfig{
			OrgName:     s.config.DMARC.OrgName,
			FromEmail:   s.config.DMARC.FromEmail,
			ReportEmail: s.config.DMARC.ReportEmail,
			Interval:    dmarcInterval,
		}
		dmarcReporter := auth.NewDMARCReporter(resolver, s.logger, dmarcReporterConfig)
		dmarcStage.SetReporter(dmarcReporter)
		s.logger.Info("DMARC reporting enabled", "org", s.config.DMARC.OrgName)
	}

	pipeline.AddStage(smtp.NewAuthSPFStage(spfChecker, s.logger))
	pipeline.AddStage(smtp.NewAuthDKIMStage(dkimVerifier, s.logger))
	pipeline.AddStage(dmarcStage)
	pipeline.AddStage(smtp.NewAuthARCStage(arcValidator, s.logger))

	if s.rateLimiter != nil {
		pipeline.AddStage(smtp.NewRateLimitStage(s.rateLimiter))
	}

	if s.config.Spam.Enabled {
		if s.config.Spam.Greylisting.Enabled {
			pipeline.AddStage(smtp.NewGreylistStageWithDelay(s.config.Spam.Greylisting.Delay.ToDuration()))
		}
		if len(s.config.Spam.RBLServers) > 0 {
			pipeline.AddStage(smtp.NewRBLStage(s.config.Spam.RBLServers, smtp.NewRealRBLDNSResolver()))
		}
		pipeline.AddStage(smtp.NewHeuristicStage())

		var classifier *spam.Classifier
		if s.config.Spam.Bayesian.Enabled && s.storageDB != nil {
			candidate := spam.NewClassifier(s.storageDB.Bolt())
			if err := candidate.Initialize(); err != nil {
				s.logger.Error("failed to initialize Bayesian classifier", "error", err)
			} else {
				classifier = candidate
				pipeline.AddStage(smtp.NewBayesianStage(candidate))
			}
		}

		autoTrain := s.config.Spam.Bayesian.AutoTrain && classifier != nil
		pipeline.AddStage(smtp.NewScoreStageWithOptions(
			s.config.Spam.RejectThreshold,
			s.config.Spam.QuarantineThreshold,
			s.config.Spam.JunkThreshold,
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

	if s.config.AV.Enabled {
		avScanner := av.NewScanner(av.Config{
			Enabled: s.config.AV.Enabled,
			Addr:    s.config.AV.Addr,
			Timeout: s.config.AV.Timeout.ToDuration(),
			Action:  s.config.AV.Action,
		})
		pipeline.AddStage(smtp.NewAVStage(&avScannerAdapter{inner: avScanner}, s.config.AV.Action))
	}

	return pipeline
}

func (s *Server) buildSubmissionSMTPConfig() *smtp.Config {
	allowInsecure := !s.config.SMTP.Submission.RequireTLS

	return &smtp.Config{
		Hostname:       s.config.Server.Hostname,
		MaxMessageSize: int64(s.config.SMTP.Inbound.MaxMessageSize),
		MaxRecipients:  s.config.SMTP.Inbound.MaxRecipients,
		MaxConnections: s.config.SMTP.Submission.MaxConnections,
		ReadTimeout:    s.config.SMTP.Inbound.ReadTimeout.ToDuration(),
		WriteTimeout:   s.config.SMTP.Inbound.WriteTimeout.ToDuration(),
		AllowInsecure:  allowInsecure,
		TLSConfig:      s.tlsManager.GetTLSConfig(),
		RequireAuth:    s.config.SMTP.Submission.RequireAuth,
		RequireTLS:     s.config.SMTP.Submission.RequireTLS,
		IsSubmission:   true,
	}
}

func (s *Server) startSMTP() {
	if s.config.SMTP.Inbound.Enabled {
		smtpAddr := fmt.Sprintf("%s:%d", s.config.SMTP.Inbound.Bind, s.config.SMTP.Inbound.Port)
		smtpCfg := &smtp.Config{
			Hostname:       s.config.Server.Hostname,
			MaxMessageSize: int64(s.config.SMTP.Inbound.MaxMessageSize),
			MaxRecipients:  s.config.SMTP.Inbound.MaxRecipients,
			MaxConnections: s.config.SMTP.Inbound.MaxConnections,
			ReadTimeout:    s.config.SMTP.Inbound.ReadTimeout.ToDuration(),
			WriteTimeout:   s.config.SMTP.Inbound.WriteTimeout.ToDuration(),
			TLSConfig:      s.tlsManager.GetTLSConfig(),
		}

		smtpServer := smtp.NewServer(smtpCfg, s.logger)
		smtpServer.SetAuthHandler(s.authenticate)
		smtpServer.SetDeliveryHandlerWithSieve(s.deliverMessageWithSieve)
		smtpServer.SetLocalDomainFunc(s.isLocalDomainName)
		// CRAM-MD5 disabled: HMAC-MD5 is cryptographically broken (CVE-2022-37454, etc.)
		// smtpServer.SetUserSecretHandler(s.getUserSecret)
		smtpServer.SetLoginResultHandler(s.protoLoginHandler("smtp"))
		smtpServer.SetAuthLimits(s.config.Security.MaxLoginAttempts, time.Duration(s.config.Security.LockoutDuration))
		smtpServer.SetLegacyRateLimits(s.config.Security.RateLimit.SMTPPerMinute, s.config.Security.RateLimit.SMTPPerHour)
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
	if s.config.SMTP.Submission.Enabled {
		submissionAddr := fmt.Sprintf("%s:%d", s.config.SMTP.Submission.Bind, s.config.SMTP.Submission.Port)
		submissionCfg := s.buildSubmissionSMTPConfig()

		submissionServer := smtp.NewServer(submissionCfg, s.logger)
		submissionServer.SetAuthHandler(s.authenticate)
		submissionServer.SetDeliveryHandlerWithSieve(s.deliverMessageWithSieve)
		submissionServer.SetLocalDomainFunc(s.isLocalDomainName)
		// CRAM-MD5 disabled: HMAC-MD5 is cryptographically broken
		// submissionServer.SetUserSecretHandler(s.getUserSecret)
		submissionServer.SetAuthLimits(s.config.Security.MaxLoginAttempts, time.Duration(s.config.Security.LockoutDuration))
		submissionServer.SetLegacyRateLimits(s.config.Security.RateLimit.SMTPPerMinute, s.config.Security.RateLimit.SMTPPerHour)
		submissionServer.SetTracingProvider(s.tracingProvider)

		go func() {
			if err := submissionServer.ListenAndServe(submissionAddr); err != nil {
				s.logger.Error("Submission server error", "error", err)
			}
		}()
		s.submissionServer = submissionServer
		s.logger.Info("Submission server started", "addr", submissionAddr)
	}

	// Submission TLS SMTP server (port 465, implicit TLS)
	if s.config.SMTP.SubmissionTLS.Enabled {
		submissionTLSAddr := fmt.Sprintf("%s:%d", s.config.SMTP.SubmissionTLS.Bind, s.config.SMTP.SubmissionTLS.Port)
		submissionTLSCfg := &smtp.Config{
			Hostname:       s.config.Server.Hostname,
			MaxMessageSize: int64(s.config.SMTP.Inbound.MaxMessageSize),
			MaxRecipients:  s.config.SMTP.Inbound.MaxRecipients,
			MaxConnections: s.config.SMTP.SubmissionTLS.MaxConnections,
			ReadTimeout:    s.config.SMTP.Inbound.ReadTimeout.ToDuration(),
			WriteTimeout:   s.config.SMTP.Inbound.WriteTimeout.ToDuration(),
			TLSConfig:      s.tlsManager.GetTLSConfig(),
			RequireAuth:    s.config.SMTP.SubmissionTLS.RequireAuth,
			RequireTLS:     false, // Already on TLS
			IsSubmission:   true,
		}

		submissionTLSServer := smtp.NewServer(submissionTLSCfg, s.logger)
		submissionTLSServer.SetAuthHandler(s.authenticate)
		submissionTLSServer.SetDeliveryHandlerWithSieve(s.deliverMessageWithSieve)
		submissionTLSServer.SetLocalDomainFunc(s.isLocalDomainName)
		// CRAM-MD5 disabled: HMAC-MD5 is cryptographically broken
		// submissionTLSServer.SetUserSecretHandler(s.getUserSecret)
		submissionTLSServer.SetAuthLimits(s.config.Security.MaxLoginAttempts, time.Duration(s.config.Security.LockoutDuration))
		submissionTLSServer.SetLegacyRateLimits(s.config.Security.RateLimit.SMTPPerMinute, s.config.Security.RateLimit.SMTPPerHour)
		submissionTLSServer.SetTracingProvider(s.tracingProvider)

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
