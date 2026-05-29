import { useState, useEffect } from 'react'
import { Mailbox, Users, Shield, AlertCircle } from 'lucide-react'
import { useI18n } from '../hooks/useI18n'

interface SharedMailbox {
  owner: string
  mailbox: string
}

interface GranteeMailbox {
  email: string
  name?: string
}

function SharedMailboxPage() {
  const { t } = useI18n()
  const [sharedMailboxes, setSharedMailboxes] = useState<SharedMailbox[]>([])
  const [grantedMailboxes, setGrantedMailboxes] = useState<GranteeMailbox[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')

  useEffect(() => {
    loadMailboxes()
  }, [])

  const loadMailboxes = async () => {
    setLoading(true)
    setError('')
    try {
      // Load shared mailboxes (mailboxes shared with current user)
      const sharedRes = await fetch('/api/v1/mailboxes/shared', {
        credentials: 'include'
      })
      // Load mailboxes where current user is the owner (they've shared with others)
      const ownerRes = await fetch('/api/v1/mailboxes/shared-as-owner', {
        credentials: 'include'
      })

      if (sharedRes.ok) {
        const data = await sharedRes.json()
        setSharedMailboxes(data.shared_mailboxes || [])
      }

      if (ownerRes.ok) {
        const data = await ownerRes.json()
        setGrantedMailboxes(data.shared_as_owner || [])
      }
    } catch (err) {
      console.error('Failed to load shared mailboxes:', err)
      setError('Failed to load mailbox information')
    } finally {
      setLoading(false)
    }
  }

  if (loading) {
    return (
      <div className="flex items-center justify-center h-64">
        <div className="animate-spin rounded-full h-8 w-8 border-b-2 border-primary-600"></div>
      </div>
    )
  }

  return (
    <div>
      <h2 className="text-lg font-medium text-gray-900 mb-6">
        {t('sharedMailbox.title')}
      </h2>

      {error && (
        <div className="mb-4 p-4 bg-red-50 border border-red-200 rounded-md flex items-center text-red-700">
          <AlertCircle className="h-5 w-5 mr-2" />
          {error}
        </div>
      )}

      {/* Shared With Me Section */}
      <div className="mb-8">
        <h3 className="text-sm font-medium text-gray-700 mb-3 flex items-center">
          <Users className="h-4 w-4 mr-2" />
          {t('sharedMailbox.sharedWithMe')}
        </h3>
        {sharedMailboxes.length === 0 ? (
          <div className="bg-gray-50 rounded-lg p-6 text-center text-gray-500">
            <Mailbox className="h-8 w-8 mx-auto mb-2 text-gray-300" />
            <p>{t('sharedMailbox.noSharedMailboxes')}</p>
            <p className="text-sm mt-1">{t('sharedMailbox.noSharedMailboxesHelp')}</p>
          </div>
        ) : (
          <div className="space-y-3">
            {sharedMailboxes.map((mbox, index) => (
              <div
                key={`${mbox.owner}-${mbox.mailbox}-${index}`}
                className="flex items-center justify-between p-4 bg-white border rounded-lg"
              >
                <div className="flex items-center">
                  <Mailbox className="h-5 w-5 text-primary-600 mr-3" />
                  <div>
                    <p className="font-medium text-gray-900">{mbox.mailbox}</p>
                    <p className="text-sm text-gray-500">
                      {t('sharedMailbox.owner')}: {mbox.owner}
                    </p>
                  </div>
                </div>
                <a
                  href={`/webmail/?mailbox=${encodeURIComponent(mbox.owner + ':' + mbox.mailbox)}`}
                  className="px-3 py-1.5 text-sm font-medium text-primary-600 hover:text-primary-700 border border-primary-600 rounded-md hover:bg-primary-50"
                >
                  {t('sharedMailbox.openMailbox')}
                </a>
              </div>
            ))}
          </div>
        )}
      </div>

      {/* Shared From Me Section */}
      <div>
        <h3 className="text-sm font-medium text-gray-700 mb-3 flex items-center">
          <Shield className="h-4 w-4 mr-2" />
          {t('sharedMailbox.sharedFromMe')}
        </h3>
        {grantedMailboxes.length === 0 ? (
          <div className="bg-gray-50 rounded-lg p-6 text-center text-gray-500">
            <Shield className="h-8 w-8 mx-auto mb-2 text-gray-300" />
            <p>{t('sharedMailbox.noGrantedMailboxes')}</p>
            <p className="text-sm mt-1">{t('sharedMailbox.noGrantedMailboxesHelp')}</p>
          </div>
        ) : (
          <div className="space-y-3">
            {grantedMailboxes.map((mbox, index) => (
              <div
                key={`${mbox.email}-${index}`}
                className="flex items-center justify-between p-4 bg-white border rounded-lg"
              >
                <div className="flex items-center">
                  <Mailbox className="h-5 w-5 text-gray-400 mr-3" />
                  <div>
                    <p className="font-medium text-gray-900">{mbox.email}</p>
                    {mbox.name && (
                      <p className="text-sm text-gray-500">{mbox.name}</p>
                    )}
                  </div>
                </div>
                <a
                  href={`/admin/accounts/${encodeURIComponent(mbox.email)}`}
                  className="px-3 py-1.5 text-sm font-medium text-gray-600 hover:text-gray-700 border border-gray-300 rounded-md hover:bg-gray-50"
                >
                  {t('sharedMailbox.manageAccess')}
                </a>
              </div>
            ))}
          </div>
        )}
      </div>
    </div>
  )
}

export default SharedMailboxPage
