const API_URL = window.location.origin + '/api/v1'

// ============================================================================
// Type Definitions
// ============================================================================

export interface Mail {
  id: string
  from: string
  fromName: string
  to: string[]
  subject: string
  body: string
  preview: string
  date: string
  read: boolean
  starred: boolean
  folder: string
  hasAttachments: boolean
  size: number
}

export interface MailAttachment {
  filename: string
  contentType: string
  content: string // base64-encoded file bytes
}

export interface SendMailRequest {
  to: string[]
  cc?: string[]
  bcc?: string[]
  subject: string
  body: string
  from?: string // Sender identity for send-as or send-on-behalf
  attachments?: MailAttachment[]
  requestReadReceipt?: boolean // ask the recipient's client for a read receipt
}

export interface CalendarEvent {
  uid: string
  summary: string
  description?: string
  location?: string
  start: string // RFC3339, or YYYY-MM-DD when allDay
  end?: string
  allDay?: boolean
}

export type CalendarEventInput = Omit<CalendarEvent, "uid"> & { uid?: string }

export interface AuthLoginRequest {
  email: string
  password: string
}

export interface AuthLoginResponse {
  expiresIn?: number
}

// Filter mirrors the backend /api/v1/filters contract
// (internal/api/filters.go EmailFilter): camelCase JSON keys.
export interface Filter {
  id: string
  name: string
  enabled: boolean
  matchAll: boolean
  conditions: FilterCondition[]
  actions: FilterAction[]
  priority: number
}

// FilterField mirrors semcore.RuleConditionKind.
export type FilterField = 'from' | 'to' | 'subject' | 'body' | 'header' | 'size' | 'flag' | 'address'

// FilterOperator mirrors semcore.RuleMatchType.
export type FilterOperator = 'contains' | 'equals' | 'startsWith' | 'endsWith' | 'matches'

export interface FilterCondition {
  field: FilterField
  operator: FilterOperator
  value: string
  headerName?: string
}

// FilterActionType mirrors semcore.RuleActionKind.String(). The full vocabulary
// is represented so rules created in Outlook/admin round-trip without loss.
export type FilterActionType =
  | 'moveToFolder'
  | 'copyToFolder'
  | 'delete'
  | 'markRead'
  | 'markImportant'
  | 'forward'
  | 'forwardAsAttachment'
  | 'redirect'
  | 'reject'
  | 'addHeader'
  | 'deleteHeader'
  | 'flag'
  | 'stop'
  | 'vacation'

export interface FilterAction {
  type: FilterActionType
  target?: string
  forwardTo?: string
  message?: string
  headerName?: string
  headerValue?: string
  flagName?: string
  clearFlag?: boolean
}

// FilterInput is the create/update payload the backend accepts.
export interface FilterInput {
  name: string
  enabled?: boolean
  matchAll: boolean
  conditions: FilterCondition[]
  actions: FilterAction[]
}

// VacationAutoReply mirrors the backend /api/v1/vacation contract
// (internal/api/vacation.go VacationConfig): snake_case JSON keys, with
// `message` as the reply body and RFC3339 date strings.
export interface VacationAutoReply {
  enabled: boolean
  subject: string
  message: string
  html_message?: string
  start_date?: string
  end_date?: string
  send_interval?: number
  exclude_addresses?: string[]
  ignore_lists?: boolean
  ignore_bulk?: boolean
}

export interface ClientSession {
  id: string
  device_type: string
  client_ip: string
  created_at: string
  last_active: string
  user_agent: string
}

export interface PushSubscription {
  endpoint: string
  keys: {
    p256dh: string
    auth: string
  }
}

export interface SearchResponse {
  emails: Mail[]
  total: number
  query: string
}

export interface ThreadsResponse {
  threads: Thread[]
}

export interface Thread {
  id: string
  subject: string
  emails: Mail[]
  participants: string[]
  lastDate: string
  unread: boolean
}

// Shared mailbox and sender identity types
export interface SharedMailbox {
  owner: string
  mailbox: string
  displayName?: string
  rights?: string
}

export interface SenderIdentity {
  email: string
  displayName?: string
  type: 'personal' | 'send-as' | 'send-on-behalf'
  mailboxOwner?: string // for shared mailbox identities
  canSend: boolean
}

