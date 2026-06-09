package api

import (
	"net/http"
	"reflect"
	"time"

	"github.com/umailserver/umailserver/internal/config"
	"gopkg.in/yaml.v3"
)

// serverConfigDTO is the typed, per-section, secrets-free view of server
// settings shown on the admin Settings page (web/admin/src/pages/Settings.tsx).
// Every secret is deliberately omitted (JWT/TOTP keys, LDAP bind password, MCP
// auth tokens, alert SMTP password and webhook headers, VAPID private key); the
// running server keeps those values from the on-disk YAML untouched across a
// settings save. Durations are expressed in whole seconds and message sizes in
// whole megabytes so the values map cleanly onto form inputs.
type serverConfigDTO struct {
	Server        serverSectionDTO        `json:"server"`
	TLS           tlsSectionDTO           `json:"tls"`
	SMTP          smtpSectionDTO          `json:"smtp"`
	IMAP          imapSectionDTO          `json:"imap"`
	POP3          pop3SectionDTO          `json:"pop3"`
	HTTP          httpSectionDTO          `json:"http"`
	Admin         adminSectionDTO         `json:"admin"`
	Spam          spamSectionDTO          `json:"spam"`
	AV            avSectionDTO            `json:"av"`
	Security      securitySectionDTO      `json:"security"`
	LDAP          ldapSectionDTO          `json:"ldap"`
	MCP           mcpSectionDTO           `json:"mcp"`
	ManageSieve   serviceSectionDTO       `json:"managesieve"`
	Logging       loggingSectionDTO       `json:"logging"`
	Metrics       metricsSectionDTO       `json:"metrics"`
	Tracing       tracingSectionDTO       `json:"tracing"`
	Database      databaseSectionDTO      `json:"database"`
	Storage       storageSectionDTO       `json:"storage"`
	CalDAV        serviceSectionDTO       `json:"caldav"`
	CardDAV       serviceSectionDTO       `json:"carddav"`
	JMAP          jmapSectionDTO          `json:"jmap"`
	DMARC         dmarcSectionDTO         `json:"dmarc"`
	Alert         alertSectionDTO         `json:"alert"`
	Push          pushSectionDTO          `json:"push"`
	Signing       signingSectionDTO       `json:"signing"`
	OOF           oofSectionDTO           `json:"oof"`
	Notifications notificationsSectionDTO `json:"notifications"`
	ScheduledSend scheduledSendSectionDTO `json:"scheduled_send"`
}

type serverSectionDTO struct {
	Hostname            string `json:"hostname"`
	DataDir             string `json:"data_dir"`
	GracefulTimeoutSecs int    `json:"graceful_timeout_secs"`
	ForceCloseAfterSecs int    `json:"force_close_after_secs"`
}

type acmeSectionDTO struct {
	Enabled     bool   `json:"enabled"`
	Email       string `json:"email"`
	Provider    string `json:"provider"`
	Challenge   string `json:"challenge"`
	DNSProvider string `json:"dns_provider"`
}

type clientAuthSectionDTO struct {
	Enabled     bool   `json:"enabled"`
	RequireCert bool   `json:"require_cert"`
	CAFile      string `json:"ca_file"`
	VerifyMode  string `json:"verify_mode"`
}

type tlsSectionDTO struct {
	ACME       acmeSectionDTO       `json:"acme"`
	CertFile   string               `json:"cert_file"`
	KeyFile    string               `json:"key_file"`
	MinVersion string               `json:"min_version"`
	ClientAuth clientAuthSectionDTO `json:"client_auth"`
}

type inboundSMTPSectionDTO struct {
	Enabled          bool   `json:"enabled"`
	Port             int    `json:"port"`
	Bind             string `json:"bind"`
	MaxMessageSizeMB int    `json:"max_message_size_mb"`
	MaxRecipients    int    `json:"max_recipients"`
	MaxConnections   int    `json:"max_connections"`
	ReadTimeoutSecs  int    `json:"read_timeout_secs"`
	WriteTimeoutSecs int    `json:"write_timeout_secs"`
}

type submissionSMTPSectionDTO struct {
	Enabled        bool   `json:"enabled"`
	Port           int    `json:"port"`
	Bind           string `json:"bind"`
	RequireAuth    bool   `json:"require_auth"`
	RequireTLS     bool   `json:"require_tls"`
	MaxConnections int    `json:"max_connections"`
}

type submissionTLSSectionDTO struct {
	Enabled        bool   `json:"enabled"`
	Port           int    `json:"port"`
	Bind           string `json:"bind"`
	RequireAuth    bool   `json:"require_auth"`
	MaxConnections int    `json:"max_connections"`
}

type smtpSectionDTO struct {
	Inbound       inboundSMTPSectionDTO    `json:"inbound"`
	Submission    submissionSMTPSectionDTO `json:"submission"`
	SubmissionTLS submissionTLSSectionDTO  `json:"submission_tls"`
}

type imapSectionDTO struct {
	Enabled         bool   `json:"enabled"`
	Port            int    `json:"port"`
	Bind            string `json:"bind"`
	STARTTLSPort    int    `json:"starttls_port"`
	IdleTimeoutSecs int    `json:"idle_timeout_secs"`
	MaxConnections  int    `json:"max_connections"`
}

type pop3SectionDTO struct {
	Enabled        bool   `json:"enabled"`
	Port           int    `json:"port"`
	Bind           string `json:"bind"`
	MaxConnections int    `json:"max_connections"`
}

type httpSectionDTO struct {
	Enabled        bool     `json:"enabled"`
	Port           int      `json:"port"`
	HTTPPort       int      `json:"http_port"`
	Bind           string   `json:"bind"`
	CorsOrigins    []string `json:"cors_origins"`
	TrustedProxies []string `json:"trusted_proxies"`
}

type adminSectionDTO struct {
	Enabled bool   `json:"enabled"`
	Port    int    `json:"port"`
	Bind    string `json:"bind"`
}

// serviceSectionDTO is the shared shape for the simple enable/bind/port services
// (ManageSieve, CalDAV, CardDAV).
type serviceSectionDTO struct {
	Enabled bool   `json:"enabled"`
	Port    int    `json:"port"`
	Bind    string `json:"bind"`
}

