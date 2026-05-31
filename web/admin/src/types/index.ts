export interface User {
  email: string;
  isAdmin: boolean;
}

export interface Domain {
  name: string;
  max_accounts: number;
  is_active: boolean;
  created_at: string;
  updated_at: string;
  dkim_selector?: string;
  dkim_public_key?: string;
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

export interface ServerConfig {
  hostname: string;
  data_dir: string;
  smtp_port: number;
  submission_port: number;
  imap_port: number;
  max_message_size_mb: number;
  max_recipients: number;
  max_emails_per_hour: number;
  greylisting_enabled: boolean;
  auto_tls: boolean;
  require_tls_smtp: boolean;
  dkim_signing: boolean;
  max_login_attempts: number;
}
