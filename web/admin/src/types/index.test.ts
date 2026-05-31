import { describe, it, expect } from 'vitest'
import type {
  Domain,
  Account,
  QueueEntry,
  ServerStats,
  HealthStatus,
  ServiceStatus,
  RealtimeMetrics,
  Activity,
  DelegationEntry,
  DirectoryObject,
  BookingPolicy,
  RoomList,
  PolicyRule,
  RateLimitConfig,
  MailboxDiagnostics,
  SubscriptionInfo,
  ProtocolFailure,
} from './index'

describe('Domain type', () => {
  it('accepts valid domain structure', () => {
    const domain: Domain = {
      name: 'example.com',
      max_accounts: 100,
      is_active: true,
      created_at: '2024-01-01T00:00:00Z',
      updated_at: '2024-01-01T00:00:00Z',
    }
    expect(domain.name).toBe('example.com')
    expect(domain.max_accounts).toBe(100)
    expect(domain.is_active).toBe(true)
  })

  it('accepts domain with optional DKIM fields', () => {
    const domain: Domain = {
      name: 'example.com',
      max_accounts: 100,
      is_active: true,
      created_at: '2024-01-01T00:00:00Z',
      updated_at: '2024-01-01T00:00:00Z',
      dkim_selector: 'mail',
      dkim_public_key: 'MFkwDQYJKoZIhvcNAQEBBQADSAAwR...',
    }
    expect(domain.dkim_selector).toBe('mail')
    expect(domain.dkim_public_key).toBeDefined()
  })
})

describe('Account type', () => {
  it('accepts valid account structure', () => {
    const account: Account = {
      email: 'user@example.com',
      is_admin: false,
      is_active: true,
      quota_used: 1024,
      quota_limit: 10240,
      created_at: '2024-01-01T00:00:00Z',
      updated_at: '2024-01-01T00:00:00Z',
    }
    expect(account.email).toBe('user@example.com')
    expect(account.quota_limit).toBe(10240)
  })

  it('accepts account with optional fields', () => {
    const account: Account = {
      email: 'user@example.com',
      is_admin: false,
      is_active: true,
      quota_used: 0,
      quota_limit: 10240,
      created_at: '2024-01-01T00:00:00Z',
      updated_at: '2024-01-01T00:00:00Z',
      forward_to: 'other@example.com',
      forward_keep_copy: true,
      last_login: '2024-06-01T12:00:00Z',
      totp_enabled: true,
    }
    expect(account.forward_to).toBe('other@example.com')
    expect(account.totp_enabled).toBe(true)
  })
})

describe('QueueEntry type', () => {
  it('accepts all valid statuses', () => {
    const statuses: QueueEntry['status'][] = ['pending', 'sending', 'failed', 'delivered']
    statuses.forEach((status) => {
      const entry: QueueEntry = {
        id: 'msg-123',
        from: 'sender@example.com',
        to: 'recipient@example.com',
        status,
        retry_count: 0,
        created_at: '2024-01-01T00:00:00Z',
      }
      expect(entry.status).toBe(status)
    })
  })

  it('accepts entry with retry fields', () => {
    const entry: QueueEntry = {
      id: 'msg-123',
      from: 'sender@example.com',
      to: 'recipient@example.com',
      status: 'failed',
      retry_count: 3,
      last_error: 'connection timeout',
      created_at: '2024-01-01T00:00:00Z',
      next_retry: '2024-01-01T01:00:00Z',
    }
    expect(entry.retry_count).toBe(3)
    expect(entry.last_error).toBe('connection timeout')
    expect(entry.next_retry).toBeDefined()
  })
})

describe('ServerStats type', () => {
  it('accepts valid stats structure', () => {
    const stats: ServerStats = {
      domains: 5,
      accounts: 50,
      messages: 10000,
      queue_size: 0,
    }
    expect(stats.domains).toBe(5)
    expect(stats.queue_size).toBe(0)
  })
})

describe('HealthStatus type', () => {
  it('accepts all valid statuses', () => {
    const statuses: HealthStatus['status'][] = ['healthy', 'unhealthy', 'warning']
    statuses.forEach((status) => {
      const health: HealthStatus = { status }
      expect(health.status).toBe(status)
    })
  })

  it('accepts health with component status', () => {
    const health: HealthStatus = {
      status: 'healthy',
      database: 'operational',
      queue: 'operational',
      storage: 'operational',
    }
    expect(health.database).toBe('operational')
  })
})

describe('ServiceStatus type', () => {
  it('accepts all valid service statuses', () => {
    const serviceStatuses: ServiceStatus['status'][] = ['operational', 'degraded', 'down']
    serviceStatuses.forEach((status) => {
      const service: ServiceStatus = { name: 'SMTP', status, port: 25 }
      expect(service.status).toBe(status)
    })
  })

  it('accepts service with optional latency', () => {
    const service: ServiceStatus = {
      name: 'IMAP',
      status: 'operational',
      port: 143,
      latency: 5,
    }
    expect(service.latency).toBe(5)
  })
})

describe('RealtimeMetrics type', () => {
  it('accepts valid metrics structure', () => {
    const metrics: RealtimeMetrics = {
      timestamp: Date.now(),
      cpu_usage: 45.5,
      memory_usage: 62.3,
      disk_usage: 55.0,
      network_in: 1024,
      network_out: 2048,
      smtp_connections: 10,
      imap_connections: 5,
      messages_sent: 100,
      messages_received: 200,
    }
    expect(metrics.cpu_usage).toBe(45.5)
    expect(metrics.smtp_connections).toBe(10)
  })
})

