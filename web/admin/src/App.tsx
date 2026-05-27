import { useState, useEffect } from "react";
import { Routes, Route, Navigate } from "react-router-dom";
import { ThemeProvider } from "@/components/theme-provider";
import { Layout } from "@/components/layout";
import { TooltipProvider } from "@/components/ui/tooltip";
import { Toaster } from "@/components/ui/sonner";
import { Login } from "@/pages/Login";
import { Dashboard } from "@/pages/Dashboard";
import { Domains } from "@/pages/Domains";
import { Accounts } from "@/pages/Accounts";
import { Queue } from "@/pages/Queue";
import { SettingsPage } from "@/pages/Settings";
import { useWebSocket } from "@/hooks/useWebSocket";
import type { User, Activity, RealtimeMetrics } from "@/types";

const adminEmailStorageKey = "umail-admin-email";
const adminPasswordChangeStorageKey = "umail-admin-requires-password-change";

function App() {
  const [isAuthenticated, setIsAuthenticated] = useState(false);
  const [user, setUser] = useState<User | null>(null);
  const [mustChangePassword, setMustChangePassword] = useState(false);
  const [activities, setActivities] = useState<Activity[]>([]);
  const [metrics, setMetrics] = useState<RealtimeMetrics | undefined>();

  // Check for existing session on mount
  // Token is stored in HttpOnly cookie (more secure against XSS)
  useEffect(() => {
    let cancelled = false;

    const restoreSession = async () => {
      try {
        const response = await fetch("/api/v1/accounts", {
          credentials: "include",
        });

        if (cancelled) {
          return;
        }

        if (response.ok) {
          const savedEmail = localStorage.getItem(adminEmailStorageKey) || "admin@example.com";
          setIsAuthenticated(true);
          setUser({ email: savedEmail, isAdmin: true });
          setMustChangePassword(false);
          localStorage.removeItem(adminPasswordChangeStorageKey);
          return;
        }

        const data = await response.json().catch(() => null);
        if (cancelled) {
          return;
        }

        if (response.status === 403 && data?.error === "password_change_required") {
          const savedEmail = localStorage.getItem(adminEmailStorageKey);
          if (savedEmail) {
            setIsAuthenticated(true);
            setUser({ email: savedEmail, isAdmin: true });
            setMustChangePassword(true);
            localStorage.setItem(adminPasswordChangeStorageKey, "true");
          }
        }
      } catch {
        // Not authenticated
      }
    };

    void restoreSession();

    return () => {
      cancelled = true;
    };
  }, []);

  // WebSocket connection for realtime updates
  const { isConnected } = useWebSocket({
    onMetrics: (newMetrics) => {
      setMetrics(newMetrics);
    },
    onActivity: (activity) => {
      setActivities((prev) => [activity, ...prev].slice(0, 50));
    },
  });

  const handleLogin = (userData: { email: string; mustChangePassword: boolean }) => {
    // Token is stored in HttpOnly cookie by the server
    // No need to store in localStorage (more secure against XSS)
    localStorage.setItem(adminEmailStorageKey, userData.email);
    if (userData.mustChangePassword) {
      localStorage.setItem(adminPasswordChangeStorageKey, "true");
    } else {
      localStorage.removeItem(adminPasswordChangeStorageKey);
    }
    setIsAuthenticated(true);
    setUser({ email: userData.email, isAdmin: true });
    setMustChangePassword(userData.mustChangePassword);
  };

  const handleLogout = () => {
    // Server will clear the HttpOnly cookie
    setIsAuthenticated(false);
    setUser(null);
    setMustChangePassword(false);
    setActivities([]);
    setMetrics(undefined);
    localStorage.removeItem(adminEmailStorageKey);
    localStorage.removeItem(adminPasswordChangeStorageKey);
  };

  const handlePasswordChangeComplete = () => {
    handleLogout();
  };

  if (!isAuthenticated) {
    return (
      <ThemeProvider defaultTheme="system" storageKey="umail-admin-theme">
        <TooltipProvider>
          <Login onLogin={handleLogin} />
        </TooltipProvider>
      </ThemeProvider>
    );
  }

  if (mustChangePassword && user) {
    return (
      <ThemeProvider defaultTheme="system" storageKey="umail-admin-theme">
        <TooltipProvider>
          <Layout user={user} onLogout={handleLogout} isConnected={isConnected}>
            <SettingsPage
              userEmail={user.email}
              requirePasswordChange
              onPasswordChanged={handlePasswordChangeComplete}
            />
          </Layout>
          <Toaster />
        </TooltipProvider>
      </ThemeProvider>
    );
  }

  return (
    <ThemeProvider defaultTheme="system" storageKey="umail-admin-theme">
      <TooltipProvider>
        <Layout user={user} onLogout={handleLogout} isConnected={isConnected}>
          <Routes>
            <Route
              path="/"
              element={
                <Dashboard
                  isConnected={isConnected}
                  metrics={metrics}
                  activities={activities}
                />
              }
            />
            <Route path="/domains" element={<Domains />} />
            <Route path="/accounts" element={<Accounts />} />
            <Route path="/queue" element={<Queue />} />
            <Route path="/settings" element={<SettingsPage />} />
            <Route path="*" element={<Navigate to="/" replace />} />
          </Routes>
        </Layout>
        <Toaster />
      </TooltipProvider>
    </ThemeProvider>
  );
}

export default App;