export interface DiagnosticEntry {
  id: string
  severity: 'error' | 'warning' | 'info'
  category: 'policy' | 'sync' | 'delivery' | 'auth' | 'access'
  message: string
  mailbox?: string
  timestamp: string
  retryable: boolean
  nextStep?: string
}

// Contact type for address book
export interface Contact {
  id: string
  name: string
  email: string
  phone?: string
  company?: string
  labels?: string[]
  display_as?: string
}

// ============================================================================
// API Client
// ============================================================================

interface RequestOptions extends RequestInit {
  headers?: Record<string, string>
}

interface ApiResponse<T = unknown> {
  data?: T
  [key: string]: unknown
}

class API {
  private token: string | null

  constructor() {
    // Token is now stored in HttpOnly cookie by the server
    // No need to read from localStorage (more secure against XSS)
    this.token = null
  }

  setToken(token: string | null): void {
    this.token = token
  }

  async request<T = unknown>(endpoint: string, options: RequestOptions = {}): Promise<T> {
    const url = API_URL + endpoint

    const headers: Record<string, string> = {
      'Content-Type': 'application/json',
      ...options.headers
    }

    // Token is sent automatically via HttpOnly cookie
    // No need to set Authorization header for web clients
    // For API clients that still use Bearer token, we keep the header support
    if (this.token) {
      headers['Authorization'] = `Bearer ${this.token}`
    }

    try {
      const response = await fetch(url, {
        ...options,
        headers,
        credentials: 'include' // Send HttpOnly cookies with requests
      })

      if (!response.ok) {
        // A 401 on an auth endpoint (login/refresh/logout) is a real result the
        // caller must handle (e.g. show "invalid credentials"); only a 401 on a
        // normal API call means the session expired, so bounce to /login then.
        if (response.status === 401 && !endpoint.startsWith('/auth/')) {
          if (window.location.pathname !== '/login') {
            window.location.href = '/login'
          }
          return null as T
        }
        throw new Error(`HTTP ${response.status}`)
      }

      const contentType = response.headers.get('content-type')
      if (contentType && contentType.includes('application/json')) {
        return await response.json() as T
      }
      return await response.text() as unknown as T
    } catch (error) {
      console.error('API error:', error)
      throw error
    }
  }

  // Auth
  async login(credentials: AuthLoginRequest): Promise<AuthLoginResponse> {
    return this.post<AuthLoginResponse>('/auth/login', credentials)
  }

  // logout invalidates the session cookie on the server.
  async logout(): Promise<void> {
    await this.post('/auth/logout')
  }

  // me returns the authenticated user's identity for session rehydration.
  async me(): Promise<{ email: string; isAdmin?: boolean }> {
    return this.get<{ email: string; isAdmin?: boolean }>('/auth/me')
  }

  // changePassword updates the authenticated user's own password.
  async changePassword(currentPassword: string, newPassword: string): Promise<void> {
    await this.post('/account/password', { currentPassword, newPassword })
  }

  // Per-user UI preferences (settings toggles)
  async getPreferences(): Promise<{ preferences?: Record<string, boolean> }> {
    return this.get<{ preferences?: Record<string, boolean> }>('/preferences')
  }

  async setPreferences(prefs: Record<string, boolean>): Promise<void> {
    await this.put('/preferences', prefs)
  }

  // Per-user outgoing-mail signature
  async getSignature(): Promise<{ signature?: string }> {
    return this.get<{ signature?: string }>('/signature')
  }

  async setSignature(signature: string): Promise<void> {
    await this.put('/signature', { signature })
  }

  // Active client sessions
  async getSessions(): Promise<{ sessions?: ClientSession[] }> {
    return this.get<{ sessions?: ClientSession[] }>('/sessions')
  }

  async revokeSession(id: string): Promise<void> {
    await this.delete(`/sessions/${encodeURIComponent(id)}`)
  }

  // Mail
  async getMail(folder: string): Promise<{ emails?: Mail[] }> {
    return this.get<{ emails?: Mail[] }>(`/mail/${folder}`)
  }

