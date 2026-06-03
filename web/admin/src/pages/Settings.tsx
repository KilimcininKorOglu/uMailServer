import { useState, useEffect, type FormEvent, type ReactNode } from "react";
import {
  Settings,
  Shield,
  Bell,
  Server,
  Network,
  Mail,
  Plug,
  Save,
  AlertCircle,
  CheckCircle2,
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
import { useConfig, useJWT, type JWTStatus, type ConfigUpdateResult } from "@/hooks/useApi";
import type { ServerConfig, ServiceConfig, RateLimitSettings, SMTPConfig } from "@/types";

interface SettingsPageProps {
  userEmail?: string;
  requirePasswordChange?: boolean;
  onPasswordChanged?: () => void;
}

// --- small presentational helpers (keep the per-section forms terse) ---

function TextRow({
  label,
  help,
  value,
  placeholder,
  onChange,
}: {
  label: string;
  help?: string;
  value: string;
  placeholder?: string;
  onChange: (v: string) => void;
}) {
  return (
    <div className="space-y-2">
      <Label>{label}</Label>
      <Input value={value} placeholder={placeholder} onChange={(e) => onChange(e.target.value)} />
      {help && <p className="text-xs text-muted-foreground">{help}</p>}
    </div>
  );
}

function NumberRow({
  label,
  help,
  value,
  onChange,
}: {
  label: string;
  help?: string;
  value: number;
  onChange: (v: number) => void;
}) {
  return (
    <div className="space-y-2">
      <Label>{label}</Label>
      <Input type="number" value={value} onChange={(e) => onChange(parseInt(e.target.value) || 0)} />
      {help && <p className="text-xs text-muted-foreground">{help}</p>}
    </div>
  );
}

function SwitchRow({
  label,
  help,
  checked,
  onChange,
}: {
  label: string;
  help?: string;
  checked: boolean;
  onChange: (v: boolean) => void;
}) {
  return (
    <div className="flex items-center justify-between">
      <div className="space-y-0.5 pr-4">
        <Label>{label}</Label>
        {help && <p className="text-xs text-muted-foreground">{help}</p>}
      </div>
      <Switch checked={checked} onCheckedChange={onChange} />
    </div>
  );
}

function SectionCard({
  title,
  description,
  icon,
  children,
}: {
  title: string;
  description?: string;
  icon?: ReactNode;
  children: ReactNode;
}) {
  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2">
          {icon}
          {title}
        </CardTitle>
        {description && <CardDescription>{description}</CardDescription>}
      </CardHeader>
      <CardContent className="space-y-4">{children}</CardContent>
    </Card>
  );
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
  const [lastResult, setLastResult] = useState<ConfigUpdateResult | null>(null);
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

  // Immutable updaters for the nested config sections.
  const upd = <K extends keyof ServerConfig>(key: K, patch: Partial<ServerConfig[K]>) =>
    setConfig((prev) => (prev ? { ...prev, [key]: { ...prev[key], ...patch } } : prev));
  const updSMTP = <K extends keyof SMTPConfig>(key: K, patch: Partial<SMTPConfig[K]>) =>
    setConfig((prev) =>
      prev ? { ...prev, smtp: { ...prev.smtp, [key]: { ...prev.smtp[key], ...patch } } } : prev,
    );
  const updRateLimit = (patch: Partial<RateLimitSettings>) =>
    setConfig((prev) =>
      prev
        ? { ...prev, security: { ...prev.security, rate_limit: { ...prev.security.rate_limit, ...patch } } }
        : prev,
    );

  const handleSave = async () => {
    if (!config) return;
    setSavingConfig(true);
    setError("");
    try {
      const result = await updateConfig(config);
      setLastResult(result);
      const restart = result.restart_required ?? [];
      if (restart.length > 0) {
        toast.warning(`Settings saved. Restart required for: ${restart.join(", ")}`);
      } else {
        toast.success("Settings saved and applied live");
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

  const saveBar = (
    <div className="flex justify-end">
      <Button onClick={handleSave} disabled={savingConfig || !config}>
        <Save className="mr-2 h-4 w-4" />
        {savingConfig ? "Saving..." : "Save Changes"}
      </Button>
    </div>
  );

  // ServiceCard renders an enable/port/bind form for the uniform protocol
  // services (ManageSieve, CalDAV, CardDAV) backed by a ServiceConfig.
  const ServiceFields = (svc: ServiceConfig, onChange: (p: Partial<ServiceConfig>) => void) => (
    <>
      <SwitchRow
        label="Enabled"
        checked={svc.enabled}
        onChange={(v) => onChange({ enabled: v })}
      />
      <Separator />
      <div className="grid gap-4 sm:grid-cols-2">
        <NumberRow label="Port" value={svc.port} onChange={(v) => onChange({ port: v })} />
        <TextRow label="Bind address" value={svc.bind} placeholder="0.0.0.0" onChange={(v) => onChange({ bind: v })} />
      </div>
    </>
  );

  return (
    <div className="space-y-6">
      <div>
        <h1 className="text-3xl font-bold tracking-tight">Settings</h1>
        <p className="text-muted-foreground mt-1">
          Configure your email server. Most changes apply live; a few structural
          ones (data directory, databases, the HTTP/admin listeners, TLS identity)
          need a restart and are reported after saving.
        </p>
      </div>

      {error && (
        <Alert variant="destructive">
          <AlertCircle className="h-4 w-4" />
          <AlertDescription>{error}</AlertDescription>
        </Alert>
      )}

      {lastResult && (
        <Alert>
          <CheckCircle2 className="h-4 w-4" />
          <AlertDescription>
            {lastResult.applied?.length
              ? `Applied live: ${lastResult.applied.join(", ")}. `
              : "No live changes. "}
            {lastResult.restart_required?.length
              ? `Restart required: ${lastResult.restart_required.join(", ")}.`
              : "No restart required."}
          </AlertDescription>
        </Alert>
      )}

      <Tabs defaultValue="services" className="space-y-6">
        <TabsList>
          <TabsTrigger value="services">
            <Network className="h-4 w-4 mr-2" />
            Services
          </TabsTrigger>
          <TabsTrigger value="general">
            <Settings className="h-4 w-4 mr-2" />
            General
          </TabsTrigger>
          <TabsTrigger value="mail">
            <Mail className="h-4 w-4 mr-2" />
            Mail
          </TabsTrigger>
          <TabsTrigger value="security">
            <Shield className="h-4 w-4 mr-2" />
            Security
          </TabsTrigger>
          <TabsTrigger value="integrations">
            <Plug className="h-4 w-4 mr-2" />
            Integrations
          </TabsTrigger>
          <TabsTrigger value="notifications">
            <Bell className="h-4 w-4 mr-2" />
            Notifications
          </TabsTrigger>
        </TabsList>

        {/* ------------------------------ Services ------------------------------ */}
        <TabsContent value="services" className="space-y-6">
          {config && (
            <>
              <SectionCard
                title="SMTP"
                description="Inbound (25), submission (587), and implicit-TLS submission (465)."
                icon={<Server className="h-5 w-5" />}
              >
                <SwitchRow
                  label="Inbound SMTP (port 25)"
                  help="Accept mail from other servers"
                  checked={config.smtp.inbound.enabled}
                  onChange={(v) => updSMTP("inbound", { enabled: v })}
                />
                <div className="grid gap-4 sm:grid-cols-2">
                  <NumberRow label="Inbound port" value={config.smtp.inbound.port} onChange={(v) => updSMTP("inbound", { port: v })} />
                  <TextRow label="Inbound bind" value={config.smtp.inbound.bind} onChange={(v) => updSMTP("inbound", { bind: v })} />
                </div>
                <Separator />
                <SwitchRow
                  label="Submission (port 587, STARTTLS)"
                  checked={config.smtp.submission.enabled}
                  onChange={(v) => updSMTP("submission", { enabled: v })}
                />
                <div className="grid gap-4 sm:grid-cols-2">
                  <NumberRow label="Submission port" value={config.smtp.submission.port} onChange={(v) => updSMTP("submission", { port: v })} />
                  <TextRow label="Submission bind" value={config.smtp.submission.bind} onChange={(v) => updSMTP("submission", { bind: v })} />
                </div>
                <SwitchRow
                  label="Require TLS for submission"
                  checked={config.smtp.submission.require_tls}
                  onChange={(v) => updSMTP("submission", { require_tls: v })}
                />
                <Separator />
                <SwitchRow
                  label="Implicit-TLS submission (port 465)"
                  checked={config.smtp.submission_tls.enabled}
                  onChange={(v) => updSMTP("submission_tls", { enabled: v })}
                />
                <div className="grid gap-4 sm:grid-cols-2">
                  <NumberRow label="Submission-TLS port" value={config.smtp.submission_tls.port} onChange={(v) => updSMTP("submission_tls", { port: v })} />
                  <TextRow label="Submission-TLS bind" value={config.smtp.submission_tls.bind} onChange={(v) => updSMTP("submission_tls", { bind: v })} />
                </div>
              </SectionCard>

              <SectionCard title="IMAP" icon={<Server className="h-5 w-5" />}>
                <SwitchRow label="Enabled" checked={config.imap.enabled} onChange={(v) => upd("imap", { enabled: v })} />
                <div className="grid gap-4 sm:grid-cols-2">
                  <NumberRow label="Port" value={config.imap.port} onChange={(v) => upd("imap", { port: v })} />
                  <TextRow label="Bind" value={config.imap.bind} onChange={(v) => upd("imap", { bind: v })} />
                  <NumberRow label="STARTTLS port (0 = off)" value={config.imap.starttls_port} onChange={(v) => upd("imap", { starttls_port: v })} />
                  <NumberRow label="Max connections" value={config.imap.max_connections} onChange={(v) => upd("imap", { max_connections: v })} />
                </div>
              </SectionCard>

              <SectionCard title="POP3" icon={<Server className="h-5 w-5" />}>
                <SwitchRow
                  label="Enabled"
                  help="Disabling this stops the POP3 listener immediately — no restart."
                  checked={config.pop3.enabled}
                  onChange={(v) => upd("pop3", { enabled: v })}
                />
                <div className="grid gap-4 sm:grid-cols-2">
                  <NumberRow label="Port" value={config.pop3.port} onChange={(v) => upd("pop3", { port: v })} />
                  <TextRow label="Bind" value={config.pop3.bind} onChange={(v) => upd("pop3", { bind: v })} />
                  <NumberRow label="Max connections" value={config.pop3.max_connections} onChange={(v) => upd("pop3", { max_connections: v })} />
                </div>
              </SectionCard>

              <SectionCard title="ManageSieve" icon={<Server className="h-5 w-5" />}>
                {ServiceFields(config.managesieve, (p) => upd("managesieve", p))}
              </SectionCard>
              <SectionCard title="CalDAV" icon={<Server className="h-5 w-5" />}>
                {ServiceFields(config.caldav, (p) => upd("caldav", p))}
              </SectionCard>
              <SectionCard title="CardDAV" icon={<Server className="h-5 w-5" />}>
                {ServiceFields(config.carddav, (p) => upd("carddav", p))}
              </SectionCard>

              <SectionCard title="JMAP" icon={<Server className="h-5 w-5" />}>
                <SwitchRow label="Enabled" checked={config.jmap.enabled} onChange={(v) => upd("jmap", { enabled: v })} />
                <div className="grid gap-4 sm:grid-cols-2">
                  <NumberRow label="Port" value={config.jmap.port} onChange={(v) => upd("jmap", { port: v })} />
                  <TextRow label="Bind" value={config.jmap.bind} onChange={(v) => upd("jmap", { bind: v })} />
                </div>
              </SectionCard>

              <SectionCard title="MCP server" icon={<Server className="h-5 w-5" />}>
                <SwitchRow label="Enabled" checked={config.mcp.enabled} onChange={(v) => upd("mcp", { enabled: v })} />
                <div className="grid gap-4 sm:grid-cols-2">
                  <NumberRow label="Port" value={config.mcp.port} onChange={(v) => upd("mcp", { port: v })} />
                  <TextRow label="Bind" value={config.mcp.bind} onChange={(v) => upd("mcp", { bind: v })} />
                </div>
              </SectionCard>

              <SectionCard title="Prometheus metrics" icon={<Server className="h-5 w-5" />}>
                <SwitchRow label="Enabled" checked={config.metrics.enabled} onChange={(v) => upd("metrics", { enabled: v })} />
                <div className="grid gap-4 sm:grid-cols-2">
                  <NumberRow label="Port" value={config.metrics.port} onChange={(v) => upd("metrics", { port: v })} />
                  <TextRow label="Bind" value={config.metrics.bind} onChange={(v) => upd("metrics", { bind: v })} />
                  <TextRow label="Path" value={config.metrics.path} placeholder="/metrics" onChange={(v) => upd("metrics", { path: v })} />
                </div>
              </SectionCard>
            </>
          )}
          {saveBar}
        </TabsContent>

        {/* ------------------------------ General ------------------------------ */}
        <TabsContent value="general" className="space-y-6">
          {config && (
            <>
              <SectionCard title="Server" description="Hostname, data directory and shutdown timeouts." icon={<Server className="h-5 w-5" />}>
                <div className="grid gap-4 sm:grid-cols-2">
                  <TextRow label="Hostname" value={config.server.hostname} placeholder="mail.example.com" onChange={(v) => upd("server", { hostname: v })} />
                  <TextRow
                    label="Data directory (restart required)"
                    value={config.server.data_dir}
                    placeholder="/var/lib/umailserver"
                    onChange={(v) => upd("server", { data_dir: v })}
                  />
                  <NumberRow label="Graceful timeout (s)" value={config.server.graceful_timeout_secs} onChange={(v) => upd("server", { graceful_timeout_secs: v })} />
                  <NumberRow label="Force close after (s)" value={config.server.force_close_after_secs} onChange={(v) => upd("server", { force_close_after_secs: v })} />
                </div>
              </SectionCard>

              <SectionCard title="Storage" icon={<Server className="h-5 w-5" />}>
                <SwitchRow label="Sync writes to disk" help="Safer but slower message and queue writes" checked={config.storage.sync} onChange={(v) => upd("storage", { sync: v })} />
                <SwitchRow label="Shared folders" checked={config.storage.shared_folders} onChange={(v) => upd("storage", { shared_folders: v })} />
              </SectionCard>

              <SectionCard title="Logging" icon={<Server className="h-5 w-5" />}>
                <div className="grid gap-4 sm:grid-cols-3">
                  <TextRow label="Level" value={config.logging.level} placeholder="info" onChange={(v) => upd("logging", { level: v })} />
                  <TextRow label="Format" value={config.logging.format} placeholder="json" onChange={(v) => upd("logging", { format: v })} />
                  <TextRow label="Output" value={config.logging.output} placeholder="stdout" onChange={(v) => upd("logging", { output: v })} />
                </div>
                <p className="text-xs text-muted-foreground">Logging changes need a restart to take effect.</p>
              </SectionCard>

              <SectionCard title="Database (restart required)" icon={<Server className="h-5 w-5" />}>
                <TextRow label="Database path" value={config.database.path} onChange={(v) => upd("database", { path: v })} />
              </SectionCard>
            </>
          )}
          {saveBar}
        </TabsContent>

        {/* ------------------------------- Mail -------------------------------- */}
        <TabsContent value="mail" className="space-y-6">
          {config && (
            <>
              <SectionCard title="Spam filtering" icon={<Mail className="h-5 w-5" />}>
                <SwitchRow label="Enable spam filtering" checked={config.spam.enabled} onChange={(v) => upd("spam", { enabled: v })} />
                <div className="grid gap-4 sm:grid-cols-3">
                  <NumberRow label="Reject threshold" value={config.spam.reject_threshold} onChange={(v) => upd("spam", { reject_threshold: v })} />
                  <NumberRow label="Quarantine threshold" value={config.spam.quarantine_threshold} onChange={(v) => upd("spam", { quarantine_threshold: v })} />
                  <NumberRow label="Junk threshold" value={config.spam.junk_threshold} onChange={(v) => upd("spam", { junk_threshold: v })} />
                </div>
                <Separator />
                <SwitchRow label="Greylisting" help="Temporarily reject unknown senders to reduce spam" checked={config.spam.greylisting_enabled} onChange={(v) => upd("spam", { greylisting_enabled: v })} />
                <NumberRow label="Greylist delay (s)" value={config.spam.greylist_delay_secs} onChange={(v) => upd("spam", { greylist_delay_secs: v })} />
                <Separator />
                <SwitchRow label="Bayesian classifier" checked={config.spam.bayesian_enabled} onChange={(v) => upd("spam", { bayesian_enabled: v })} />
                <SwitchRow label="Bayesian auto-train" checked={config.spam.bayesian_auto_train} onChange={(v) => upd("spam", { bayesian_auto_train: v })} />
              </SectionCard>

              <SectionCard title="Antivirus (ClamAV)" icon={<Shield className="h-5 w-5" />}>
                <SwitchRow label="Enable antivirus scanning" checked={config.av.enabled} onChange={(v) => upd("av", { enabled: v })} />
                <div className="grid gap-4 sm:grid-cols-3">
                  <TextRow label="ClamAV address" value={config.av.addr} placeholder="127.0.0.1:3310" onChange={(v) => upd("av", { addr: v })} />
                  <NumberRow label="Timeout (s)" value={config.av.timeout_secs} onChange={(v) => upd("av", { timeout_secs: v })} />
                  <TextRow label="Action" value={config.av.action} placeholder="reject | quarantine | tag" onChange={(v) => upd("av", { action: v })} />
                </div>
              </SectionCard>

              <SectionCard title="DMARC reporting" icon={<Mail className="h-5 w-5" />}>
                <SwitchRow label="Enable DMARC reports" checked={config.dmarc.enabled} onChange={(v) => upd("dmarc", { enabled: v })} />
                <div className="grid gap-4 sm:grid-cols-2">
                  <TextRow label="Organization name" value={config.dmarc.org_name} onChange={(v) => upd("dmarc", { org_name: v })} />
                  <TextRow label="From email" value={config.dmarc.from_email} onChange={(v) => upd("dmarc", { from_email: v })} />
                  <TextRow label="Report email" value={config.dmarc.report_email} onChange={(v) => upd("dmarc", { report_email: v })} />
                  <TextRow label="Interval" value={config.dmarc.interval} placeholder="24h" onChange={(v) => upd("dmarc", { interval: v })} />
                </div>
              </SectionCard>

              <SectionCard title="DKIM signing (restart required)" icon={<KeyRound className="h-5 w-5" />}>
                <SwitchRow
                  label="Sign outgoing mail with DKIM (global)"
                  help="Per-domain DKIM keys (Domains page) are only used when this is on."
                  checked={config.signing.enabled}
                  onChange={(v) => upd("signing", { enabled: v })}
                />
                <TextRow label="Signing key directory" value={config.signing.key_dir} onChange={(v) => upd("signing", { key_dir: v })} />
              </SectionCard>
            </>
          )}
          {saveBar}
        </TabsContent>

        {/* ------------------------------ Security ----------------------------- */}
        <TabsContent value="security" className="space-y-6">
          {config && (
            <>
              <SectionCard title="TLS & ACME (restart required)" icon={<Shield className="h-5 w-5" />}>
                <SwitchRow label="Automatic TLS (ACME / Let's Encrypt)" checked={config.tls.acme.enabled} onChange={(v) => setConfig((p) => (p ? { ...p, tls: { ...p.tls, acme: { ...p.tls.acme, enabled: v } } } : p))} />
                <div className="grid gap-4 sm:grid-cols-2">
                  <TextRow label="ACME email" value={config.tls.acme.email} onChange={(v) => setConfig((p) => (p ? { ...p, tls: { ...p.tls, acme: { ...p.tls.acme, email: v } } } : p))} />
                  <TextRow label="ACME provider" value={config.tls.acme.provider} placeholder="letsencrypt" onChange={(v) => setConfig((p) => (p ? { ...p, tls: { ...p.tls, acme: { ...p.tls.acme, provider: v } } } : p))} />
                  <TextRow label="Certificate file" value={config.tls.cert_file} onChange={(v) => upd("tls", { cert_file: v })} />
                  <TextRow label="Key file" value={config.tls.key_file} onChange={(v) => upd("tls", { key_file: v })} />
                  <TextRow label="Minimum TLS version" value={config.tls.min_version} placeholder="1.2 | 1.3" onChange={(v) => upd("tls", { min_version: v })} />
                </div>
              </SectionCard>

              <SectionCard title="Authentication limits" icon={<Shield className="h-5 w-5" />}>
                <div className="grid gap-4 sm:grid-cols-2">
                  <NumberRow label="Max login attempts" value={config.security.max_login_attempts} onChange={(v) => upd("security", { max_login_attempts: v })} />
                  <NumberRow label="Lockout duration (s)" value={config.security.lockout_secs} onChange={(v) => upd("security", { lockout_secs: v })} />
                  <NumberRow label="SPF cache TTL (s)" value={config.security.spf_cache_ttl_secs} onChange={(v) => upd("security", { spf_cache_ttl_secs: v })} />
                </div>
              </SectionCard>

              <SectionCard title="Rate limiting" description="Applied live to the global limiter." icon={<Shield className="h-5 w-5" />}>
                <div className="grid gap-4 sm:grid-cols-2">
                  <NumberRow label="Messages per user / hour" value={config.security.rate_limit.user_per_hour} onChange={(v) => updRateLimit({ user_per_hour: v })} />
                  <NumberRow label="Messages per user / day" value={config.security.rate_limit.user_per_day} onChange={(v) => updRateLimit({ user_per_day: v })} />
                  <NumberRow label="Max recipients per message" value={config.security.rate_limit.user_max_recipients} onChange={(v) => updRateLimit({ user_max_recipients: v })} />
                  <NumberRow label="Connections per IP" value={config.security.rate_limit.ip_connections} onChange={(v) => updRateLimit({ ip_connections: v })} />
                  <NumberRow label="Global messages / minute" value={config.security.rate_limit.global_per_minute} onChange={(v) => updRateLimit({ global_per_minute: v })} />
                  <NumberRow label="HTTP requests / minute" value={config.security.rate_limit.http_requests_per_minute} onChange={(v) => updRateLimit({ http_requests_per_minute: v })} />
                </div>
              </SectionCard>

              <SectionCard title="JWT signing key" description="Rotate the secret used to sign session tokens. Existing tokens stay valid until they expire." icon={<KeyRound className="h-5 w-5" />}>
                <div className="flex items-center justify-between">
                  <div className="space-y-0.5">
                    <Label>Active signing keys</Label>
                    <p className="text-xs text-muted-foreground">
                      {jwtStatus ? `${jwtStatus.activeKeys} active key(s); current: ${jwtStatus.currentKid}` : "Loading key status..."}
                    </p>
                  </div>
                  <Button variant="outline" onClick={() => setJwtDialogOpen(true)} disabled={jwtRotating}>
                    <RefreshCw className={`mr-2 h-4 w-4 ${jwtRotating ? "animate-spin" : ""}`} />
                    {jwtRotating ? "Rotating..." : "Rotate Key"}
                  </Button>
                </div>
              </SectionCard>
            </>
          )}
          {saveBar}
        </TabsContent>

        {/* ---------------------------- Integrations --------------------------- */}
        <TabsContent value="integrations" className="space-y-6">
          {config && (
            <>
              <SectionCard title="LDAP authentication (restart required)" icon={<Plug className="h-5 w-5" />}>
                <SwitchRow label="Enable LDAP" help="The bind password stays in YAML and is never shown here." checked={config.ldap.enabled} onChange={(v) => upd("ldap", { enabled: v })} />
                <div className="grid gap-4 sm:grid-cols-2">
                  <TextRow label="URL" value={config.ldap.url} placeholder="ldaps://ldap.example.com" onChange={(v) => upd("ldap", { url: v })} />
                  <TextRow label="Bind DN" value={config.ldap.bind_dn} onChange={(v) => upd("ldap", { bind_dn: v })} />
                  <TextRow label="Base DN" value={config.ldap.base_dn} onChange={(v) => upd("ldap", { base_dn: v })} />
                  <TextRow label="User filter" value={config.ldap.user_filter} onChange={(v) => upd("ldap", { user_filter: v })} />
                </div>
                <SwitchRow label="StartTLS" checked={config.ldap.start_tls} onChange={(v) => upd("ldap", { start_tls: v })} />
              </SectionCard>

              <SectionCard title="Alerting (restart required)" icon={<Bell className="h-5 w-5" />}>
                <SwitchRow label="Enable alerts" help="SMTP password and webhook headers stay in YAML." checked={config.alert.enabled} onChange={(v) => upd("alert", { enabled: v })} />
                <div className="grid gap-4 sm:grid-cols-2">
                  <TextRow label="Webhook URL" value={config.alert.webhook_url} onChange={(v) => upd("alert", { webhook_url: v })} />
                  <TextRow label="SMTP server" value={config.alert.smtp_server} onChange={(v) => upd("alert", { smtp_server: v })} />
                  <TextRow label="From address" value={config.alert.from_address} onChange={(v) => upd("alert", { from_address: v })} />
                  <NumberRow label="Queue threshold" value={config.alert.queue_threshold} onChange={(v) => upd("alert", { queue_threshold: v })} />
                </div>
              </SectionCard>

              <SectionCard title="Web push (restart required)" icon={<Bell className="h-5 w-5" />}>
                <SwitchRow label="Enable web push" help="The VAPID private key stays in YAML." checked={config.push.enabled} onChange={(v) => upd("push", { enabled: v })} />
                <TextRow label="Subject (mailto: or https:// URL)" value={config.push.subject} onChange={(v) => upd("push", { subject: v })} />
              </SectionCard>

              <SectionCard title="Tracing (restart required)" icon={<Plug className="h-5 w-5" />}>
                <SwitchRow label="Enable distributed tracing" checked={config.tracing.enabled} onChange={(v) => upd("tracing", { enabled: v })} />
                <div className="grid gap-4 sm:grid-cols-2">
                  <TextRow label="Exporter" value={config.tracing.exporter} placeholder="otlp | stdout | noop" onChange={(v) => upd("tracing", { exporter: v })} />
                  <TextRow label="OTLP endpoint" value={config.tracing.otlp_endpoint} onChange={(v) => upd("tracing", { otlp_endpoint: v })} />
                </div>
              </SectionCard>
            </>
          )}
          {saveBar}
        </TabsContent>

        {/* ---------------------------- Notifications -------------------------- */}
        <TabsContent value="notifications" className="space-y-6">
          {config && (
            <>
              <SectionCard title="Out-of-office defaults" icon={<Bell className="h-5 w-5" />}>
                <SwitchRow label="Default enabled" checked={config.oof.default_enabled} onChange={(v) => upd("oof", { default_enabled: v })} />
                <SwitchRow label="Internal senders only" checked={config.oof.internal_only} onChange={(v) => upd("oof", { internal_only: v })} />
                <TextRow label="Default subject" value={config.oof.default_subject} onChange={(v) => upd("oof", { default_subject: v })} />
                <div className="space-y-2">
                  <Label>Default message</Label>
                  <Input value={config.oof.default_message} onChange={(e) => upd("oof", { default_message: e.target.value })} />
                </div>
              </SectionCard>

              <SectionCard title="Notification preferences" icon={<Bell className="h-5 w-5" />}>
                <SwitchRow label="Queue alerts" help="Notify when emails fail to send" checked={config.notifications.queue_alerts} onChange={(v) => upd("notifications", { queue_alerts: v })} />
                <SwitchRow label="Security alerts" help="Notify on suspicious login attempts" checked={config.notifications.security_alerts} onChange={(v) => upd("notifications", { security_alerts: v })} />
                <SwitchRow label="Weekly reports" help="Receive weekly email statistics" checked={config.notifications.weekly_reports} onChange={(v) => upd("notifications", { weekly_reports: v })} />
              </SectionCard>
            </>
          )}
          {saveBar}
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
