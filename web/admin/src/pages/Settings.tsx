import { useState, useEffect, type FormEvent } from "react";
import {
  Settings,
  Shield,
  Bell,
  Server,
  Database,
  Save,
  AlertCircle,
  KeyRound,
  RefreshCw,
} from "lucide-react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Separator } from "@/components/ui/separator";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { useConfig, useJWT, type JWTStatus } from "@/hooks/useApi";
import type { ServerConfig } from "@/types";

interface SettingsPageProps {
  userEmail?: string;
  requirePasswordChange?: boolean;
  onPasswordChanged?: () => void;
}

export function SettingsPage({
  userEmail = "",
  requirePasswordChange = false,
  onPasswordChanged,
}: SettingsPageProps) {
  const { config, setConfig, fetchConfig, updateConfig } = useConfig();
  const { fetchStatus: fetchJWTStatus, rotate: rotateJWT } = useJWT();
  const [savingConfig, setSavingConfig] = useState(false);
  const [error, setError] = useState("");
  const [newPassword, setNewPassword] = useState("");
  const [confirmPassword, setConfirmPassword] = useState("");
  const [passwordSaving, setPasswordSaving] = useState(false);
  const [jwtStatus, setJwtStatus] = useState<JWTStatus | null>(null);
  const [jwtRotating, setJwtRotating] = useState(false);
  const [jwtDialogOpen, setJwtDialogOpen] = useState(false);

  useEffect(() => {
    if (!requirePasswordChange) {
      fetchConfig().catch(() => setError("Failed to load server configuration"));
      fetchJWTStatus()
        .then(setJwtStatus)
        .catch(() => undefined);
    }
  }, [requirePasswordChange, fetchConfig, fetchJWTStatus]);

  const handleRotateJWT = async () => {
    setJwtRotating(true);
    try {
      const result = await rotateJWT();
      toast.success(result.message || "JWT signing key rotated");
      const status = await fetchJWTStatus();
      setJwtStatus(status);
    } catch (err) {
      toast.error((err as { message?: string }).message || "Failed to rotate JWT signing key");
    } finally {
      setJwtRotating(false);
      setJwtDialogOpen(false);
    }
  };

  const setField = <K extends keyof ServerConfig>(key: K, value: ServerConfig[K]) => {
    setConfig((prev) => (prev ? { ...prev, [key]: value } : prev));
  };

  const handleSave = async () => {
    if (!config) return;
    setSavingConfig(true);
    setError("");
    try {
      const result = await updateConfig(config);
      const restart = result.restart_required ?? [];
      if (restart.length > 0) {
        toast.warning(
          `Settings saved. A server restart is required for: ${restart.join(", ")}`,
        );
      } else {
        toast.success("Settings saved successfully");
      }
    } catch (err) {
      toast.error((err as { message?: string }).message || "Failed to save settings");
    } finally {
      setSavingConfig(false);
    }
  };

  const handleRequiredPasswordChange = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    setError("");

    if (!userEmail) {
      setError("Unable to determine the current admin account");
      return;
    }
    if (newPassword !== confirmPassword) {
      setError("New passwords do not match");
      return;
    }

    setPasswordSaving(true);

    try {
      const response = await fetch(`/api/v1/accounts/${userEmail}`, {
        method: "PUT",
        headers: {
          "Content-Type": "application/json",
        },
        credentials: "include",
        body: JSON.stringify({ password: newPassword }),
      });

      const data = await response.json().catch(() => null);
      if (!response.ok) {
        throw new Error(data?.error || "Failed to change password");
      }

      await fetch("/api/v1/auth/logout", {
        method: "POST",
        credentials: "include",
      }).catch(() => undefined);

      onPasswordChanged?.();
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to change password");
    } finally {
      setPasswordSaving(false);
    }
  };

  if (requirePasswordChange) {
    return (
      <div className="space-y-6 max-w-2xl">
        <div>
          <h1 className="text-3xl font-bold tracking-tight">Change Admin Password</h1>
          <p className="text-muted-foreground mt-1">
            The bootstrap admin account must set a new password before you can use the admin UI.
          </p>
        </div>

        <Alert variant="destructive">
          <AlertCircle className="h-4 w-4" />
          <AlertDescription>
            Sign in with the temporary bootstrap password only once, then replace it with a strong password to continue.
          </AlertDescription>
        </Alert>

        {error && (
          <Alert variant="destructive">
            <AlertCircle className="h-4 w-4" />
            <AlertDescription>{error}</AlertDescription>
          </Alert>
        )}

        <Card>
          <CardHeader>
            <CardTitle>Set a new password for {userEmail}</CardTitle>
            <CardDescription>
              Your new password must include uppercase, lowercase, number, and special characters.
            </CardDescription>
          </CardHeader>
          <CardContent>
            <form onSubmit={handleRequiredPasswordChange} className="space-y-4">
              <div className="space-y-2">
                <Label htmlFor="new-password">New Password</Label>
                <Input
                  id="new-password"
                  type="password"
                  value={newPassword}
                  onChange={(event) => setNewPassword(event.target.value)}
                  autoComplete="new-password"
                  required
                />
              </div>
              <div className="space-y-2">
                <Label htmlFor="confirm-password">Confirm New Password</Label>
                <Input
                  id="confirm-password"
                  type="password"
                  value={confirmPassword}
                  onChange={(event) => setConfirmPassword(event.target.value)}
                  autoComplete="new-password"
                  required
                />
              </div>
              <div className="flex justify-end">
                <Button type="submit" disabled={passwordSaving}>
                  {passwordSaving ? "Updating..." : "Update Password"}
                </Button>
              </div>
            </form>
          </CardContent>
        </Card>
      </div>
    );
  }

  return (
    <div className="space-y-6">
      {/* Header */}
      <div>
        <h1 className="text-3xl font-bold tracking-tight">Settings</h1>
        <p className="text-muted-foreground mt-1">
          Configure your email server settings
        </p>
      </div>

      {error && (
        <Alert variant="destructive">
          <AlertCircle className="h-4 w-4" />
          <AlertDescription>{error}</AlertDescription>
        </Alert>
      )}

      <Tabs defaultValue="general" className="space-y-6">
        <TabsList>
          <TabsTrigger value="general">
            <Settings className="h-4 w-4 mr-2" />
            General
          </TabsTrigger>
          <TabsTrigger value="security">
            <Shield className="h-4 w-4 mr-2" />
            Security
          </TabsTrigger>
          <TabsTrigger value="notifications">
            <Bell className="h-4 w-4 mr-2" />
            Notifications
          </TabsTrigger>
        </TabsList>

        <TabsContent value="general" className="space-y-6">
          <Card>
            <CardHeader>
              <CardTitle className="flex items-center gap-2">
                <Server className="h-5 w-5" />
                Server Configuration
              </CardTitle>
              <CardDescription>
                Basic server settings and hostname configuration
              </CardDescription>
            </CardHeader>
            <CardContent className="space-y-4">
              <div className="grid gap-4 sm:grid-cols-2">
                <div className="space-y-2">
                  <Label htmlFor="hostname">Server Hostname</Label>
                  <Input
                    id="hostname"
                    placeholder="mail.example.com"
                    value={config?.hostname ?? ""}
                    onChange={(e) => setField("hostname", e.target.value)}
                  />
                </div>
                <div className="space-y-2">
                  <Label htmlFor="data-dir">Data Directory</Label>
                  <Input
                    id="data-dir"
                    placeholder="/var/lib/umailserver"
                    value={config?.data_dir ?? ""}
                    onChange={(e) => setField("data_dir", e.target.value)}
                  />
                </div>
              </div>

              <Separator />

              <div className="space-y-4">
                <h4 className="text-sm font-medium">Port Configuration</h4>
                <div className="grid gap-4 sm:grid-cols-3">
                  <div className="space-y-2">
                    <Label htmlFor="smtp-port">SMTP Port</Label>
                    <Input
                      id="smtp-port"
                      type="number"
                      value={config?.smtp_port ?? 0}
                      onChange={(e) => setField("smtp_port", parseInt(e.target.value) || 0)}
                    />
                  </div>
                  <div className="space-y-2">
                    <Label htmlFor="submission-port">Submission Port</Label>
                    <Input
                      id="submission-port"
                      type="number"
                      value={config?.submission_port ?? 0}
                      onChange={(e) => setField("submission_port", parseInt(e.target.value) || 0)}
                    />
                  </div>
                  <div className="space-y-2">
                    <Label htmlFor="imap-port">IMAP Port</Label>
                    <Input
                      id="imap-port"
                      type="number"
                      value={config?.imap_port ?? 0}
                      onChange={(e) => setField("imap_port", parseInt(e.target.value) || 0)}
                    />
                  </div>
                </div>
              </div>
            </CardContent>
          </Card>

          <Card>
            <CardHeader>
              <CardTitle className="flex items-center gap-2">
                <Database className="h-5 w-5" />
                Storage & Limits
              </CardTitle>
              <CardDescription>
                Configure storage limits and message handling
              </CardDescription>
            </CardHeader>
            <CardContent className="space-y-4">
              <div className="grid gap-4 sm:grid-cols-2">
                <div className="space-y-2">
                  <Label htmlFor="max-message-size">Max Message Size (MB)</Label>
                  <Input
                    id="max-message-size"
                    type="number"
                    value={config?.max_message_size_mb ?? 0}
                    onChange={(e) => setField("max_message_size_mb", parseInt(e.target.value) || 0)}
                  />
                </div>
                <div className="space-y-2">
                  <Label htmlFor="max-recipients">Max Recipients</Label>
                  <Input
                    id="max-recipients"
                    type="number"
                    value={config?.max_recipients ?? 0}
                    onChange={(e) => setField("max_recipients", parseInt(e.target.value) || 0)}
                  />
                </div>
              </div>
              <div className="flex items-center justify-between pt-2">
                <div className="space-y-0.5">
                  <Label>Enable Greylisting</Label>
                  <p className="text-xs text-muted-foreground">
                    Temporarily reject unknown senders to reduce spam
                  </p>
                </div>
                <Switch
                  checked={config?.greylisting_enabled ?? false}
                  onCheckedChange={(c) => setField("greylisting_enabled", c)}
                />
              </div>
            </CardContent>
          </Card>

          <div className="flex justify-end">
            <Button onClick={handleSave} disabled={savingConfig || !config}>
              <Save className="mr-2 h-4 w-4" />
              {savingConfig ? "Saving..." : "Save Changes"}
            </Button>
          </div>
        </TabsContent>

        <TabsContent value="security" className="space-y-6">
          <Card>
            <CardHeader>
              <CardTitle>TLS & Encryption</CardTitle>
              <CardDescription>
                Configure TLS certificates and encryption settings
              </CardDescription>
            </CardHeader>
            <CardContent className="space-y-4">
              <div className="flex items-center justify-between">
                <div className="space-y-0.5">
                  <Label>Auto TLS (Let's Encrypt)</Label>
                  <p className="text-xs text-muted-foreground">
                    Automatically obtain and renew certificates
                  </p>
                </div>
                <Switch
                  checked={config?.auto_tls ?? false}
                  onCheckedChange={(c) => setField("auto_tls", c)}
                />
              </div>
              <Separator />
              <div className="flex items-center justify-between">
                <div className="space-y-0.5">
                  <Label>Require TLS for SMTP</Label>
                  <p className="text-xs text-muted-foreground">
                    Only accept encrypted connections
                  </p>
                </div>
                <Switch
                  checked={config?.require_tls_smtp ?? false}
                  onCheckedChange={(c) => setField("require_tls_smtp", c)}
                />
              </div>
              <Separator />
              <div className="flex items-center justify-between">
                <div className="space-y-0.5">
                  <Label>DKIM Signing (global)</Label>
                  <p className="text-xs text-muted-foreground">
                    Sign outgoing mail with DKIM server-wide. Per-domain DKIM
                    keys (shown on the Domains page) are only used when this is on.
                  </p>
                </div>
                <Switch
                  checked={config?.dkim_signing ?? false}
                  onCheckedChange={(c) => setField("dkim_signing", c)}
                />
              </div>
            </CardContent>
          </Card>

          <Card>
            <CardHeader>
              <CardTitle>Rate Limiting</CardTitle>
              <CardDescription>
                Configure rate limits to prevent abuse
              </CardDescription>
            </CardHeader>
            <CardContent className="space-y-4">
              <div className="grid gap-4 sm:grid-cols-2">
                <div className="space-y-2">
                  <Label htmlFor="rate-limit">Max Emails per Hour</Label>
                  <Input
                    id="rate-limit"
                    type="number"
                    value={config?.max_emails_per_hour ?? 0}
                    onChange={(e) => setField("max_emails_per_hour", parseInt(e.target.value) || 0)}
                  />
                </div>
                <div className="space-y-2">
                  <Label htmlFor="auth-attempts">Max Auth Attempts</Label>
                  <Input
                    id="auth-attempts"
                    type="number"
                    value={config?.max_login_attempts ?? 0}
                    onChange={(e) => setField("max_login_attempts", parseInt(e.target.value) || 0)}
                  />
                </div>
              </div>
            </CardContent>
          </Card>

          <Card>
            <CardHeader>
              <CardTitle className="flex items-center gap-2">
                <KeyRound className="h-5 w-5" />
                JWT Signing Key
              </CardTitle>
              <CardDescription>
                Rotate the secret used to sign admin and user session tokens.
                Existing tokens stay valid until they expire.
              </CardDescription>
            </CardHeader>
            <CardContent className="space-y-4">
              <div className="flex items-center justify-between">
                <div className="space-y-0.5">
                  <Label>Active Signing Keys</Label>
                  <p className="text-xs text-muted-foreground">
                    {jwtStatus
                      ? `${jwtStatus.activeKeys} active key(s); current: ${jwtStatus.currentKid}`
                      : "Loading key status..."}
                  </p>
                </div>
                <Button
                  variant="outline"
                  onClick={() => setJwtDialogOpen(true)}
                  disabled={jwtRotating}
                >
                  <RefreshCw className={`mr-2 h-4 w-4 ${jwtRotating ? "animate-spin" : ""}`} />
                  {jwtRotating ? "Rotating..." : "Rotate Key"}
                </Button>
              </div>
            </CardContent>
          </Card>

          <div className="flex justify-end">
            <Button onClick={handleSave} disabled={savingConfig || !config}>
              <Save className="mr-2 h-4 w-4" />
              {savingConfig ? "Saving..." : "Save Changes"}
            </Button>
          </div>
        </TabsContent>

        <TabsContent value="notifications" className="space-y-6">
          <Card>
            <CardHeader>
              <CardTitle>Email Notifications</CardTitle>
              <CardDescription>
                Notification preferences (persisted server-side)
              </CardDescription>
            </CardHeader>
            <CardContent className="space-y-4">
              <div className="flex items-center justify-between">
                <div className="space-y-0.5">
                  <Label>Queue Alerts</Label>
                  <p className="text-xs text-muted-foreground">
                    Notify when emails fail to send
                  </p>
                </div>
                <Switch
                  checked={config?.notify_queue_alerts ?? false}
                  onCheckedChange={(c) => setField("notify_queue_alerts", c)}
                />
              </div>
              <Separator />
              <div className="flex items-center justify-between">
                <div className="space-y-0.5">
                  <Label>Security Alerts</Label>
                  <p className="text-xs text-muted-foreground">
                    Notify on suspicious login attempts
                  </p>
                </div>
                <Switch
                  checked={config?.notify_security_alerts ?? false}
                  onCheckedChange={(c) => setField("notify_security_alerts", c)}
                />
              </div>
              <Separator />
              <div className="flex items-center justify-between">
                <div className="space-y-0.5">
                  <Label>Weekly Reports</Label>
                  <p className="text-xs text-muted-foreground">
                    Receive weekly email statistics
                  </p>
                </div>
                <Switch
                  checked={config?.notify_weekly_reports ?? false}
                  onCheckedChange={(c) => setField("notify_weekly_reports", c)}
                />
              </div>
              <div className="flex justify-end">
                <Button onClick={handleSave} disabled={savingConfig || !config}>
                  {savingConfig ? "Saving..." : "Save Changes"}
                </Button>
              </div>
            </CardContent>
          </Card>
        </TabsContent>
      </Tabs>

      <Dialog open={jwtDialogOpen} onOpenChange={setJwtDialogOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Rotate JWT Signing Key</DialogTitle>
            <DialogDescription>
              A new signing key will be generated and used for all new session
              tokens. Existing tokens remain valid until they expire, so signed-in
              users are not logged out.
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="outline" onClick={() => setJwtDialogOpen(false)} disabled={jwtRotating}>
              Cancel
            </Button>
            <Button onClick={handleRotateJWT} disabled={jwtRotating}>
              {jwtRotating ? "Rotating..." : "Rotate Key"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
}