  // getMessage fetches a single message by id (resolved across all folders).
  async getMessage(id: string): Promise<Mail> {
    return this.get<Mail>(`/mail/message?id=${encodeURIComponent(id)}`)
  }

  // getMailboxes returns the user's mailbox names.
  async getMailboxes(): Promise<{ mailboxes?: string[] }> {
    return this.get<{ mailboxes?: string[] }>('/mailboxes')
  }

  // Custom folder management (built-in folders cannot be renamed/deleted).
  async createFolder(name: string): Promise<{ name: string }> {
    return this.post<{ name: string }>('/folders', { name })
  }

  async renameFolder(current: string, name: string): Promise<{ name: string }> {
    return this.put<{ name: string }>(`/folders/${encodeURIComponent(current)}`, { name })
  }

  async deleteFolder(name: string): Promise<void> {
    await this.delete(`/folders/${encodeURIComponent(name)}`)
  }

  async sendMail(mail: SendMailRequest): Promise<void> {
    await this.post('/mail/send', mail)
  }

  // saveDraft stores a draft in the Drafts folder, replacing the existing draft
  // when an id is supplied. Returns the (possibly new) draft id.
  async saveDraft(draft: { id?: string; to: string[]; cc?: string[]; bcc?: string[]; subject: string; body: string; from?: string }): Promise<{ id: string }> {
    return this.post<{ id: string }>('/mail/draft', draft)
  }

  async deleteMail(id: string): Promise<void> {
    await this.delete(`/mail/delete?id=${id}`)
  }

  // setFlag sets or clears an IMAP flag (\\Seen for read, \\Flagged for star)
  // on a message so the state persists server-side.
  async setFlag(id: string, flag: '\\Seen' | '\\Flagged', value: boolean): Promise<void> {
    await this.post('/mail/flag', { id, flag, value })
  }

  // moveMail moves a message to another folder (e.g. "inbox" to restore from
  // Trash, or "archive" to archive).
  async moveMail(id: string, to: string): Promise<void> {
    await this.post('/mail/move', { id, to })
  }

  // Filters
  async getFilters(): Promise<{ filters?: Filter[] }> {
    return this.get<{ filters?: Filter[] }>('/filters')
  }

  async createFilter(filter: FilterInput): Promise<Filter> {
    return this.post<Filter>('/filters', filter)
  }

  async updateFilter(id: string, filter: Partial<FilterInput>): Promise<Filter> {
    return this.put<Filter>(`/filters/${id}`, filter)
  }

  async deleteFilter(id: string): Promise<void> {
    await this.delete(`/filters/${id}`)
  }

  // reorderFilters sets the filter priority order (first id = highest priority).
  async reorderFilters(filterIds: string[]): Promise<void> {
    await this.post('/filters/reorder', { filterIds })
  }

  // Vacation/Auto-reply
  async getVacation(): Promise<VacationAutoReply> {
    return this.get<VacationAutoReply>('/vacation')
  }

  async setVacation(vacation: VacationAutoReply): Promise<void> {
    await this.put('/vacation', vacation)
  }

  async deleteVacation(): Promise<void> {
    await this.delete('/vacation')
  }

  // Search
  async search(query: string): Promise<SearchResponse> {
    return this.get<SearchResponse>(`/search?q=${encodeURIComponent(query)}`)
  }

  // Threads
  async getThreads(): Promise<ThreadsResponse> {
    return this.get<ThreadsResponse>('/threads')
  }

  async getThread(id: string): Promise<{ thread?: Thread }> {
    return this.get<{ thread?: Thread }>(`/threads/${id}`)
  }

  // Push notifications
  async getVapidPublicKey(): Promise<{ key?: string }> {
    return this.get<{ key?: string }>('/push/vapid-public-key')
  }

  async subscribePush(subscription: PushSubscription): Promise<void> {
    await this.post('/push/subscribe', subscription)
  }

  async unsubscribePush(endpoint: string): Promise<void> {
    await this.delete(`/push/unsubscribe?endpoint=${encodeURIComponent(endpoint)}`)
  }

  async getPushSubscriptions(): Promise<{ subscriptions?: PushSubscription[] }> {
    return this.get<{ subscriptions?: PushSubscription[] }>('/push/subscriptions')
  }

