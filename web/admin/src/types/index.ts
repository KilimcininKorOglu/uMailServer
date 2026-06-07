export interface User {
  email: string;
  isAdmin: boolean;
}

export interface Tenant {
  id: string;
  name: string;
  is_active: boolean;
  settings?: Record<string, string>;
  created_at: string;
  updated_at: string;
}

export interface TenantBranding {
  app_name: string;
  logo_url: string;
  primary_color: string;
  features: Record<string, boolean>;
}

export interface Domain {
  name: string;
  max_accounts: number;
  is_active: boolean;
  created_at: string;
  updated_at: string;
  dkim_selector?: string;
  dkim_public_key?: string;
  // Outbound From display-name templates (placeholders: {name} {title}
  // {department} {company} {email}). company_name feeds {company}.
  company_name?: string;
  from_template_internal?: string;
  from_template_external?: string;
}

export interface Alias {
  alias: string; // full alias address: name@domain
  target: string; // destination account: user@domain
  domain: string;
  is_active: boolean;
  created_at: string;
}

export interface MailGroup {
  email: string; // group address: name@domain
  description?: string;
  is_active: boolean;
  dynamic: boolean; // false = static member list, true = rule-based
  sender_policy: "internal" | "anyone";
  members: string[]; // static membership
  dynamic_domain?: string; // domain scanned for dynamic membership
  dynamic_admin_only?: boolean; // dynamic: filter by admin status
  dynamic_local_pattern?: string; // dynamic: glob match on local-part
  created_at?: string;
  updated_at?: string;
}

export interface MailGroupInput {
  email: string;
  description?: string;
  dynamic: boolean;
  sender_policy: "internal" | "anyone";
  members?: string[];
  dynamic_domain?: string;
  dynamic_admin_only?: boolean;
  dynamic_local_pattern?: string;
}

export interface Account {
  email: string;
  is_admin: boolean;
  is_active: boolean;
  must_change_password?: boolean;
  quota_used: number;
  quota_limit: number;
  forward_to?: string;
  forward_keep_copy?: boolean;
  created_at: string;
  updated_at: string;
  last_login?: string;
  vacation_settings?: string;
  totp_enabled?: boolean;
  display_name?: string;
  title?: string;
  department?: string;
  phone?: string;
}

// AccountProfile is the optional directory profile passed on create/update.
export interface AccountProfile {
  display_name?: string;
  title?: string;
  department?: string;
  phone?: string;
}

export interface QueueEntry {
  id: string;
  from: string;
  to: string;
  status: 'pending' | 'sending' | 'failed' | 'delivered';
  retry_count: number;
  last_error?: string;
  created_at: string;
  next_retry?: string;
}

export interface ServerStats {
  domains: number;
  accounts: number;
  messages: number;
  queue_size: number;
}

export interface HealthStatus {
  status: 'healthy' | 'unhealthy' | 'warning';
  database?: string;
  queue?: string;
  storage?: string;
}

export interface ServiceStatus {
  name: string;
  status: 'operational' | 'degraded' | 'down';
  port?: number;
  latency?: number;
}

export interface RealtimeMetrics {
  timestamp: number;
  cpu_usage: number;
  memory_usage: number;
  disk_usage: number;
  network_in: number;
  network_out: number;
  smtp_connections: number;
  imap_connections: number;
  messages_sent: number;
  messages_received: number;
}

export interface Activity {
  id: string;
  type: 'message' | 'account' | 'domain' | 'queue' | 'system';
  message: string;
  details?: string;
  timestamp: string;
  severity?: 'info' | 'warning' | 'error' | 'success';
}

export interface DelegationEntry {
  id: string;
  owner: string;
  grantee: string;
  mailbox: string;
  rights: string;
  canSendAs: boolean;
  canSendOnBehalf: boolean;
  createdAt: string;
}

export interface DirectoryObject {
  id: string;
  name: string;
  email: string;
  type: 'room' | 'equipment';
  isHidden: boolean;
  isBookable: boolean;
  capacity?: number;
}

export interface BookingPolicy {
  id: string;
  resourceName: string;
  autoAccept: boolean;
  allowRecurring: boolean;
  maxDuration: number;
  requiresApproval: boolean;
  approvalDelegate: string;
}

export interface RoomList {
  id: string;
  name: string;
  rooms: string[];
}

export interface PolicyRule {
  id: string;
  name: string;
  enabled: boolean;
  priority: number;
  conditions: string;
  actions: string;
  mailbox: string;
}