type spamSectionDTO struct {
	Enabled             bool     `json:"enabled"`
	RejectThreshold     float64  `json:"reject_threshold"`
	JunkThreshold       float64  `json:"junk_threshold"`
	QuarantineThreshold float64  `json:"quarantine_threshold"`
	BayesianEnabled     bool     `json:"bayesian_enabled"`
	BayesianAutoTrain   bool     `json:"bayesian_auto_train"`
	GreylistingEnabled  bool     `json:"greylisting_enabled"`
	GreylistDelaySecs   int      `json:"greylist_delay_secs"`
	RBLServers          []string `json:"rbl_servers"`
}

type avSectionDTO struct {
	Enabled     bool   `json:"enabled"`
	Addr        string `json:"addr"`
	TimeoutSecs int    `json:"timeout_secs"`
	Action      string `json:"action"`
}

type rateLimitSectionDTO struct {
	IPPerMinute           int `json:"ip_per_minute"`
	IPPerHour             int `json:"ip_per_hour"`
	IPPerDay              int `json:"ip_per_day"`
	IPConnections         int `json:"ip_connections"`
	UserPerMinute         int `json:"user_per_minute"`
	UserPerHour           int `json:"user_per_hour"`
	UserPerDay            int `json:"user_per_day"`
	UserMaxRecipients     int `json:"user_max_recipients"`
	GlobalPerMinute       int `json:"global_per_minute"`
	GlobalPerHour         int `json:"global_per_hour"`
	DomainPerMinute       int `json:"domain_per_minute"`
	DomainPerHour         int `json:"domain_per_hour"`
	DomainPerDay          int `json:"domain_per_day"`
	SMTPPerMinute         int `json:"smtp_per_minute"`
	SMTPPerHour           int `json:"smtp_per_hour"`
	IMAPConnections       int `json:"imap_connections"`
	HTTPRequestsPerMinute int `json:"http_requests_per_minute"`
}

type auditLogSectionDTO struct {
	Path       string `json:"path"`
	MaxSizeMB  int    `json:"max_size_mb"`
	MaxBackups int    `json:"max_backups"`
	MaxAgeDays int    `json:"max_age_days"`
}

type securitySectionDTO struct {
	MaxLoginAttempts int                 `json:"max_login_attempts"`
	LockoutSecs      int                 `json:"lockout_secs"`
	DisableLegacyJWT bool                `json:"disable_legacy_jwt"`
	SPFCacheTTLSecs  int                 `json:"spf_cache_ttl_secs"`
	RateLimit        rateLimitSectionDTO `json:"rate_limit"`
	AuditLog         auditLogSectionDTO  `json:"audit_log"`
}

type ldapSectionDTO struct {
	Enabled        bool     `json:"enabled"`
	URL            string   `json:"url"`
	BindDN         string   `json:"bind_dn"`
	BaseDN         string   `json:"base_dn"`
	UserFilter     string   `json:"user_filter"`
	EmailAttribute string   `json:"email_attribute"`
	NameAttribute  string   `json:"name_attribute"`
	GroupAttribute string   `json:"group_attribute"`
	AdminGroups    []string `json:"admin_groups"`
	StartTLS       bool     `json:"start_tls"`
	SkipVerify     bool     `json:"skip_verify"`
	RootCA         string   `json:"root_ca"`
	TimeoutSecs    int      `json:"timeout_secs"`
}

type mcpSectionDTO struct {
	Enabled bool   `json:"enabled"`
	Port    int    `json:"port"`
	Bind    string `json:"bind"`
}

type loggingSectionDTO struct {
	Level      string `json:"level"`
	Format     string `json:"format"`
	Output     string `json:"output"`
	MaxSizeMB  int    `json:"max_size_mb"`
	MaxBackups int    `json:"max_backups"`
	MaxAgeDays int    `json:"max_age_days"`
}

type metricsSectionDTO struct {
	Enabled bool   `json:"enabled"`
	Port    int    `json:"port"`
	Bind    string `json:"bind"`
	Path    string `json:"path"`
}

type tracingSectionDTO struct {
	Enabled      bool    `json:"enabled"`
	ServiceName  string  `json:"service_name"`
	Exporter     string  `json:"exporter"`
	OTLPEndpoint string  `json:"otlp_endpoint"`
	Environment  string  `json:"environment"`
	SampleRate   float64 `json:"sample_rate"`
}

type databaseSectionDTO struct {
	Path    string `json:"path"`
	Backend string `json:"backend"`
}

type storageSectionDTO struct {
	Sync          bool `json:"sync"`
	SharedFolders bool `json:"shared_folders"`
}

type jmapSectionDTO struct {
	Enabled     bool     `json:"enabled"`
	Port        int      `json:"port"`
	Bind        string   `json:"bind"`
	CorsOrigins []string `json:"cors_origins"`
}

type dmarcSectionDTO struct {
	Enabled     bool   `json:"enabled"`
	OrgName     string `json:"org_name"`
	FromEmail   string `json:"from_email"`
	ReportEmail string `json:"report_email"`
	Interval    string `json:"interval"`
}

type alertSectionDTO struct {
	Enabled         bool     `json:"enabled"`
	WebhookURL      string   `json:"webhook_url"`
	SMTPServer      string   `json:"smtp_server"`
	SMTPPort        int      `json:"smtp_port"`
	SMTPUsername    string   `json:"smtp_username"`
	FromAddress     string   `json:"from_address"`
	ToAddresses     []string `json:"to_addresses"`
	UseTLS          bool     `json:"use_tls"`
	MinIntervalSecs int      `json:"min_interval_secs"`
	MaxAlerts       int      `json:"max_alerts"`
	DiskThreshold   float64  `json:"disk_threshold"`
	MemoryThreshold float64  `json:"memory_threshold"`
	ErrorThreshold  float64  `json:"error_threshold"`
	TLSWarningDays  int      `json:"tls_warning_days"`
	QueueThreshold  int      `json:"queue_threshold"`
	AllowPrivateIP  bool     `json:"allow_private_ip"`
}

type pushSectionDTO struct {
	Enabled        bool   `json:"enabled"`
	Subject        string `json:"subject"`
	VAPIDPublicKey string `json:"vapid_public_key"`
}

