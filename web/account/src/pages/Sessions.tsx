import { useState, useEffect } from 'react'
import { Monitor, Smartphone, Tablet, Clock, AlertCircle, Trash2 } from 'lucide-react'
import { useI18n } from '../hooks/useI18n'

interface ClientSession {
  id: string
  device_type: string
  client_ip: string
  created_at: string
  last_active: string
  user_agent: string
}

function SessionsPage() {
  const { t } = useI18n()
  const [sessions, setSessions] = useState<ClientSession[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [revoking, setRevoking] = useState<string | null>(null)

  useEffect(() => {
    loadSessions()
  }, [])

  const loadSessions = async () => {
    setLoading(true)
    setError('')
    try {
      const response = await fetch('/api/v1/sessions', {
        credentials: 'include'
      })
      if (response.ok) {
        const data = await response.json()
        setSessions(data.sessions || [])
      } else {
        // Sessions endpoint might not exist yet, show empty state
        setSessions([])
      }
    } catch (err) {
      console.error('Failed to load sessions:', err)
      setError('Failed to load session information')
    } finally {
      setLoading(false)
    }
  }

  const handleRevoke = async (sessionId: string) => {
    if (!confirm(t('sessions.revokeConfirm'))) return

    setRevoking(sessionId)
    try {
      const response = await fetch(`/api/v1/sessions/${sessionId}`, {
        method: 'DELETE',
        credentials: 'include'
      })
      if (response.ok) {
        await loadSessions()
      } else {
        setError('Failed to revoke session')
      }
    } catch (err) {
      console.error('Failed to revoke session:', err)
      setError('Failed to revoke session')
    } finally {
      setRevoking(null)
    }
  }

  const getDeviceIcon = (deviceType: string) => {
    switch (deviceType.toLowerCase()) {
      case 'mobile':
      case 'phone':
        return <Smartphone className="h-5 w-5" />
      case 'tablet':
        return <Tablet className="h-5 w-5" />
      default:
        return <Monitor className="h-5 w-5" />
    }
  }

  const formatDate = (dateStr: string) => {
    try {
      const date = new Date(dateStr)
      return date.toLocaleString()
    } catch {
      return dateStr
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
        {t('sessions.title')}
      </h2>

      {error && (
        <div className="mb-4 p-4 bg-red-50 border border-red-200 rounded-md flex items-center text-red-700">
          <AlertCircle className="h-5 w-5 mr-2" />
          {error}
        </div>
      )}

      {sessions.length === 0 ? (
        <div className="bg-gray-50 rounded-lg p-8 text-center text-gray-500">
          <Monitor className="h-12 w-12 mx-auto mb-4 text-gray-300" />
          <p className="font-medium">{t('sessions.noSessions')}</p>
          <p className="text-sm mt-2">{t('sessions.noSessionsHelp')}</p>
        </div>
      ) : (
        <div className="space-y-4">
          {sessions.map((session) => (
            <div
              key={session.id}
              className="flex items-center justify-between p-4 bg-white border rounded-lg"
            >
              <div className="flex items-center gap-4">
                <div className="p-2 bg-gray-100 rounded-lg text-gray-600">
                  {getDeviceIcon(session.device_type)}
                </div>
                <div>
                  <p className="font-medium text-gray-900">
                    {session.device_type || t('sessions.unknownDevice')}
                  </p>
                  <p className="text-sm text-gray-500">{session.user_agent}</p>
                  <div className="flex items-center gap-4 mt-1 text-xs text-gray-400">
                    <span className="flex items-center gap-1">
                      <Clock className="h-3 w-3" />
                      {t('sessions.lastActive')}: {formatDate(session.last_active)}
                    </span>
                    <span>IP: {session.client_ip}</span>
                  </div>
                </div>
              </div>
              <button
                onClick={() => handleRevoke(session.id)}
                disabled={revoking === session.id}
                className="p-2 text-gray-400 hover:text-red-600 disabled:opacity-50"
                title={t('sessions.revoke')}
              >
                <Trash2 className="h-4 w-4" />
              </button>
            </div>
          ))}
        </div>
      )}
    </div>
  )
}

export default SessionsPage