  // Shared mailboxes
  async getSharedMailboxes(): Promise<{ shared_mailboxes?: SharedMailbox[] }> {
    return this.get<{ shared_mailboxes?: SharedMailbox[] }>('/mailboxes/shared')
  }

  async getSharedAsOwner(): Promise<{ shared_as_owner?: string[] }> {
    return this.get<{ shared_as_owner?: string[] }>('/mailboxes/shared-as-owner')
  }

  // Sender identities for compose
  async getSenderIdentities(personalEmail: string): Promise<SenderIdentity[]> {
    const [sharedResult] = await Promise.all([
      this.getSharedMailboxes()
    ])

    const identities: SenderIdentity[] = []

    // Add personal identity (user's own mailbox)
    identities.push({
      email: personalEmail,
      displayName: personalEmail,
      type: 'personal',
      canSend: true
    })

    // Add identities from shared mailboxes
    if (sharedResult.shared_mailboxes) {
      for (const mb of sharedResult.shared_mailboxes) {
        // User has access to this shared mailbox
        // They can send on behalf of the owner if they have write rights
        identities.push({
          email: mb.owner,
          displayName: `${mb.mailbox} (${mb.owner})`,
          type: 'send-on-behalf',
          mailboxOwner: mb.owner,
          canSend: true // Permission will be validated server-side on send
        })
      }
    }

    return identities
  }

  // Diagnostics
  async getDiagnostics(): Promise<{ errors?: DiagnosticEntry[] }> {
    return this.get<{ errors?: DiagnosticEntry[] }>('/mail/diagnostics')
  }

  async getMailboxDiagnostics(mailbox: string): Promise<{ errors?: DiagnosticEntry[] }> {
    return this.get<{ errors?: DiagnosticEntry[] }>(`/mail/diagnostics?mailbox=${encodeURIComponent(mailbox)}`)
  }

  // Contacts (CardDAV-backed)
  async getContacts(): Promise<{ contacts?: Contact[]; total?: number }> {
    return this.get<{ contacts?: Contact[]; total?: number }>('/contacts')
  }

  async createContact(contact: { name: string; email: string; phone?: string; company?: string }): Promise<{ contact?: Contact; status?: string }> {
    return this.post<{ contact?: Contact; status?: string }>('/contacts', contact)
  }

  async updateContact(id: string, contact: { name: string; email: string; phone?: string; company?: string }): Promise<{ contact?: Contact; status?: string }> {
    return this.put<{ contact?: Contact; status?: string }>(`/contacts/${id}`, contact)
  }

  async deleteContact(id: string): Promise<void> {
    await this.delete(`/contacts/${id}`)
  }

  // Calendar (CalDAV-backed)
  async getCalendarEvents(): Promise<{ events?: CalendarEvent[] }> {
    return this.get<{ events?: CalendarEvent[] }>('/calendar/events')
  }

  async createCalendarEvent(event: CalendarEventInput): Promise<CalendarEvent> {
    return this.post<CalendarEvent>('/calendar/events', event)
  }

  async updateCalendarEvent(uid: string, event: CalendarEventInput): Promise<CalendarEvent> {
    return this.put<CalendarEvent>(`/calendar/events/${encodeURIComponent(uid)}`, event)
  }

  async deleteCalendarEvent(uid: string): Promise<void> {
    await this.delete(`/calendar/events/${encodeURIComponent(uid)}`)
  }

  // Generic methods
  get<T = ApiResponse>(endpoint: string): Promise<T> {
    return this.request<T>(endpoint, { method: 'GET' })
  }

  post<T = unknown>(endpoint: string, data?: unknown): Promise<T> {
    return this.request<T>(endpoint, {
      method: 'POST',
      body: data ? JSON.stringify(data) : undefined
    })
  }

  put<T = unknown>(endpoint: string, data?: unknown): Promise<T> {
    return this.request<T>(endpoint, {
      method: 'PUT',
      body: data ? JSON.stringify(data) : undefined
    })
  }

  delete<T = ApiResponse>(endpoint: string): Promise<T> {
    return this.request<T>(endpoint, { method: 'DELETE' })
  }
}

export default new API()
