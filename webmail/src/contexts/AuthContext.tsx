import { createContext, useContext, useState, useCallback, useEffect } from 'react'
import api from '../utils/api'

interface AuthContextType {
  user: { email: string } | null
  isAuthenticated: boolean
  isLoading: boolean
  loading: boolean
  error: string | null
  login: (email: string, password: string) => Promise<boolean>
  logout: () => Promise<void>
}

const AuthContext = createContext<AuthContextType | null>(null)

export function AuthProvider({ children }: { children: React.ReactNode }) {
  const [user, setUser] = useState<{ email: string } | null>(null)
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
    api.me()
      .then((me) => {
        if (active && me?.email) {
          setUser({ email: me.email })
          setIsAuthenticated(true)
        }
      })
      .catch(() => {
        // No valid session: remain logged out (no redirect, /auth/* 401s throw).
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
