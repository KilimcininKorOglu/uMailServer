import { useState, useEffect } from "react";
import { Routes, Route, Navigate, useNavigate } from "react-router-dom";
import { ThemeProvider } from "@/components/theme-provider";
import { Layout } from "@/components/layout";
import { TooltipProvider } from "@/components/ui/tooltip";
import { Toaster } from "@/components/ui/sonner";
import { Login } from "@/pages/Login";
import { Dashboard } from "@/pages/Dashboard";
import { Domains } from "@/pages/Domains";
import { Accounts } from "@/pages/Accounts";
import { Aliases } from "@/pages/Aliases";
import { Groups } from "@/pages/Groups";
import { Queue } from "@/pages/Queue";
import { SettingsPage } from "@/pages/Settings";
import { Policies } from "@/pages/Policies";
import { Delegation } from "@/pages/Delegation";
import { Diagnostics } from "@/pages/Diagnostics";
import { Directory } from "@/pages/Directory";
import { Jobs } from "@/pages/Jobs";
import { Tenants } from "@/pages/Tenants";
import { Cluster } from "@/pages/Cluster";
import { useWebSocket } from "@/hooks/useWebSocket";
import { getCookie, setCookie, deleteCookie } from "@/utils/cookies";
import type { User, Activity } from "@/types";

const adminEmailStorageKey = "umail-admin-email";
const adminPasswordChangeStorageKey = "umail-admin-requires-password-change";

function App() {
  const [isAuthenticated, setIsAuthenticated] = useState(false);
  const [user, setUser] = useState<User | null>(null);
  const [mustChangePassword, setMustChangePassword] = useState(false);
  const [activities, setActivities] = useState<Activity[]>([]);
  const navigate = useNavigate();

  // Check for existing session on mount
  // Token is stored in HttpOnly cookie (more secure against XSS)
  useEffect(() => {
    let cancelled = false;

    const restoreSession = async () => {
      // Only probe for an existing session if this browser previously logged in
      // (the email marker is set on login and cleared on logout). Without it the
      // jwt cookie is absent, so the probe would 403 and log a console error on
      // the login page before the user has done anything.
      if (!getCookie(adminEmailStorageKey)) {
        return;
      }
      try {
        const response = await fetch("/api/v1/accounts", {
          credentials: "include",
        });

        if (cancelled) {
          return;
        }

        if (response.ok) {
          // Resolve the real email: prefer the value stored at login, but if it
          // is missing (e.g. localStorage was cleared) ask the server via
          // /auth/me instead of showing a misleading placeholder address.
          let email = getCookie(adminEmailStorageKey) ?? "";
          if (!email) {
            const me = await fetch("/api/v1/auth/me", { credentials: "include" })
              .then((r) => (r.ok ? r.json() : null))
              .catch(() => null);
            if (cancelled) {
              return;
            }
            email = me?.email ?? "";
            if (email) {
              setCookie(adminEmailStorageKey, email);
            }
          }
          setIsAuthenticated(true);
          setUser({ email, isAdmin: true });
          setMustChangePassword(false);
          deleteCookie(adminPasswordChangeStorageKey);
          return;
        }

        const data = await response.json().catch(() => null);
        if (cancelled) {
          return;
        }

        if (response.status === 403 && data?.error === "password_change_required") {
          const savedEmail = getCookie(adminEmailStorageKey);
          if (savedEmail) {
            setIsAuthenticated(true);
            setUser({ email: savedEmail, isAdmin: true });
            setMustChangePassword(true);
            setCookie(adminPasswordChangeStorageKey, "true");
          }
          return;
        }

        // The stored session is no longer valid (e.g. the cookie expired). Clear
        // the markers so subsequent login-page loads do not re-probe and log a
        // 403 every time.
        deleteCookie(adminEmailStorageKey);
        deleteCookie(adminPasswordChangeStorageKey);
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
    enabled: isAuthenticated,
    onActivity: (activity) => {
      setActivities((prev) => [activity, ...prev].slice(0, 50));
    },
  });

  const handleLogin = (userData: { email: string; mustChangePassword: boolean }) => {
    // Token is stored in HttpOnly cookie by the server
    // No need to store in localStorage (more secure against XSS)
    setCookie(adminEmailStorageKey, userData.email);
    if (userData.mustChangePassword) {
      setCookie(adminPasswordChangeStorageKey, "true");
    } else {
      deleteCookie(adminPasswordChangeStorageKey);
    }
    setIsAuthenticated(true);
    setUser({ email: userData.email, isAdmin: true });
    setMustChangePassword(userData.mustChangePassword);
  };

  const handleLogout = async () => {
    // Revoke the token and clear the HttpOnly cookie server-side; without this
    // the session stays valid and a re-navigation silently re-authenticates.
    try {
      await fetch("/api/v1/auth/logout", {
        method: "POST",
        credentials: "include",
      });
    } catch {
      // Even if the request fails, clear local state below.
    }
    setIsAuthenticated(false);
    setUser(null);
    setMustChangePassword(false);
    setActivities([]);
    deleteCookie(adminEmailStorageKey);
    deleteCookie(adminPasswordChangeStorageKey);
    // Reset the address bar to the admin root so it does not linger on the
    // sub-route the user was viewing when they logged out.
    navigate("/", { replace: true });
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
                  activities={activities}
                />
              }
            />
            <Route path="/domains" element={<Domains />} />
            <Route path="/accounts" element={<Accounts />} />
            <Route path="/aliases" element={<Aliases />} />
            <Route path="/groups" element={<Groups />} />
            <Route path="/queue" element={<Queue />} />
            <Route path="/policies" element={<Policies />} />
            <Route path="/delegation" element={<Delegation />} />
            <Route path="/diagnostics" element={<Diagnostics />} />
            <Route path="/directory" element={<Directory />} />
            <Route path="/jobs" element={<Jobs />} />
            <Route path="/tenants" element={<Tenants />} />
            <Route path="/cluster" element={<Cluster />} />
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
