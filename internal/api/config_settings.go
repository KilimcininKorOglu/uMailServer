package api

import (
	"net/http"
	"slices"

	"github.com/umailserver/umailserver/internal/config"
	"gopkg.in/yaml.v3"
)

// serverConfigDTO is the flat, secrets-free view of server settings shown on the
// admin Settings page (web/admin/src/pages/Settings.tsx). It deliberately omits
// every secret (JWT/TOTP/VAPID/LDAP/alert credentials).
type serverConfigDTO struct {
	Hostname         string `json:"hostname"`
	DataDir          string `json:"data_dir"`
	SMTPPort         int    `json:"smtp_port"`
	SubmissionPort   int    `json:"submission_port"`
	IMAPPort         int    `json:"imap_port"`
	MaxMessageSizeMB int    `json:"max_message_size_mb"`
	MaxRecipients    int    `json:"max_recipients"`
	MaxEmailsPerHour int    `json:"max_emails_per_hour"`
	GreylistEnabled  bool   `json:"greylisting_enabled"`
	AutoTLS          bool   `json:"auto_tls"`
	RequireTLSSMTP   bool   `json:"require_tls_smtp"`
	DKIMSigning      bool   `json:"dkim_signing"`
	MaxLoginAttempts int    `json:"max_login_attempts"`

	// Out-of-Office defaults (server-wide template).
	OOFDefaultEnabled bool   `json:"oof_default_enabled"`
	OOFInternalOnly   bool   `json:"oof_internal_only"`
	OOFDefaultSubject string `json:"oof_default_subject"`
	OOFDefaultMessage string `json:"oof_default_message"`

	// Notification preferences.
	NotifyQueueAlerts    bool `json:"notify_queue_alerts"`
	NotifySecurityAlerts bool `json:"notify_security_alerts"`
	NotifyWeeklyReports  bool `json:"notify_weekly_reports"`
}

// configPutResponse reports the outcome of a config update. applied lists the
// fields that took effect live; restartRequired lists fields that were persisted
// but need a restart to take effect.
type configPutResponse struct {
	Status          string   `json:"status"`
	Applied         []string `json:"applied"`
	RestartRequired []string `json:"restart_required"`
	Message         string   `json:"message"`
}

// SetConfigManager injects the loaded config and its file path so the admin
// config API can read and persist runtime settings. An empty path disables
// persistence (the PUT handler will return 503).
func (s *Server) SetConfigManager(cfg *config.Config, path string) {
	s.configMu.Lock()
	defer s.configMu.Unlock()
	s.liveConfig = cfg
	s.configPath = path
}

// handleConfig handles GET/PUT /api/v1/admin/config.
func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.handleGetConfig(w, r)
	case http.MethodPut:
		s.handlePutConfig(w, r)
	default:
		s.sendError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (s *Server) handleGetConfig(w http.ResponseWriter, _ *http.Request) {
	s.configMu.Lock()
	cfg := s.liveConfig
	s.configMu.Unlock()
	if cfg == nil {
		s.sendError(w, http.StatusServiceUnavailable, "config not available")
		return
	}
	s.sendJSON(w, http.StatusOK, configToDTO(cfg))
}

