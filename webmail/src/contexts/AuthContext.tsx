import { createContext, useContext, useState, useCallback, useEffect } from 'react'
import api from '../utils/api'

interface AuthContextType {
  user: { email: string; hasAvatar?: boolean } | null
  isAuthenticated: boolean
  isLoading: boolean
  loading: boolean
  error: string | null
  login: (email: string, password: string) => Promise<boolean>
  logout: () => Promise<void>
}

const AuthContext = createContext<AuthContextType | null>(null)

// Marker set on a successful login and cleared on logout / a stale-session
// probe. Without it, the mount-time `api.me()` probe runs on a fresh or
// logged-out browser and logs a 401 on the login screen before the user has
// done anything. (The JWT itself lives in an unreadable HttpOnly cookie.)
const sessionMarkerKey = 'umail-webmail-authed'

export function AuthProvider({ children }: { children: React.ReactNode }) {
  const [user, setUser] = useState<{ email: string; hasAvatar?: boolean } | null>(null)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [isAuthenticated, setIsAuthenticated] = useState(false)
  // hydrating gates routing until we know whether a valid session cookie exists.
  const [hydrating, setHydrating] = useState(true)

  // On mount, ask the server who we are. The JWT lives in an HttpOnly cookie the
  // client cannot read, so this is the only way to restore the session after a
  // page reload instead of bouncing the user to /login.
  useEffect(() => {
    let active = true
    // Only probe for a session if this browser previously logged in; otherwise
    // skip the request so the login screen does not log a 401.
    if (!localStorage.getItem(sessionMarkerKey)) {
      setHydrating(false)
      return
    }
    api.me()
      .then((me) => {
        if (!active) return
        if (me?.authenticated && me.email) {
          setUser({ email: me.email, hasAvatar: me.has_avatar })
          setIsAuthenticated(true)
        } else {
          // Soft 200 with authenticated:false — the stored session is no longer
          // valid. Clear the marker so we stop probing on future loads.
          localStorage.removeItem(sessionMarkerKey)
        }
      })
      .catch(() => {
        // Network/other failure: clear the marker so we stop probing on future
        // loads (the soft check itself no longer returns 401).
        localStorage.removeItem(sessionMarkerKey)
      })
      .finally(() => {
        if (active) setHydrating(false)
      })
    return () => {
      active = false
    }
  }, [])

  const login = useCallback(async (email: string, password: string): Promise<boolean> => {
    setLoading(true)
    setError(null)
    try {
      // Token is now in HttpOnly cookie - no need to store in memory
      await api.post<{ expiresIn?: number }>('/auth/login', { email, password })
      localStorage.setItem(sessionMarkerKey, '1')
      setUser({ email })
      setIsAuthenticated(true)
      return true
    } catch (err: unknown) {
      setError(err instanceof Error ? err.message : 'Login failed')
      return false
    } finally {
      setLoading(false)
    }
  }, [])

  const logout = useCallback(async () => {
    // Invalidate the HttpOnly session cookie server-side; clear local state
    // regardless of the request outcome so the UI never gets stuck signed in.
    try {
      await api.logout()
    } catch (err) {
      console.error('Logout request failed:', err)
    }
    setUser(null)
    setIsAuthenticated(false)
    api.setToken(null)
    localStorage.removeItem(sessionMarkerKey)
  }, [])

  const value: AuthContextType = {
    user,
    isAuthenticated,
    isLoading: hydrating,
    loading,
    error,
    login,
    logout
  }

  return (
    <AuthContext.Provider value={value}>
      {children}
    </AuthContext.Provider>
  )
}

export function useAuth() {
  const context = useContext(AuthContext)
  if (!context) {
    throw new Error('useAuth must be used within an AuthProvider')
  }
  return context
}
