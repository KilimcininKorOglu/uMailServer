import { createContext, useContext, useState, useCallback, useEffect } from 'react'
import api, { SharedMailbox, Mail } from '../utils/api'

interface MailboxContextType {
  // Current active mailbox context
  currentMailbox: {
    type: 'personal' | 'shared'
    email: string // The email address of the current mailbox
    owner?: string // For shared mailboxes, the owner's email
  }
  
  // List of shared mailboxes the user has access to
  sharedMailboxes: SharedMailbox[]
  
  // Loading state
  loading: boolean
  
  // Switch to a different mailbox context
  switchMailbox: (email: string, owner?: string) => void
  
  // Switch back to personal mailbox
  switchToPersonal: () => void
  
  // Load shared mailboxes
  loadSharedMailboxes: () => Promise<void>
  
  // Check if currently in a shared mailbox
  isInSharedMailbox: () => boolean

  // Shared inbox state: fetched once here and consumed by the inbox page, the
  // sidebar unread badge, and the header notifications, so they stay in sync
  // (a read/delete in one place updates the others) and the inbox is not
  // fetched three times on load.
  inboxEmails: Mail[]
  inboxUnread: number
  inboxLoading: boolean
  refreshInbox: () => Promise<void>
  // Optimistically apply changes (e.g. read/starred) to inbox messages.
  patchInbox: (ids: string[], changes: Partial<Mail>) => void
  // Optimistically drop messages from the inbox (archive/delete).
  removeFromInbox: (ids: string[]) => void
}

const MailboxContext = createContext<MailboxContextType | null>(null)

export function MailboxProvider({ children, personalEmail }: { children: React.ReactNode; personalEmail: string }) {
  const [currentMailbox, setCurrentMailbox] = useState<{
    type: 'personal' | 'shared'
    email: string
    owner?: string
  }>({
    type: 'personal',
    email: personalEmail
  })
  const [sharedMailboxes, setSharedMailboxes] = useState<SharedMailbox[]>([])
  const [loading, setLoading] = useState(false)
  const [inboxEmails, setInboxEmails] = useState<Mail[]>([])
  const [inboxLoading, setInboxLoading] = useState(true)

  // fetchInbox pulls the inbox without toggling the loading flag, so background
  // polling does not flash the skeleton.
  const fetchInbox = useCallback(async () => {
    const res = await api.getMail('inbox')
    setInboxEmails(res.emails ?? [])
  }, [])

  const refreshInbox = useCallback(async () => {
    setInboxLoading(true)
    try {
      await fetchInbox()
    } catch {
      setInboxEmails([])
    } finally {
      setInboxLoading(false)
    }
  }, [fetchInbox])

  useEffect(() => {
    refreshInbox()
  }, [refreshInbox])

  // Real-time inbox updates (push-to-pull): the server pushes a lightweight
  // signal over SSE when something changes, and the UI fetches the message over
  // HTTP in response — so the inbox updates instantly without aggressive
  // polling, and the traffic stays controlled.
  useEffect(() => {
    const refresh = () => {
      fetchInbox().catch(() => undefined)
    }

    // Primary path: Server-Sent Events. The /api/v1/events stream authenticates
    // via the HttpOnly jwt cookie on same-origin requests, so EventSource is the
    // correct client. The browser auto-reconnects on transient drops.
    let es: EventSource | null = null
    try {
      es = new EventSource('/api/v1/events', { withCredentials: true })
      // "new_mail" is the delivery signal; expunge/flags/folder changes can come
      // from another client or IMAP, so refresh on those too to stay in sync.
      es.addEventListener('new_mail', refresh)
      es.addEventListener('expunge', refresh)
      es.addEventListener('flags_changed', refresh)
      es.addEventListener('folder_update', refresh)
    } catch {
      es = null
    }

    // Fallback safety net for when the SSE stream is unavailable: a slow poll
    // plus a refresh when the tab regains focus. Kept long (push drives
    // immediacy) so background traffic stays minimal.
    const interval = setInterval(refresh, 300000)
    const onVisible = () => {
      if (document.visibilityState === 'visible') refresh()
    }
    document.addEventListener('visibilitychange', onVisible)

    return () => {
      es?.close()
      clearInterval(interval)
      document.removeEventListener('visibilitychange', onVisible)
    }
  }, [fetchInbox])

  const patchInbox = useCallback((ids: string[], changes: Partial<Mail>) => {
    const idset = new Set(ids)
    setInboxEmails((prev) => prev.map((m) => (idset.has(m.id) ? { ...m, ...changes } : m)))
  }, [])

  const removeFromInbox = useCallback((ids: string[]) => {
    const idset = new Set(ids)
    setInboxEmails((prev) => prev.filter((m) => !idset.has(m.id)))
  }, [])

  const inboxUnread = inboxEmails.filter((m) => !m.read).length

  const loadSharedMailboxes = useCallback(async () => {
    setLoading(true)
    try {
      const result = await api.getSharedMailboxes()
      if (result.shared_mailboxes) {
        setSharedMailboxes(result.shared_mailboxes)
      }
    } catch (err) {
      console.error('Failed to load shared mailboxes:', err)
    } finally {
      setLoading(false)
    }
  }, [])

  const switchMailbox = useCallback((email: string, owner?: string) => {
    if (owner && owner !== email) {
      // This is a shared mailbox
      setCurrentMailbox({
        type: 'shared',
        email,
        owner
      })
    } else {
      // Personal mailbox
      setCurrentMailbox({
        type: 'personal',
        email
      })
    }
  }, [])

  const switchToPersonal = useCallback(() => {
    setCurrentMailbox({
      type: 'personal',
      email: personalEmail
    })
  }, [personalEmail])

  const isInSharedMailbox = useCallback(() => {
    return currentMailbox.type === 'shared'
  }, [currentMailbox.type])

  const value: MailboxContextType = {
    currentMailbox,
    sharedMailboxes,
    loading,
    switchMailbox,
    switchToPersonal,
    loadSharedMailboxes,
    isInSharedMailbox,
    inboxEmails,
    inboxUnread,
    inboxLoading,
    refreshInbox,
    patchInbox,
    removeFromInbox
  }

  return (
    <MailboxContext.Provider value={value}>
      {children}
    </MailboxContext.Provider>
  )
}

export function useMailbox() {
  const context = useContext(MailboxContext)
  if (!context) {
    throw new Error('useMailbox must be used within a MailboxProvider')
  }
  return context
}