export interface RateLimitConfig {
  ip_per_minute: number;
  ip_per_hour: number;
  ip_per_day: number;
  ip_connections: number;
  user_per_minute: number;
  user_per_hour: number;
  user_per_day: number;
  user_max_recipients: number;
  global_per_minute: number;
  global_per_hour: number;
}

export interface MailboxDiagnostics {
  email: string;
  syncState: 'healthy' | 'degraded' | 'error';
  lastSync: string;
  subscriptionBacklog: number;
  protocolFailures: number;
  policyBlocks: number;
  oofActive: boolean;
  rulesCount: number;
  totalFolders: number;
  totalItems: number;
}

export interface SubscriptionInfo {
  id: string;
  mailbox: string;
  type: string;
  status: 'active' | 'expiring' | 'expired';
  watermark: string;
  createdAt: string;
  lastEvent: string;
}

export interface ProtocolFailure {
  id: string;
  mailbox: string;
  protocol: string;
  error: string;
  timestamp: string;
}

export interface Job {
  id: string;
  type: string;
  status: 'pending' | 'running' | 'completed' | 'failed';
  progress: number;
  mailbox?: string;
  startedAt?: string;
  completedAt?: string;
  error?: string;
}

// BackupManifest mirrors storage.BackupManifest (GET /api/v1/backups).
export interface BackupManifest {
  id: string;
  filename: string;
  size: number;
  created_at: string;
  type: string; // "full" | "per-user" | "per-mailbox"
  target: string;
  checksum?: string;
  encrypted?: boolean;
  path?: string;
}

// ClusterInstance mirrors internal/cluster.InstanceHealth (GET /api/v1/cluster/status).
export interface ClusterInstance {
  instance_id: string;
  last_heartbeat: string;
  is_leader: boolean;
  connections: number;
  status: 'healthy' | 'degraded' | 'offline';
}

export interface ClusterStatus {
  enabled: boolean;
  status?: string; // "disabled" when the cluster manager is off
  instance_id?: string;
  is_leader?: boolean;
  instances: ClusterInstance[];
}

// ServerConfig mirrors the backend serverConfigDTO (internal/api/config_settings.go):
// a typed, per-section, secrets-free view of the server configuration. Secrets
// (JWT/TOTP keys, LDAP bind password, MCP auth tokens, alert SMTP password and
// webhook headers, VAPID private key) are intentionally absent. Durations are in
// whole seconds and message sizes in whole megabytes.
export interface AcmeConfig {
  enabled: boolean;
  email: string;
  provider: string;
  challenge: string;
  dns_provider: string;
}

export interface ClientAuthConfig {
  enabled: boolean;
  require_cert: boolean;
  ca_file: string;
  verify_mode: string;
}

export interface TLSConfig {
  acme: AcmeConfig;
  cert_file: string;
  key_file: string;
  min_version: string;
  client_auth: ClientAuthConfig;
}

export interface InboundSMTPConfig {
  enabled: boolean;
  port: number;
  bind: string;
  max_message_size_mb: number;
  max_recipients: number;
  max_connections: number;
  read_timeout_secs: number;
  write_timeout_secs: number;
}

export interface SubmissionSMTPConfig {
  enabled: boolean;
  port: number;
  bind: string;
  require_auth: boolean;
  require_tls: boolean;
  max_connections: number;
}

export interface SubmissionTLSConfig {
  enabled: boolean;
  port: number;
  bind: string;
  require_auth: boolean;
  max_connections: number;
}

export interface SMTPConfig {
  inbound: InboundSMTPConfig;
  submission: SubmissionSMTPConfig;
  submission_tls: SubmissionTLSConfig;
}

export interface IMAPConfig {
  enabled: boolean;
  port: number;
  bind: string;
  starttls_port: number;
  idle_timeout_secs: number;
  max_connections: number;
}

export interface POP3Config {
  enabled: boolean;
  port: number;
  bind: string;
  max_connections: number;
}

export interface HTTPConfig {
  enabled: boolean;
  port: number;
  http_port: number;
  bind: string;
  cors_origins: string[];
  trusted_proxies: string[];
}

export interface AdminConfig {
  enabled: boolean;
  port: number;
  bind: string;
}

// ServiceConfig is the shared enable/port/bind shape for ManageSieve, CalDAV,
// and CardDAV.
export interface ServiceConfig {
  enabled: boolean;
  port: number;
  bind: string;
}

export interface SpamConfig {
  enabled: boolean;
  reject_threshold: number;
  junk_threshold: number;
  quarantine_threshold: number;
  bayesian_enabled: boolean;
  bayesian_auto_train: boolean;
  greylisting_enabled: boolean;
  greylist_delay_secs: number;
  rbl_servers: string[];
}