func (s *Server) handlePutConfig(w http.ResponseWriter, r *http.Request) {
	s.configMu.Lock()
	cur := s.liveConfig
	path := s.configPath
	s.configMu.Unlock()

	if cur == nil {
		s.sendError(w, http.StatusServiceUnavailable, "config not available")
		return
	}
	if path == "" {
		s.sendError(w, http.StatusServiceUnavailable, "no config file loaded; cannot persist changes")
		return
	}

	var req serverConfigDTO
	if err := decodeJSON(r, &req); err != nil {
		s.sendError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if msg, ok := validateConfigDTO(&req); !ok {
		s.sendError(w, http.StatusBadRequest, msg)
		return
	}

	// Work on a deep clone so a validation failure never touches the live config.
	clone, err := cloneConfig(cur)
	if err != nil {
		s.sendError(w, http.StatusInternalServerError, "failed to clone config")
		return
	}

	applied, restart := applyConfigDTO(clone, configToDTO(cur), &req)

	if err := clone.Validate(); err != nil {
		s.sendError(w, http.StatusBadRequest, "resulting config is invalid: "+err.Error())
		return
	}
	if err := config.SaveAtomic(clone, path); err != nil {
		s.sendError(w, http.StatusInternalServerError, "failed to persist config: "+err.Error())
		return
	}

	// Hot-apply the rate limit (the only setting that can take effect live) and
	// swap the live config to the clone so subsequent GETs reflect the change.
	if slices.Contains(applied, "max_emails_per_hour") && s.rateLimitMgr != nil {
		rl := s.rateLimitMgr.GetConfig()
		if rl != nil {
			newRL := *rl
			newRL.UserPerHour = req.MaxEmailsPerHour
			s.rateLimitMgr.SetConfig(&newRL)
		}
	}
	s.configMu.Lock()
	s.liveConfig = clone
	s.configMu.Unlock()

	msg := "Settings saved."
	if len(restart) > 0 {
		msg = "Settings saved. A server restart is required for some changes to take effect."
	}
	s.sendJSON(w, http.StatusOK, configPutResponse{
		Status:          "saved",
		Applied:         applied,
		RestartRequired: restart,
		Message:         msg,
	})
}

// configToDTO projects a Config into the flat settings view.
func configToDTO(cfg *config.Config) serverConfigDTO {
	return serverConfigDTO{
		Hostname:         cfg.Server.Hostname,
		DataDir:          cfg.Server.DataDir,
		SMTPPort:         cfg.SMTP.Inbound.Port,
		SubmissionPort:   cfg.SMTP.Submission.Port,
		IMAPPort:         cfg.IMAP.Port,
		MaxMessageSizeMB: int(int64(cfg.SMTP.Inbound.MaxMessageSize) / int64(config.MB)),
		MaxRecipients:    cfg.SMTP.Inbound.MaxRecipients,
		MaxEmailsPerHour: cfg.Security.RateLimit.UserPerHour,
		GreylistEnabled:  cfg.Spam.Greylisting.Enabled,
		AutoTLS:          cfg.TLS.ACME.Enabled,
		RequireTLSSMTP:   cfg.SMTP.Submission.RequireTLS,
		DKIMSigning:      cfg.Signing.Enabled,
		MaxLoginAttempts: cfg.Security.MaxLoginAttempts,

		OOFDefaultEnabled: cfg.OOF.DefaultEnabled,
		OOFInternalOnly:   cfg.OOF.InternalOnly,
		OOFDefaultSubject: cfg.OOF.DefaultSubject,
		OOFDefaultMessage: cfg.OOF.DefaultMessage,

		NotifyQueueAlerts:    cfg.Notifications.QueueAlerts,
		NotifySecurityAlerts: cfg.Notifications.SecurityAlerts,
		NotifyWeeklyReports:  cfg.Notifications.WeeklyReports,
	}
}

// applyConfigDTO writes the request fields onto cfg and returns which changed
// fields are applied-live versus restart-required. Only the rate limit can be
// applied live; everything else is captured into listeners/managers at startup,
// so changing it requires a restart (reported honestly per Rule 12).
func applyConfigDTO(cfg *config.Config, before serverConfigDTO, req *serverConfigDTO) (applied, restart []string) {
	hot := func(field string) { applied = append(applied, field) }
	cold := func(field string) { restart = append(restart, field) }

	if req.Hostname != before.Hostname {
		cfg.Server.Hostname = req.Hostname
		cold("hostname")
	}
	if req.DataDir != before.DataDir {
		cfg.Server.DataDir = req.DataDir
		cold("data_dir")
	}
	if req.SMTPPort != before.SMTPPort {
		cfg.SMTP.Inbound.Port = req.SMTPPort
		cold("smtp_port")
	}
	if req.SubmissionPort != before.SubmissionPort {
		cfg.SMTP.Submission.Port = req.SubmissionPort
		cold("submission_port")
	}
	if req.IMAPPort != before.IMAPPort {
		cfg.IMAP.Port = req.IMAPPort
		cold("imap_port")
	}
	if req.MaxMessageSizeMB != before.MaxMessageSizeMB {
		cfg.SMTP.Inbound.MaxMessageSize = config.Size(int64(req.MaxMessageSizeMB) * int64(config.MB))
		cold("max_message_size_mb")
	}
	if req.MaxRecipients != before.MaxRecipients {
		cfg.SMTP.Inbound.MaxRecipients = req.MaxRecipients
		cold("max_recipients")
	}
	if req.MaxEmailsPerHour != before.MaxEmailsPerHour {
		cfg.Security.RateLimit.UserPerHour = req.MaxEmailsPerHour
		hot("max_emails_per_hour")
	}
	if req.GreylistEnabled != before.GreylistEnabled {
		cfg.Spam.Greylisting.Enabled = req.GreylistEnabled
		cold("greylisting_enabled")
	}
	if req.AutoTLS != before.AutoTLS {
		cfg.TLS.ACME.Enabled = req.AutoTLS
		cold("auto_tls")
	}
	if req.RequireTLSSMTP != before.RequireTLSSMTP {
		cfg.SMTP.Submission.RequireTLS = req.RequireTLSSMTP
		cold("require_tls_smtp")
	}
	if req.DKIMSigning != before.DKIMSigning {
		cfg.Signing.Enabled = req.DKIMSigning
		cold("dkim_signing")
	}
	if req.MaxLoginAttempts != before.MaxLoginAttempts {
		cfg.Security.MaxLoginAttempts = req.MaxLoginAttempts
		cold("max_login_attempts")
	}

	// OOF defaults and notification preferences are pure persisted settings:
	// the swapped live config reflects them immediately, so they are applied
	// live (no listener/manager to restart).
	if req.OOFDefaultEnabled != before.OOFDefaultEnabled {
		cfg.OOF.DefaultEnabled = req.OOFDefaultEnabled
		hot("oof_default_enabled")
	}
	if req.OOFInternalOnly != before.OOFInternalOnly {
		cfg.OOF.InternalOnly = req.OOFInternalOnly
		hot("oof_internal_only")
	}
	if req.OOFDefaultSubject != before.OOFDefaultSubject {
		cfg.OOF.DefaultSubject = req.OOFDefaultSubject
		hot("oof_default_subject")
	}
	if req.OOFDefaultMessage != before.OOFDefaultMessage {
		cfg.OOF.DefaultMessage = req.OOFDefaultMessage
		hot("oof_default_message")
	}
	if req.NotifyQueueAlerts != before.NotifyQueueAlerts {
		cfg.Notifications.QueueAlerts = req.NotifyQueueAlerts
		hot("notify_queue_alerts")
	}
	if req.NotifySecurityAlerts != before.NotifySecurityAlerts {
		cfg.Notifications.SecurityAlerts = req.NotifySecurityAlerts
		hot("notify_security_alerts")
	}
	if req.NotifyWeeklyReports != before.NotifyWeeklyReports {
		cfg.Notifications.WeeklyReports = req.NotifyWeeklyReports
		hot("notify_weekly_reports")
	}
	return applied, restart
}

// validateConfigDTO bounds-checks incoming values before any write.
func validateConfigDTO(req *serverConfigDTO) (string, bool) {
	if req.Hostname == "" {
		return "hostname is required", false
	}
	if req.DataDir == "" {
		return "data_dir is required", false
	}
	for _, p := range []int{req.SMTPPort, req.SubmissionPort, req.IMAPPort} {
		if p < 1 || p > 65535 {
			return "ports must be between 1 and 65535", false
		}
	}
	if req.MaxMessageSizeMB < 1 || req.MaxMessageSizeMB > 1024 {
		return "max_message_size_mb must be between 1 and 1024", false
	}
	if req.MaxRecipients < 1 || req.MaxRecipients > 10000 {
		return "max_recipients must be between 1 and 10000", false
	}
	if req.MaxEmailsPerHour < 1 || req.MaxEmailsPerHour > 1000000 {
		return "max_emails_per_hour must be between 1 and 1000000", false
	}
	if req.MaxLoginAttempts < 1 || req.MaxLoginAttempts > 1000 {
		return "max_login_attempts must be between 1 and 1000", false
	}
	if len(req.OOFDefaultSubject) > 255 {
		return "oof_default_subject exceeds maximum length of 255", false
	}
	if len(req.OOFDefaultMessage) > 5000 {
		return "oof_default_message exceeds maximum length of 5000", false
	}
	return "", true
}

// cloneConfig deep-copies a Config via a YAML round-trip.
func cloneConfig(cfg *config.Config) (*config.Config, error) {
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return nil, err
	}
	var out config.Config
	if err := yaml.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