type signingSectionDTO struct {
	Enabled bool   `json:"enabled"`
	KeyDir  string `json:"key_dir"`
}

type oofSectionDTO struct {
	DefaultEnabled bool   `json:"default_enabled"`
	InternalOnly   bool   `json:"internal_only"`
	DefaultSubject string `json:"default_subject"`
	DefaultMessage string `json:"default_message"`
}

type notificationsSectionDTO struct {
	QueueAlerts    bool `json:"queue_alerts"`
	SecurityAlerts bool `json:"security_alerts"`
	WeeklyReports  bool `json:"weekly_reports"`
}

type scheduledSendSectionDTO struct {
	Enabled        bool `json:"enabled"`
	MaxHorizonDays int  `json:"max_horizon_days"`
	TickSeconds    int  `json:"tick_seconds"`
	MaxPerUser     int  `json:"max_per_user"`
}

// configPutResponse reports the outcome of a config update. applied lists the
// sections that took effect live; restartRequired lists sections that were
// persisted but need a restart to take effect.
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

// SetConfigReloader registers the callback that applies a persisted config
// change to the running server. When set, a successful PUT applies the change
// live through it and reports the reloader's section-level result, so the admin
// sees what actually took effect rather than the DTO's static guess.
func (s *Server) SetConfigReloader(fn func(newCfg *config.Config) (applied, restartRequired []string)) {
	s.configMu.Lock()
	defer s.configMu.Unlock()
	s.configReloader = fn
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
	reloader := s.configReloader
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
	// The clone carries every current value, including secrets, so applying the
	// secrets-free DTO onto it leaves the secrets intact.
	clone, err := cloneConfig(cur)
	if err != nil {
		s.sendError(w, http.StatusInternalServerError, "failed to clone config")
		return
	}

	applyConfigDTO(clone, &req)

	if err := clone.Validate(); err != nil {
		s.sendError(w, http.StatusBadRequest, "resulting config is invalid: "+err.Error())
		return
	}
	if err := config.SaveAtomic(clone, path); err != nil {
		s.sendError(w, http.StatusInternalServerError, "failed to persist config: "+err.Error())
		return
	}

	var applied, restart []string
	if reloader != nil {
		// The running server applies the change live and is the source of truth
		// for what took effect.
		applied, restart = reloader(clone)
	} else {
		// Standalone fallback (no reloader wired, e.g. in tests): the API can
		// hot-apply the global rate limit on its own; everything else that
		// changed needs a restart.
		if !reflect.DeepEqual(cur.Security.RateLimit, clone.Security.RateLimit) && s.rateLimitMgr != nil {
			if rl := s.rateLimitMgr.GetConfig(); rl != nil {
				newRL := *rl
				newRL.UserPerHour = clone.Security.RateLimit.UserPerHour
				s.rateLimitMgr.SetConfig(&newRL)
				applied = append(applied, "security.rate_limit")
			}
		}
		restart = changedSections(cur, clone, applied)
	}

	// Swap the live config to the clone so subsequent GETs reflect the change.
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

// durSecs / secsToDur / mbOf / mbToSize convert between the config package's
// Duration/Size types and the DTO's whole-second / whole-megabyte integers.
func durSecs(d config.Duration) int   { return int(d.ToDuration() / time.Second) }
func secsToDur(s int) config.Duration { return config.Duration(time.Duration(s) * time.Second) }
func goDurSecs(d time.Duration) int   { return int(d / time.Second) }
func secsToGoDur(s int) time.Duration { return time.Duration(s) * time.Second }
func mbOf(s config.Size) int          { return int(int64(s) / int64(config.MB)) }
func mbToSize(mb int) config.Size     { return config.Size(int64(mb) * int64(config.MB)) }

// configToDTO projects a Config into the typed, secrets-free settings view.
func configToDTO(cfg *config.Config) serverConfigDTO {
	return serverConfigDTO{
		Server: serverSectionDTO{
			Hostname:            cfg.Server.Hostname,
			DataDir:             cfg.Server.DataDir,
			GracefulTimeoutSecs: cfg.Server.GracefulTimeout,
			ForceCloseAfterSecs: cfg.Server.ForceCloseAfter,
		},
		TLS: tlsSectionDTO{
			ACME: acmeSectionDTO{
				Enabled:     cfg.TLS.ACME.Enabled,
				Email:       cfg.TLS.ACME.Email,
				Provider:    cfg.TLS.ACME.Provider,
				Challenge:   cfg.TLS.ACME.Challenge,
				DNSProvider: cfg.TLS.ACME.DNSProvider,
			},
			CertFile:   cfg.TLS.CertFile,
			KeyFile:    cfg.TLS.KeyFile,
			MinVersion: cfg.TLS.MinVersion,
			ClientAuth: clientAuthSectionDTO{
				Enabled:     cfg.TLS.ClientAuth.Enabled,
				RequireCert: cfg.TLS.ClientAuth.RequireCert,
				CAFile:      cfg.TLS.ClientAuth.CAFile,
				VerifyMode:  cfg.TLS.ClientAuth.VerifyMode,
			},
		},
		SMTP: smtpSectionDTO{
			Inbound: inboundSMTPSectionDTO{
				Enabled:          cfg.SMTP.Inbound.Enabled,
				Port:             cfg.SMTP.Inbound.Port,
				Bind:             cfg.SMTP.Inbound.Bind,
				MaxMessageSizeMB: mbOf(cfg.SMTP.Inbound.MaxMessageSize),
				MaxRecipients:    cfg.SMTP.Inbound.MaxRecipients,
				MaxConnections:   cfg.SMTP.Inbound.MaxConnections,
				ReadTimeoutSecs:  durSecs(cfg.SMTP.Inbound.ReadTimeout),
				WriteTimeoutSecs: durSecs(cfg.SMTP.Inbound.WriteTimeout),
			},
			Submission: submissionSMTPSectionDTO{
				Enabled:        cfg.SMTP.Submission.Enabled,
				Port:           cfg.SMTP.Submission.Port,
				Bind:           cfg.SMTP.Submission.Bind,
				RequireAuth:    cfg.SMTP.Submission.RequireAuth,
				RequireTLS:     cfg.SMTP.Submission.RequireTLS,
				MaxConnections: cfg.SMTP.Submission.MaxConnections,
			},
			SubmissionTLS: submissionTLSSectionDTO{
				Enabled:        cfg.SMTP.SubmissionTLS.Enabled,
				Port:           cfg.SMTP.SubmissionTLS.Port,
				Bind:           cfg.SMTP.SubmissionTLS.Bind,
				RequireAuth:    cfg.SMTP.SubmissionTLS.RequireAuth,
				MaxConnections: cfg.SMTP.SubmissionTLS.MaxConnections,
			},
		},
		IMAP: imapSectionDTO{
			Enabled:         cfg.IMAP.Enabled,
			Port:            cfg.IMAP.Port,
			Bind:            cfg.IMAP.Bind,
			STARTTLSPort:    cfg.IMAP.STARTTLSPort,
			IdleTimeoutSecs: durSecs(cfg.IMAP.IdleTimeout),
			MaxConnections:  cfg.IMAP.MaxConnections,
		},
		POP3: pop3SectionDTO{
			Enabled:        cfg.POP3.Enabled,
			Port:           cfg.POP3.Port,
			Bind:           cfg.POP3.Bind,
			MaxConnections: cfg.POP3.MaxConnections,
		},
		HTTP: httpSectionDTO{
			Enabled:        cfg.HTTP.Enabled,
			Port:           cfg.HTTP.Port,
			HTTPPort:       cfg.HTTP.HTTPPort,
			Bind:           cfg.HTTP.Bind,
			CorsOrigins:    cfg.HTTP.CorsOrigins,
			TrustedProxies: cfg.HTTP.TrustedProxies,
		},
		Admin: adminSectionDTO{
			Enabled: cfg.Admin.Enabled,
			Port:    cfg.Admin.Port,
			Bind:    cfg.Admin.Bind,
		},
		Spam: spamSectionDTO{
			Enabled:             cfg.Spam.Enabled,
			RejectThreshold:     cfg.Spam.RejectThreshold,
			JunkThreshold:       cfg.Spam.JunkThreshold,
			QuarantineThreshold: cfg.Spam.QuarantineThreshold,
			BayesianEnabled:     cfg.Spam.Bayesian.Enabled,
			BayesianAutoTrain:   cfg.Spam.Bayesian.AutoTrain,
			GreylistingEnabled:  cfg.Spam.Greylisting.Enabled,
			GreylistDelaySecs:   durSecs(cfg.Spam.Greylisting.Delay),
			RBLServers:          cfg.Spam.RBLServers,
		},
		AV: avSectionDTO{
			Enabled:     cfg.AV.Enabled,
			Addr:        cfg.AV.Addr,
			TimeoutSecs: durSecs(cfg.AV.Timeout),
			Action:      cfg.AV.Action,
		},
		Security: securitySectionDTO{
			MaxLoginAttempts: cfg.Security.MaxLoginAttempts,
			LockoutSecs:      durSecs(cfg.Security.LockoutDuration),
			DisableLegacyJWT: cfg.Security.DisableLegacyJWT,
			SPFCacheTTLSecs:  durSecs(cfg.Security.SPFCacheTTL),
			RateLimit:        rateLimitToDTO(cfg.Security.RateLimit),
			AuditLog: auditLogSectionDTO{
				Path:       cfg.Security.AuditLog.Path,
				MaxSizeMB:  cfg.Security.AuditLog.MaxSizeMB,
				MaxBackups: cfg.Security.AuditLog.MaxBackups,
				MaxAgeDays: cfg.Security.AuditLog.MaxAgeDays,
			},
		},
		LDAP: ldapSectionDTO{
			Enabled:        cfg.LDAP.Enabled,
			URL:            cfg.LDAP.URL,
			BindDN:         cfg.LDAP.BindDN,
			BaseDN:         cfg.LDAP.BaseDN,
			UserFilter:     cfg.LDAP.UserFilter,
			EmailAttribute: cfg.LDAP.EmailAttribute,
			NameAttribute:  cfg.LDAP.NameAttribute,
			GroupAttribute: cfg.LDAP.GroupAttribute,
			AdminGroups:    cfg.LDAP.AdminGroups,
			StartTLS:       cfg.LDAP.StartTLS,
			SkipVerify:     cfg.LDAP.SkipVerify,
			RootCA:         cfg.LDAP.RootCA,
			TimeoutSecs:    goDurSecs(cfg.LDAP.Timeout),
		},
		MCP: mcpSectionDTO{
			Enabled: cfg.MCP.Enabled,
			Port:    cfg.MCP.Port,
			Bind:    cfg.MCP.Bind,
		},
		ManageSieve: serviceSectionDTO{
			Enabled: cfg.ManageSieve.Enabled,
			Port:    cfg.ManageSieve.Port,
			Bind:    cfg.ManageSieve.Bind,
		},
		Logging: loggingSectionDTO{
			Level:      cfg.Logging.Level,
			Format:     cfg.Logging.Format,
			Output:     cfg.Logging.Output,
			MaxSizeMB:  cfg.Logging.MaxSizeMB,
			MaxBackups: cfg.Logging.MaxBackups,
			MaxAgeDays: cfg.Logging.MaxAgeDays,
		},
		Metrics: metricsSectionDTO{
			Enabled: cfg.Metrics.Enabled,
			Port:    cfg.Metrics.Port,
			Bind:    cfg.Metrics.Bind,
			Path:    cfg.Metrics.Path,
		},
		Tracing: tracingSectionDTO{
			Enabled:      cfg.Tracing.Enabled,
			ServiceName:  cfg.Tracing.ServiceName,
			Exporter:     cfg.Tracing.Exporter,
			OTLPEndpoint: cfg.Tracing.OTLPEndpoint,
			Environment:  cfg.Tracing.Environment,
			SampleRate:   cfg.Tracing.SampleRate,
		},
		Database: databaseSectionDTO{Path: cfg.Database.Path, Backend: cfg.Database.Backend},
		Storage: storageSectionDTO{
			Sync:          cfg.Storage.Sync,
			SharedFolders: cfg.Storage.SharedFolders,
		},
		CalDAV: serviceSectionDTO{
			Enabled: cfg.CalDAV.Enabled,
			Port:    cfg.CalDAV.Port,
			Bind:    cfg.CalDAV.Bind,
		},
		CardDAV: serviceSectionDTO{
			Enabled: cfg.CardDAV.Enabled,
			Port:    cfg.CardDAV.Port,
			Bind:    cfg.CardDAV.Bind,
		},
		JMAP: jmapSectionDTO{
			Enabled:     cfg.JMAP.Enabled,
			Port:        cfg.JMAP.Port,
			Bind:        cfg.JMAP.Bind,
			CorsOrigins: cfg.JMAP.CorsOrigins,
		},
		DMARC: dmarcSectionDTO{
			Enabled:     cfg.DMARC.Enabled,
			OrgName:     cfg.DMARC.OrgName,
			FromEmail:   cfg.DMARC.FromEmail,
			ReportEmail: cfg.DMARC.ReportEmail,
			Interval:    cfg.DMARC.Interval,
		},
		Alert: alertSectionDTO{
			Enabled:         cfg.Alert.Enabled,
			WebhookURL:      cfg.Alert.WebhookURL,
			SMTPServer:      cfg.Alert.SMTPServer,
			SMTPPort:        cfg.Alert.SMTPPort,
			SMTPUsername:    cfg.Alert.SMTPUsername,
			FromAddress:     cfg.Alert.FromAddress,
			ToAddresses:     cfg.Alert.ToAddresses,
			UseTLS:          cfg.Alert.UseTLS,
			MinIntervalSecs: durSecs(cfg.Alert.MinInterval),
			MaxAlerts:       cfg.Alert.MaxAlerts,
			DiskThreshold:   cfg.Alert.DiskThreshold,
			MemoryThreshold: cfg.Alert.MemoryThreshold,
			ErrorThreshold:  cfg.Alert.ErrorThreshold,
			TLSWarningDays:  cfg.Alert.TLSWarningDays,
			QueueThreshold:  cfg.Alert.QueueThreshold,
			AllowPrivateIP:  cfg.Alert.AllowPrivateIP,
		},
		Push: pushSectionDTO{
			Enabled:        cfg.Push.Enabled,
			Subject:        cfg.Push.Subject,
			VAPIDPublicKey: cfg.Push.VAPIDPublicKey,
		},
		Signing: signingSectionDTO{
			Enabled: cfg.Signing.Enabled,
			KeyDir:  cfg.Signing.KeyDir,
		},
		OOF: oofSectionDTO{
			DefaultEnabled: cfg.OOF.DefaultEnabled,
			InternalOnly:   cfg.OOF.InternalOnly,
			DefaultSubject: cfg.OOF.DefaultSubject,
			DefaultMessage: cfg.OOF.DefaultMessage,
		},
		Notifications: notificationsSectionDTO{
			QueueAlerts:    cfg.Notifications.QueueAlerts,
			SecurityAlerts: cfg.Notifications.SecurityAlerts,
			WeeklyReports:  cfg.Notifications.WeeklyReports,
		},
		ScheduledSend: scheduledSendSectionDTO{
			Enabled:        cfg.ScheduledSend.Enabled,
			MaxHorizonDays: cfg.ScheduledSend.MaxHorizonDays,
			TickSeconds:    cfg.ScheduledSend.TickSeconds,
			MaxPerUser:     cfg.ScheduledSend.MaxPerUser,
		},
	}
}

func rateLimitToDTO(rl config.RateLimitConfig) rateLimitSectionDTO {
	return rateLimitSectionDTO{
		IPPerMinute:           rl.IPPerMinute,
		IPPerHour:             rl.IPPerHour,
		IPPerDay:              rl.IPPerDay,
		IPConnections:         rl.IPConnections,
		UserPerMinute:         rl.UserPerMinute,
		UserPerHour:           rl.UserPerHour,
		UserPerDay:            rl.UserPerDay,
		UserMaxRecipients:     rl.UserMaxRecipients,
		GlobalPerMinute:       rl.GlobalPerMinute,
		GlobalPerHour:         rl.GlobalPerHour,
		DomainPerMinute:       rl.DomainPerMinute,
		DomainPerHour:         rl.DomainPerHour,
		DomainPerDay:          rl.DomainPerDay,
		SMTPPerMinute:         rl.SMTPPerMinute,
		SMTPPerHour:           rl.SMTPPerHour,
		IMAPConnections:       rl.IMAPConnections,
		HTTPRequestsPerMinute: rl.HTTPRequestsPerMinute,
	}
}

// applyConfigDTO writes the secrets-free DTO fields onto cfg. cfg is a clone of
// the live config, so fields the DTO does not carry (every secret) keep their
// current values. Classification of what took effect live versus what needs a
// restart is the running server's job (see ReloadConfig), not this writer's.
func applyConfigDTO(cfg *config.Config, req *serverConfigDTO) {
	cfg.Server.Hostname = req.Server.Hostname
	cfg.Server.DataDir = req.Server.DataDir
	cfg.Server.GracefulTimeout = req.Server.GracefulTimeoutSecs
	cfg.Server.ForceCloseAfter = req.Server.ForceCloseAfterSecs

	cfg.TLS.ACME.Enabled = req.TLS.ACME.Enabled
	cfg.TLS.ACME.Email = req.TLS.ACME.Email
	cfg.TLS.ACME.Provider = req.TLS.ACME.Provider
	cfg.TLS.ACME.Challenge = req.TLS.ACME.Challenge
	cfg.TLS.ACME.DNSProvider = req.TLS.ACME.DNSProvider
	cfg.TLS.CertFile = req.TLS.CertFile
	cfg.TLS.KeyFile = req.TLS.KeyFile
	cfg.TLS.MinVersion = req.TLS.MinVersion
	cfg.TLS.ClientAuth.Enabled = req.TLS.ClientAuth.Enabled
	cfg.TLS.ClientAuth.RequireCert = req.TLS.ClientAuth.RequireCert
	cfg.TLS.ClientAuth.CAFile = req.TLS.ClientAuth.CAFile
	cfg.TLS.ClientAuth.VerifyMode = req.TLS.ClientAuth.VerifyMode

	cfg.SMTP.Inbound.Enabled = req.SMTP.Inbound.Enabled
	cfg.SMTP.Inbound.Port = req.SMTP.Inbound.Port
	cfg.SMTP.Inbound.Bind = req.SMTP.Inbound.Bind
	cfg.SMTP.Inbound.MaxMessageSize = mbToSize(req.SMTP.Inbound.MaxMessageSizeMB)
	cfg.SMTP.Inbound.MaxRecipients = req.SMTP.Inbound.MaxRecipients
	cfg.SMTP.Inbound.MaxConnections = req.SMTP.Inbound.MaxConnections
	cfg.SMTP.Inbound.ReadTimeout = secsToDur(req.SMTP.Inbound.ReadTimeoutSecs)
	cfg.SMTP.Inbound.WriteTimeout = secsToDur(req.SMTP.Inbound.WriteTimeoutSecs)
	cfg.SMTP.Submission.Enabled = req.SMTP.Submission.Enabled
	cfg.SMTP.Submission.Port = req.SMTP.Submission.Port
	cfg.SMTP.Submission.Bind = req.SMTP.Submission.Bind
	cfg.SMTP.Submission.RequireAuth = req.SMTP.Submission.RequireAuth
	cfg.SMTP.Submission.RequireTLS = req.SMTP.Submission.RequireTLS
	cfg.SMTP.Submission.MaxConnections = req.SMTP.Submission.MaxConnections
	cfg.SMTP.SubmissionTLS.Enabled = req.SMTP.SubmissionTLS.Enabled
	cfg.SMTP.SubmissionTLS.Port = req.SMTP.SubmissionTLS.Port
	cfg.SMTP.SubmissionTLS.Bind = req.SMTP.SubmissionTLS.Bind
	cfg.SMTP.SubmissionTLS.RequireAuth = req.SMTP.SubmissionTLS.RequireAuth
	cfg.SMTP.SubmissionTLS.MaxConnections = req.SMTP.SubmissionTLS.MaxConnections

	cfg.IMAP.Enabled = req.IMAP.Enabled
	cfg.IMAP.Port = req.IMAP.Port
	cfg.IMAP.Bind = req.IMAP.Bind
	cfg.IMAP.STARTTLSPort = req.IMAP.STARTTLSPort
	cfg.IMAP.IdleTimeout = secsToDur(req.IMAP.IdleTimeoutSecs)
	cfg.IMAP.MaxConnections = req.IMAP.MaxConnections

	cfg.POP3.Enabled = req.POP3.Enabled
	cfg.POP3.Port = req.POP3.Port
	cfg.POP3.Bind = req.POP3.Bind
	cfg.POP3.MaxConnections = req.POP3.MaxConnections

	cfg.HTTP.Enabled = req.HTTP.Enabled
	cfg.HTTP.Port = req.HTTP.Port
	cfg.HTTP.HTTPPort = req.HTTP.HTTPPort
	cfg.HTTP.Bind = req.HTTP.Bind
	cfg.HTTP.CorsOrigins = req.HTTP.CorsOrigins
	cfg.HTTP.TrustedProxies = req.HTTP.TrustedProxies

	cfg.Admin.Enabled = req.Admin.Enabled
	cfg.Admin.Port = req.Admin.Port
	cfg.Admin.Bind = req.Admin.Bind

	cfg.Spam.Enabled = req.Spam.Enabled
	cfg.Spam.RejectThreshold = req.Spam.RejectThreshold
	cfg.Spam.JunkThreshold = req.Spam.JunkThreshold
	cfg.Spam.QuarantineThreshold = req.Spam.QuarantineThreshold
	cfg.Spam.Bayesian.Enabled = req.Spam.BayesianEnabled
	cfg.Spam.Bayesian.AutoTrain = req.Spam.BayesianAutoTrain
	cfg.Spam.Greylisting.Enabled = req.Spam.GreylistingEnabled
	cfg.Spam.Greylisting.Delay = secsToDur(req.Spam.GreylistDelaySecs)
	cfg.Spam.RBLServers = req.Spam.RBLServers

	cfg.AV.Enabled = req.AV.Enabled
	cfg.AV.Addr = req.AV.Addr
	cfg.AV.Timeout = secsToDur(req.AV.TimeoutSecs)
	cfg.AV.Action = req.AV.Action

	cfg.Security.MaxLoginAttempts = req.Security.MaxLoginAttempts
	cfg.Security.LockoutDuration = secsToDur(req.Security.LockoutSecs)
	cfg.Security.DisableLegacyJWT = req.Security.DisableLegacyJWT
	cfg.Security.SPFCacheTTL = secsToDur(req.Security.SPFCacheTTLSecs)
	applyRateLimitDTO(&cfg.Security.RateLimit, &req.Security.RateLimit)
	cfg.Security.AuditLog.Path = req.Security.AuditLog.Path
	cfg.Security.AuditLog.MaxSizeMB = req.Security.AuditLog.MaxSizeMB
	cfg.Security.AuditLog.MaxBackups = req.Security.AuditLog.MaxBackups
	cfg.Security.AuditLog.MaxAgeDays = req.Security.AuditLog.MaxAgeDays

	cfg.LDAP.Enabled = req.LDAP.Enabled
	cfg.LDAP.URL = req.LDAP.URL
	cfg.LDAP.BindDN = req.LDAP.BindDN
	cfg.LDAP.BaseDN = req.LDAP.BaseDN
	cfg.LDAP.UserFilter = req.LDAP.UserFilter
	cfg.LDAP.EmailAttribute = req.LDAP.EmailAttribute
	cfg.LDAP.NameAttribute = req.LDAP.NameAttribute
	cfg.LDAP.GroupAttribute = req.LDAP.GroupAttribute
	cfg.LDAP.AdminGroups = req.LDAP.AdminGroups
	cfg.LDAP.StartTLS = req.LDAP.StartTLS
	cfg.LDAP.SkipVerify = req.LDAP.SkipVerify
	cfg.LDAP.RootCA = req.LDAP.RootCA
	cfg.LDAP.Timeout = secsToGoDur(req.LDAP.TimeoutSecs)

	cfg.MCP.Enabled = req.MCP.Enabled
	cfg.MCP.Port = req.MCP.Port
	cfg.MCP.Bind = req.MCP.Bind

	cfg.ManageSieve.Enabled = req.ManageSieve.Enabled
	cfg.ManageSieve.Port = req.ManageSieve.Port
	cfg.ManageSieve.Bind = req.ManageSieve.Bind

	cfg.Logging.Level = req.Logging.Level
	cfg.Logging.Format = req.Logging.Format
	cfg.Logging.Output = req.Logging.Output
	cfg.Logging.MaxSizeMB = req.Logging.MaxSizeMB
	cfg.Logging.MaxBackups = req.Logging.MaxBackups
	cfg.Logging.MaxAgeDays = req.Logging.MaxAgeDays

	cfg.Metrics.Enabled = req.Metrics.Enabled
	cfg.Metrics.Port = req.Metrics.Port
	cfg.Metrics.Bind = req.Metrics.Bind
	cfg.Metrics.Path = req.Metrics.Path

	cfg.Tracing.Enabled = req.Tracing.Enabled
	cfg.Tracing.ServiceName = req.Tracing.ServiceName
	cfg.Tracing.Exporter = req.Tracing.Exporter
	cfg.Tracing.OTLPEndpoint = req.Tracing.OTLPEndpoint
	cfg.Tracing.Environment = req.Tracing.Environment
	cfg.Tracing.SampleRate = req.Tracing.SampleRate

	cfg.Database.Path = req.Database.Path
	cfg.Database.Backend = req.Database.Backend

	cfg.Storage.Sync = req.Storage.Sync
	cfg.Storage.SharedFolders = req.Storage.SharedFolders

	cfg.CalDAV.Enabled = req.CalDAV.Enabled
	cfg.CalDAV.Port = req.CalDAV.Port
	cfg.CalDAV.Bind = req.CalDAV.Bind

	cfg.CardDAV.Enabled = req.CardDAV.Enabled
	cfg.CardDAV.Port = req.CardDAV.Port
	cfg.CardDAV.Bind = req.CardDAV.Bind

	cfg.JMAP.Enabled = req.JMAP.Enabled
	cfg.JMAP.Port = req.JMAP.Port
	cfg.JMAP.Bind = req.JMAP.Bind
	cfg.JMAP.CorsOrigins = req.JMAP.CorsOrigins

	cfg.DMARC.Enabled = req.DMARC.Enabled
	cfg.DMARC.OrgName = req.DMARC.OrgName
	cfg.DMARC.FromEmail = req.DMARC.FromEmail
	cfg.DMARC.ReportEmail = req.DMARC.ReportEmail
	cfg.DMARC.Interval = req.DMARC.Interval

	cfg.Alert.Enabled = req.Alert.Enabled
	cfg.Alert.WebhookURL = req.Alert.WebhookURL
	cfg.Alert.SMTPServer = req.Alert.SMTPServer
	cfg.Alert.SMTPPort = req.Alert.SMTPPort
	cfg.Alert.SMTPUsername = req.Alert.SMTPUsername
	cfg.Alert.FromAddress = req.Alert.FromAddress
	cfg.Alert.ToAddresses = req.Alert.ToAddresses
	cfg.Alert.UseTLS = req.Alert.UseTLS
	cfg.Alert.MinInterval = secsToDur(req.Alert.MinIntervalSecs)
	cfg.Alert.MaxAlerts = req.Alert.MaxAlerts
	cfg.Alert.DiskThreshold = req.Alert.DiskThreshold
	cfg.Alert.MemoryThreshold = req.Alert.MemoryThreshold
	cfg.Alert.ErrorThreshold = req.Alert.ErrorThreshold
	cfg.Alert.TLSWarningDays = req.Alert.TLSWarningDays
	cfg.Alert.QueueThreshold = req.Alert.QueueThreshold
	cfg.Alert.AllowPrivateIP = req.Alert.AllowPrivateIP

	cfg.Push.Enabled = req.Push.Enabled
	cfg.Push.Subject = req.Push.Subject
	cfg.Push.VAPIDPublicKey = req.Push.VAPIDPublicKey

	cfg.Signing.Enabled = req.Signing.Enabled
	cfg.Signing.KeyDir = req.Signing.KeyDir

	cfg.OOF.DefaultEnabled = req.OOF.DefaultEnabled
	cfg.OOF.InternalOnly = req.OOF.InternalOnly
	cfg.OOF.DefaultSubject = req.OOF.DefaultSubject
	cfg.OOF.DefaultMessage = req.OOF.DefaultMessage

	cfg.Notifications.QueueAlerts = req.Notifications.QueueAlerts
	cfg.Notifications.SecurityAlerts = req.Notifications.SecurityAlerts
	cfg.Notifications.WeeklyReports = req.Notifications.WeeklyReports

	cfg.ScheduledSend.Enabled = req.ScheduledSend.Enabled
	cfg.ScheduledSend.MaxHorizonDays = req.ScheduledSend.MaxHorizonDays
	cfg.ScheduledSend.TickSeconds = req.ScheduledSend.TickSeconds
	cfg.ScheduledSend.MaxPerUser = req.ScheduledSend.MaxPerUser
}

func applyRateLimitDTO(rl *config.RateLimitConfig, req *rateLimitSectionDTO) {
	rl.IPPerMinute = req.IPPerMinute
	rl.IPPerHour = req.IPPerHour
	rl.IPPerDay = req.IPPerDay
	rl.IPConnections = req.IPConnections
	rl.UserPerMinute = req.UserPerMinute
	rl.UserPerHour = req.UserPerHour
	rl.UserPerDay = req.UserPerDay
	rl.UserMaxRecipients = req.UserMaxRecipients
	rl.GlobalPerMinute = req.GlobalPerMinute
	rl.GlobalPerHour = req.GlobalPerHour
	rl.DomainPerMinute = req.DomainPerMinute
	rl.DomainPerHour = req.DomainPerHour
	rl.DomainPerDay = req.DomainPerDay
	rl.SMTPPerMinute = req.SMTPPerMinute
	rl.SMTPPerHour = req.SMTPPerHour
	rl.IMAPConnections = req.IMAPConnections
	rl.HTTPRequestsPerMinute = req.HTTPRequestsPerMinute
}

// changedSections compares two configs section by section and returns the YAML
// section names that differ, omitting any already listed in applied. It backs
// the no-reloader fallback's restart-required reporting.
func changedSections(before, after *config.Config, applied []string) []string {
	appliedSet := make(map[string]struct{}, len(applied))
	for _, a := range applied {
		appliedSet[a] = struct{}{}
	}
	var changed []string
	for _, sec := range []struct {
		name       string
		oldV, newV any
	}{
		{"server", before.Server, after.Server},
		{"tls", before.TLS, after.TLS},
		{"smtp", before.SMTP, after.SMTP},
		{"imap", before.IMAP, after.IMAP},
		{"pop3", before.POP3, after.POP3},
		{"http", before.HTTP, after.HTTP},
		{"admin", before.Admin, after.Admin},
		{"spam", before.Spam, after.Spam},
		{"av", before.AV, after.AV},
		{"security", before.Security, after.Security},
		{"ldap", before.LDAP, after.LDAP},
		{"mcp", before.MCP, after.MCP},
		{"managesieve", before.ManageSieve, after.ManageSieve},
		{"logging", before.Logging, after.Logging},
		{"metrics", before.Metrics, after.Metrics},
		{"tracing", before.Tracing, after.Tracing},
		{"database", before.Database, after.Database},
		{"storage", before.Storage, after.Storage},
		{"caldav", before.CalDAV, after.CalDAV},
		{"carddav", before.CardDAV, after.CardDAV},
		{"jmap", before.JMAP, after.JMAP},
		{"dmarc", before.DMARC, after.DMARC},
		{"alert", before.Alert, after.Alert},
		{"push", before.Push, after.Push},
		{"signing", before.Signing, after.Signing},
		{"oof", before.OOF, after.OOF},
		{"notifications", before.Notifications, after.Notifications},
		{"scheduled_send", before.ScheduledSend, after.ScheduledSend},
	} {
		if _, skip := appliedSet[sec.name]; skip {
			continue
		}
		if !reflect.DeepEqual(sec.oldV, sec.newV) {
			changed = append(changed, sec.name)
		}
	}
	return changed
}

// validateConfigDTO bounds-checks incoming values before any write. The cloned
// config's own Validate() is the final guard after the fields are applied.
func validateConfigDTO(req *serverConfigDTO) (string, bool) {
	if req.Server.Hostname == "" {
		return "hostname is required", false
	}
	if req.Server.DataDir == "" {
		return "data_dir is required", false
	}
	// A service's listen port only has to be valid when that service is enabled;
	// a disabled service may legitimately leave its port at 0.
	enabledPorts := []struct {
		enabled bool
		port    int
	}{
		{req.SMTP.Inbound.Enabled, req.SMTP.Inbound.Port},
		{req.SMTP.Submission.Enabled, req.SMTP.Submission.Port},
		{req.SMTP.SubmissionTLS.Enabled, req.SMTP.SubmissionTLS.Port},
		{req.IMAP.Enabled, req.IMAP.Port},
		{req.POP3.Enabled, req.POP3.Port},
		{req.HTTP.Enabled, req.HTTP.Port},
		{req.Admin.Enabled, req.Admin.Port},
		{req.MCP.Enabled, req.MCP.Port},
		{req.ManageSieve.Enabled, req.ManageSieve.Port},
		{req.Metrics.Enabled, req.Metrics.Port},
		{req.CalDAV.Enabled, req.CalDAV.Port},
		{req.CardDAV.Enabled, req.CardDAV.Port},
		{req.JMAP.Enabled, req.JMAP.Port},
	}
	for _, ep := range enabledPorts {
		if ep.enabled && (ep.port < 1 || ep.port > 65535) {
			return "an enabled service's port must be between 1 and 65535", false
		}
	}
	// IMAP STARTTLS and the plain HTTP redirect port are optional (0 = disabled).
	for _, p := range []int{req.IMAP.STARTTLSPort, req.HTTP.HTTPPort} {
		if p < 0 || p > 65535 {
			return "optional ports must be between 0 and 65535", false
		}
	}
	if req.SMTP.Inbound.MaxMessageSizeMB < 1 || req.SMTP.Inbound.MaxMessageSizeMB > 1024 {
		return "smtp.inbound.max_message_size_mb must be between 1 and 1024", false
	}
	if req.SMTP.Inbound.MaxRecipients < 1 || req.SMTP.Inbound.MaxRecipients > 10000 {
		return "smtp.inbound.max_recipients must be between 1 and 10000", false
	}
	if req.Security.MaxLoginAttempts < 1 || req.Security.MaxLoginAttempts > 1000 {
		return "security.max_login_attempts must be between 1 and 1000", false
	}
	if req.Security.RateLimit.UserPerHour < 1 || req.Security.RateLimit.UserPerHour > 1000000 {
		return "security.rate_limit.user_per_hour must be between 1 and 1000000", false
	}
	if req.Tracing.SampleRate < 0 || req.Tracing.SampleRate > 1 {
		return "tracing.sample_rate must be between 0.0 and 1.0", false
	}
	if len(req.OOF.DefaultSubject) > 255 {
		return "oof.default_subject exceeds maximum length of 255", false
	}
	if len(req.OOF.DefaultMessage) > 5000 {
		return "oof.default_message exceeds maximum length of 5000", false
	}
	// Bounded unconditionally (even when disabled) so a full-config PUT that omits
	// the section is rejected rather than silently zeroing the live values.
	if req.ScheduledSend.MaxHorizonDays < 1 || req.ScheduledSend.MaxHorizonDays > 3650 {
		return "scheduled_send.max_horizon_days must be between 1 and 3650", false
	}
	if req.ScheduledSend.TickSeconds < 5 || req.ScheduledSend.TickSeconds > 3600 {
		return "scheduled_send.tick_seconds must be between 5 and 3600", false
	}
	if req.ScheduledSend.MaxPerUser < 1 || req.ScheduledSend.MaxPerUser > 10000 {
		return "scheduled_send.max_per_user must be between 1 and 10000", false
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
