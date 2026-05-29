import { createContext, useContext, useState, useCallback } from 'react'
import api, { SharedMailbox } from '../utils/api'

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
    isInSharedMailbox
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