export interface AVConfig {
  enabled: boolean;
  addr: string;
  timeout_secs: number;
  action: string;
}

// RateLimitSettings is the settings-DTO view of the rate limit (mirrors the
// backend rateLimitSectionDTO). It is distinct from RateLimitConfig above, which
// backs the live rate-limit admin endpoint and carries only the core counters.
export interface RateLimitSettings {
  ip_per_minute: number;
  ip_per_hour: number;
  ip_per_day: number;
  ip_connections: number;
  user_per_minute: number;
  user_per_hour: number;
  user_per_day: number;
  user_max_recipients: number;
  global_per_minute: number;
  global_per_hour: number;
  smtp_per_minute: number;
  smtp_per_hour: number;
  imap_connections: number;
  http_requests_per_minute: number;
}

export interface AuditLogConfig {
  path: string;
  max_size_mb: number;
  max_backups: number;
  max_age_days: number;
}

export interface SecurityConfig {
  max_login_attempts: number;
  lockout_secs: number;
  disable_legacy_jwt: boolean;
  spf_cache_ttl_secs: number;
  rate_limit: RateLimitSettings;
  audit_log: AuditLogConfig;
}

export interface LDAPConfig {
  enabled: boolean;
  url: string;
  bind_dn: string;
  base_dn: string;
  user_filter: string;
  email_attribute: string;
  name_attribute: string;
  group_attribute: string;
  admin_groups: string[];
  start_tls: boolean;
  skip_verify: boolean;
  root_ca: string;
  timeout_secs: number;
}

export interface MCPConfig {
  enabled: boolean;
  port: number;
  bind: string;
}

export interface LoggingConfig {
  level: string;
  format: string;
  output: string;
  max_size_mb: number;
  max_backups: number;
  max_age_days: number;
}

export interface MetricsConfig {
  enabled: boolean;
  port: number;
  bind: string;
  path: string;
}

export interface TracingConfig {
  enabled: boolean;
  service_name: string;
  exporter: string;
  otlp_endpoint: string;
  environment: string;
  sample_rate: number;
}

export interface DatabaseConfig {
  path: string;
}

export interface StorageConfig {
  sync: boolean;
  shared_folders: boolean;
}

export interface JMAPConfig {
  enabled: boolean;
  port: number;
  bind: string;
  cors_origins: string[];
}

export interface DMARCConfig {
  enabled: boolean;
  org_name: string;
  from_email: string;
  report_email: string;
  interval: string;
}

export interface AlertConfig {
  enabled: boolean;
  webhook_url: string;
  smtp_server: string;
  smtp_port: number;
  smtp_username: string;
  from_address: string;
  to_addresses: string[];
  use_tls: boolean;
  min_interval_secs: number;
  max_alerts: number;
  disk_threshold: number;
  memory_threshold: number;
  error_threshold: number;
  tls_warning_days: number;
  queue_threshold: number;
  allow_private_ip: boolean;
}

export interface PushConfig {
  enabled: boolean;
  subject: string;
  vapid_public_key: string;
}

export interface SigningConfig {
  enabled: boolean;
  key_dir: string;
}

export interface OOFConfig {
  default_enabled: boolean;
  internal_only: boolean;
  default_subject: string;
  default_message: string;
}

export interface NotificationsConfig {
  queue_alerts: boolean;
  security_alerts: boolean;
  weekly_reports: boolean;
}

export interface ServerSettings {
  hostname: string;
  data_dir: string;
  graceful_timeout_secs: number;
  force_close_after_secs: number;
}

export interface ServerConfig {
  server: ServerSettings;
  tls: TLSConfig;
  smtp: SMTPConfig;
  imap: IMAPConfig;
  pop3: POP3Config;
  http: HTTPConfig;
  admin: AdminConfig;
  spam: SpamConfig;
  av: AVConfig;
  security: SecurityConfig;
  ldap: LDAPConfig;
  mcp: MCPConfig;
  managesieve: ServiceConfig;
  logging: LoggingConfig;
  metrics: MetricsConfig;
  tracing: TracingConfig;
  database: DatabaseConfig;
  storage: StorageConfig;
  caldav: ServiceConfig;
  carddav: ServiceConfig;
  jmap: JMAPConfig;
  dmarc: DMARCConfig;
  alert: AlertConfig;
  push: PushConfig;
  signing: SigningConfig;
  oof: OOFConfig;
  notifications: NotificationsConfig;
}