describe('Activity type', () => {
  it('accepts all valid activity types', () => {
    const activityTypes: Activity['type'][] = ['message', 'account', 'domain', 'queue', 'system']
    activityTypes.forEach((type) => {
      const activity: Activity = {
        id: 'act-1',
        type,
        message: 'Test activity',
        timestamp: '2024-01-01T00:00:00Z',
      }
      expect(activity.type).toBe(type)
    })
  })

  it('accepts activity with severity', () => {
    const severities: Activity['severity'][] = ['info', 'warning', 'error', 'success']
    severities.forEach((severity) => {
      const activity: Activity = {
        id: 'act-1',
        type: 'system',
        message: 'Test',
        timestamp: '2024-01-01T00:00:00Z',
        severity,
      }
      expect(activity.severity).toBe(severity)
    })
  })
})

describe('DelegationEntry type', () => {
  it('accepts a valid delegation grant', () => {
    const entry: DelegationEntry = {
      id: 'del-abc123',
      owner: 'owner@example.com',
      grantee: 'delegate@example.com',
      mailbox: 'owner@example.com',
      rights: 'read, write',
      canSendAs: false,
      canSendOnBehalf: true,
      createdAt: '2024-01-01T00:00:00Z',
    }
    expect(entry.owner).toBe('owner@example.com')
    expect(entry.grantee).toBe('delegate@example.com')
    expect(entry.canSendOnBehalf).toBe(true)
    expect(entry.canSendAs).toBe(false)
  })
})

describe('DirectoryObject type', () => {
  it('accepts room and equipment resources', () => {
    const room: DirectoryObject = {
      id: 'conf-a@example.com',
      name: 'Conference Room A',
      email: 'conf-a@example.com',
      type: 'room',
      isHidden: false,
      isBookable: true,
      capacity: 10,
    }
    const equip: DirectoryObject = {
      id: 'projector@example.com',
      name: 'Projector',
      email: 'projector@example.com',
      type: 'equipment',
      isHidden: false,
      isBookable: true,
    }
    expect(room.type).toBe('room')
    expect(room.capacity).toBe(10)
    expect(equip.type).toBe('equipment')
  })
})

describe('BookingPolicy type', () => {
  it('accepts a valid booking policy', () => {
    const policy: BookingPolicy = {
      id: 'conf-a@example.com',
      resourceName: 'Conference Room A',
      autoAccept: true,
      allowRecurring: true,
      maxDuration: 480,
      requiresApproval: false,
      approvalDelegate: '',
    }
    expect(policy.autoAccept).toBe(true)
    expect(policy.maxDuration).toBe(480)
  })
})

describe('RoomList type', () => {
  it('accepts a room list with member rooms', () => {
    const list: RoomList = {
      id: 'rl-1',
      name: 'Floor 1 Rooms',
      rooms: ['conf-a@example.com', 'conf-b@example.com'],
    }
    expect(list.rooms).toHaveLength(2)
    expect(list.name).toBe('Floor 1 Rooms')
  })
})

describe('PolicyRule type', () => {
  it('accepts a valid inbox rule', () => {
    const rule: PolicyRule = {
      id: 'rule-1',
      name: 'Move newsletters',
      enabled: true,
      priority: 1,
      conditions: 'Subject contains "newsletter"',
      actions: 'moveToFolder -> Newsletters',
      mailbox: 'user@example.com',
    }
    expect(rule.enabled).toBe(true)
    expect(rule.mailbox).toBe('user@example.com')
  })
})

describe('RateLimitConfig type', () => {
  it('accepts a valid flat rate-limit config', () => {
    const cfg: RateLimitConfig = {
      ip_per_minute: 60,
      ip_per_hour: 1000,
      ip_per_day: 10000,
      ip_connections: 10,
      user_per_minute: 30,
      user_per_hour: 500,
      user_per_day: 5000,
      user_max_recipients: 100,
      global_per_minute: 600,
      global_per_hour: 10000,
    }
    expect(cfg.user_per_hour).toBe(500)
    expect(cfg.ip_connections).toBe(10)
  })
})

describe('MailboxDiagnostics type', () => {
  it('accepts all valid sync states', () => {
    const states: MailboxDiagnostics['syncState'][] = ['healthy', 'degraded', 'error']
    states.forEach((syncState) => {
      const d: MailboxDiagnostics = {
        email: 'user@example.com',
        syncState,
        lastSync: '',
        subscriptionBacklog: 0,
        protocolFailures: 0,
        policyBlocks: 0,
        oofActive: false,
        rulesCount: 2,
        totalFolders: 8,
        totalItems: 67,
      }
      expect(d.syncState).toBe(syncState)
    })
  })
})

describe('SubscriptionInfo type', () => {
  it('accepts a valid subscription', () => {
    const sub: SubscriptionInfo = {
      id: 'sub-1',
      mailbox: 'user@example.com',
      type: 'pull',
      status: 'active',
      watermark: '12345',
      createdAt: '2024-01-01T00:00:00Z',
      lastEvent: '2024-01-01T00:00:00Z',
    }
    expect(sub.status).toBe('active')
    expect(sub.watermark).toBe('12345')
  })
})

describe('ProtocolFailure type', () => {
  it('accepts a valid protocol failure', () => {
    const f: ProtocolFailure = {
      id: 'fail-1',
      mailbox: 'user@example.com',
      protocol: 'IMAP',
      error: 'Connection timeout',
      timestamp: '2024-01-01T00:00:00Z',
    }
    expect(f.protocol).toBe('IMAP')
  })
})
