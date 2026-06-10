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
import { useI18n } from "@/hooks/useI18n";
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
  const { t } = useI18n();
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
      fetchConfig().catch(() => setError(t("settings.loadConfigFailed")));
      fetchJWTStatus()
        .then(setJwtStatus)
        .catch(() => undefined);
    }
  }, [requirePasswordChange, fetchConfig, fetchJWTStatus, t]);

  const handleRotateJWT = async () => {
    setJwtRotating(true);
    try {
      const result = await rotateJWT();
      toast.success(result.message || t("settings.jwtRotated"));
      const status = await fetchJWTStatus();
      setJwtStatus(status);
    } catch (err) {
      toast.error((err as { message?: string }).message || t("settings.jwtRotateFailed"));
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
        toast.warning(t("settings.savedRestartRequired", { items: restart.join(", ") }));
      } else {
        toast.success(t("settings.savedAppliedLive"));
      }
    } catch (err) {
      toast.error((err as { message?: string }).message || t("settings.saveFailed"));
    } finally {
      setSavingConfig(false);
    }
  };

  const handleRequiredPasswordChange = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    setError("");

    if (!userEmail) {
      setError(t("settings.cannotDetermineAdmin"));
      return;
    }
    if (newPassword !== confirmPassword) {
      setError(t("settings.passwordsNoMatch"));
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
        throw new Error(data?.error || t("settings.passwordChangeFailed"));
      }

      await fetch("/api/v1/auth/logout", {
        method: "POST",
        credentials: "include",
      }).catch(() => undefined);

      onPasswordChanged?.();
    } catch (err) {
      setError(err instanceof Error ? err.message : t("settings.passwordChangeFailed"));
    } finally {
      setPasswordSaving(false);
    }
  };

  if (requirePasswordChange) {
    return (
      <div className="space-y-6 max-w-2xl">
        <div>
          <h1 className="text-3xl font-bold tracking-tight">{t("settings.changeAdminPassword")}</h1>
          <p className="text-muted-foreground mt-1">
            {t("settings.bootstrapPasswordDescription")}
          </p>
        </div>

        <Alert variant="destructive">
          <AlertCircle className="h-4 w-4" />
          <AlertDescription>
            {t("settings.bootstrapPasswordAlert")}
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
            <CardTitle>{t("settings.setNewPasswordFor", { email: userEmail })}</CardTitle>
            <CardDescription>
              {t("settings.passwordRequirements")}
            </CardDescription>
          </CardHeader>
          <CardContent>
            <form onSubmit={handleRequiredPasswordChange} className="space-y-4">
              <div className="space-y-2">
                <Label htmlFor="new-password">{t("settings.newPassword")}</Label>
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
                <Label htmlFor="confirm-password">{t("settings.confirmNewPassword")}</Label>
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
                  {passwordSaving ? t("settings.updating") : t("settings.updatePassword")}
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
        {savingConfig ? t("common.saving") : t("common.saveChanges")}
      </Button>
    </div>
  );

  // ServiceCard renders an enable/port/bind form for the uniform protocol
  // services (ManageSieve, CalDAV, CardDAV) backed by a ServiceConfig.
  const ServiceFields = (svc: ServiceConfig, onChange: (p: Partial<ServiceConfig>) => void) => (
    <>
      <SwitchRow
        label={t("common.enabled")}
        checked={svc.enabled}
        onChange={(v) => onChange({ enabled: v })}
      />
      <Separator />
      <div className="grid gap-4 sm:grid-cols-2">
        <NumberRow label={t("settings.port")} value={svc.port} onChange={(v) => onChange({ port: v })} />
        <TextRow label={t("settings.bindAddress")} value={svc.bind} placeholder="0.0.0.0" onChange={(v) => onChange({ bind: v })} />
      </div>
    </>
  );

  return (
    <div className="space-y-6">
      <div>
        <h1 className="text-3xl font-bold tracking-tight">{t("settings.title")}</h1>
        <p className="text-muted-foreground mt-1">
          {t("settings.pageDescription")}
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
              ? `${t("settings.appliedLive", { items: lastResult.applied.join(", ") })} `
              : `${t("settings.noLiveChanges")} `}
            {lastResult.restart_required?.length
              ? t("settings.restartRequiredList", { items: lastResult.restart_required.join(", ") })
              : t("settings.noRestartRequired")}
          </AlertDescription>
        </Alert>
      )}

      <Tabs defaultValue="services" className="space-y-6">
        <TabsList>
          <TabsTrigger value="services">
            <Network className="h-4 w-4 mr-2" />
            {t("settings.tabServices")}
          </TabsTrigger>
          <TabsTrigger value="general">
            <Settings className="h-4 w-4 mr-2" />
            {t("settings.general")}
          </TabsTrigger>
          <TabsTrigger value="mail">
            <Mail className="h-4 w-4 mr-2" />
            {t("settings.tabMail")}
          </TabsTrigger>
          <TabsTrigger value="security">
            <Shield className="h-4 w-4 mr-2" />
            {t("settings.security")}
          </TabsTrigger>
          <TabsTrigger value="integrations">
            <Plug className="h-4 w-4 mr-2" />
            {t("settings.tabIntegrations")}
          </TabsTrigger>
          <TabsTrigger value="notifications">
            <Bell className="h-4 w-4 mr-2" />
            {t("settings.tabNotifications")}
          </TabsTrigger>
        </TabsList>

        {/* ------------------------------ Services ------------------------------ */}
        <TabsContent value="services" className="space-y-6">
          {config && (
            <>
              <SectionCard
                title={t("settings.smtp")}
                description={t("settings.smtpDescription")}
                icon={<Server className="h-5 w-5" />}
              >
                <SwitchRow
                  label={t("settings.inboundSmtp")}
                  help={t("settings.inboundSmtpHelp")}
                  checked={config.smtp.inbound.enabled}
                  onChange={(v) => updSMTP("inbound", { enabled: v })}
                />
                <div className="grid gap-4 sm:grid-cols-2">
                  <NumberRow label={t("settings.inboundPort")} value={config.smtp.inbound.port} onChange={(v) => updSMTP("inbound", { port: v })} />
                  <TextRow label={t("settings.inboundBind")} value={config.smtp.inbound.bind} onChange={(v) => updSMTP("inbound", { bind: v })} />
                </div>
                <Separator />
                <SwitchRow
                  label={t("settings.submission587")}
                  checked={config.smtp.submission.enabled}
                  onChange={(v) => updSMTP("submission", { enabled: v })}
                />
                <div className="grid gap-4 sm:grid-cols-2">
                  <NumberRow label={t("settings.submissionPort")} value={config.smtp.submission.port} onChange={(v) => updSMTP("submission", { port: v })} />
                  <TextRow label={t("settings.submissionBind")} value={config.smtp.submission.bind} onChange={(v) => updSMTP("submission", { bind: v })} />
                </div>
                <SwitchRow
                  label={t("settings.requireTlsSubmission")}
                  checked={config.smtp.submission.require_tls}
                  onChange={(v) => updSMTP("submission", { require_tls: v })}
                />
                <Separator />
                <SwitchRow
                  label={t("settings.submissionTls465")}
                  checked={config.smtp.submission_tls.enabled}
                  onChange={(v) => updSMTP("submission_tls", { enabled: v })}
                />
                <div className="grid gap-4 sm:grid-cols-2">
                  <NumberRow label={t("settings.submissionTlsPort")} value={config.smtp.submission_tls.port} onChange={(v) => updSMTP("submission_tls", { port: v })} />
                  <TextRow label={t("settings.submissionTlsBind")} value={config.smtp.submission_tls.bind} onChange={(v) => updSMTP("submission_tls", { bind: v })} />
                </div>
                <Separator />
                <SwitchRow
                  label={t("settings.lmtp")}
                  help={t("settings.lmtpHelp")}
                  checked={config.smtp.lmtp.enabled}
                  onChange={(v) => updSMTP("lmtp", { enabled: v })}
                />
                <div className="grid gap-4 sm:grid-cols-2">
                  <NumberRow label={t("settings.lmtpPort")} value={config.smtp.lmtp.port} onChange={(v) => updSMTP("lmtp", { port: v })} />
                  <TextRow label={t("settings.lmtpBind")} value={config.smtp.lmtp.bind} onChange={(v) => updSMTP("lmtp", { bind: v })} />
                </div>
              </SectionCard>

              <SectionCard title={t("settings.imap")} icon={<Server className="h-5 w-5" />}>
                <SwitchRow label={t("common.enabled")} checked={config.imap.enabled} onChange={(v) => upd("imap", { enabled: v })} />
                <div className="grid gap-4 sm:grid-cols-2">
                  <NumberRow label={t("settings.port")} value={config.imap.port} onChange={(v) => upd("imap", { port: v })} />
                  <TextRow label={t("settings.bind")} value={config.imap.bind} onChange={(v) => upd("imap", { bind: v })} />
                  <NumberRow label={t("settings.starttlsPort")} value={config.imap.starttls_port} onChange={(v) => upd("imap", { starttls_port: v })} />
                  <NumberRow label={t("settings.maxConnections")} value={config.imap.max_connections} onChange={(v) => upd("imap", { max_connections: v })} />
                </div>
              </SectionCard>

              <SectionCard title={t("settings.pop3")} icon={<Server className="h-5 w-5" />}>
                <SwitchRow
                  label={t("common.enabled")}
                  help={t("settings.pop3DisableHelp")}
                  checked={config.pop3.enabled}
                  onChange={(v) => upd("pop3", { enabled: v })}
                />
                <div className="grid gap-4 sm:grid-cols-2">
                  <NumberRow label={t("settings.port")} value={config.pop3.port} onChange={(v) => upd("pop3", { port: v })} />
                  <TextRow label={t("settings.bind")} value={config.pop3.bind} onChange={(v) => upd("pop3", { bind: v })} />
                  <NumberRow label={t("settings.maxConnections")} value={config.pop3.max_connections} onChange={(v) => upd("pop3", { max_connections: v })} />
                </div>
              </SectionCard>

              <SectionCard title={t("settings.managesieve")} icon={<Server className="h-5 w-5" />}>
                {ServiceFields(config.managesieve, (p) => upd("managesieve", p))}
              </SectionCard>
              <SectionCard title={t("settings.caldav")} icon={<Server className="h-5 w-5" />}>
                {ServiceFields(config.caldav, (p) => upd("caldav", p))}
              </SectionCard>
              <SectionCard title={t("settings.carddav")} icon={<Server className="h-5 w-5" />}>
                {ServiceFields(config.carddav, (p) => upd("carddav", p))}
              </SectionCard>

              <SectionCard title={t("settings.jmap")} icon={<Server className="h-5 w-5" />}>
                <SwitchRow label={t("common.enabled")} checked={config.jmap.enabled} onChange={(v) => upd("jmap", { enabled: v })} />
                <div className="grid gap-4 sm:grid-cols-2">
                  <NumberRow label={t("settings.port")} value={config.jmap.port} onChange={(v) => upd("jmap", { port: v })} />
                  <TextRow label={t("settings.bind")} value={config.jmap.bind} onChange={(v) => upd("jmap", { bind: v })} />
                </div>
              </SectionCard>

              <SectionCard title={t("settings.mcpServer")} icon={<Server className="h-5 w-5" />}>
                <SwitchRow label={t("common.enabled")} checked={config.mcp.enabled} onChange={(v) => upd("mcp", { enabled: v })} />
                <div className="grid gap-4 sm:grid-cols-2">
                  <NumberRow label={t("settings.port")} value={config.mcp.port} onChange={(v) => upd("mcp", { port: v })} />
                  <TextRow label={t("settings.bind")} value={config.mcp.bind} onChange={(v) => upd("mcp", { bind: v })} />
                </div>
              </SectionCard>

              <SectionCard title={t("settings.prometheusMetrics")} icon={<Server className="h-5 w-5" />}>
                <SwitchRow label={t("common.enabled")} checked={config.metrics.enabled} onChange={(v) => upd("metrics", { enabled: v })} />
                <div className="grid gap-4 sm:grid-cols-2">
                  <NumberRow label={t("settings.port")} value={config.metrics.port} onChange={(v) => upd("metrics", { port: v })} />
                  <TextRow label={t("settings.bind")} value={config.metrics.bind} onChange={(v) => upd("metrics", { bind: v })} />
                  <TextRow label={t("settings.path")} value={config.metrics.path} placeholder="/metrics" onChange={(v) => upd("metrics", { path: v })} />
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
              <SectionCard title={t("settings.server")} description={t("settings.serverDescription")} icon={<Server className="h-5 w-5" />}>
                <div className="grid gap-4 sm:grid-cols-2">
                  <TextRow label={t("settings.hostname")} value={config.server.hostname} placeholder="mail.example.com" onChange={(v) => upd("server", { hostname: v })} />
                  <TextRow
                    label={`${t("settings.dataDirectory")} (${t("settings.restartRequired")})`}
                    value={config.server.data_dir}
                    placeholder="/var/lib/umailserver"
                    onChange={(v) => upd("server", { data_dir: v })}
                  />
                  <NumberRow label={t("settings.gracefulTimeout")} value={config.server.graceful_timeout_secs} onChange={(v) => upd("server", { graceful_timeout_secs: v })} />
                  <NumberRow label={t("settings.forceCloseAfter")} value={config.server.force_close_after_secs} onChange={(v) => upd("server", { force_close_after_secs: v })} />
                </div>
              </SectionCard>

              <SectionCard title={t("settings.storage")} icon={<Server className="h-5 w-5" />}>
                <SwitchRow label={t("settings.syncWrites")} help={t("settings.syncWritesHelp")} checked={config.storage.sync} onChange={(v) => upd("storage", { sync: v })} />
                <SwitchRow label={t("settings.sharedFolders")} checked={config.storage.shared_folders} onChange={(v) => upd("storage", { shared_folders: v })} />
              </SectionCard>

              <SectionCard title={t("settings.logging")} icon={<Server className="h-5 w-5" />}>
                <div className="grid gap-4 sm:grid-cols-3">
                  <TextRow label={t("settings.level")} value={config.logging.level} placeholder="info" onChange={(v) => upd("logging", { level: v })} />
                  <TextRow label={t("settings.format")} value={config.logging.format} placeholder="json" onChange={(v) => upd("logging", { format: v })} />
                  <TextRow label={t("settings.output")} value={config.logging.output} placeholder="stdout" onChange={(v) => upd("logging", { output: v })} />
                </div>
                <p className="text-xs text-muted-foreground">{t("settings.loggingRestartNote")}</p>
              </SectionCard>

              <SectionCard title={`${t("settings.database")} (${t("settings.restartRequired")})`} icon={<Server className="h-5 w-5" />}>
                <TextRow label={t("settings.databasePath")} value={config.database.path} onChange={(v) => upd("database", { path: v })} />
              </SectionCard>
            </>
          )}
          {saveBar}
        </TabsContent>

        {/* ------------------------------- Mail -------------------------------- */}
        <TabsContent value="mail" className="space-y-6">
          {config && (
            <>
              <SectionCard title={t("settings.spamFiltering")} icon={<Mail className="h-5 w-5" />}>
                <SwitchRow label={t("settings.enableSpamFiltering")} checked={config.spam.enabled} onChange={(v) => upd("spam", { enabled: v })} />
                <div className="grid gap-4 sm:grid-cols-3">
                  <NumberRow label={t("settings.rejectThreshold")} value={config.spam.reject_threshold} onChange={(v) => upd("spam", { reject_threshold: v })} />
                  <NumberRow label={t("settings.quarantineThreshold")} value={config.spam.quarantine_threshold} onChange={(v) => upd("spam", { quarantine_threshold: v })} />
                  <NumberRow label={t("settings.junkThreshold")} value={config.spam.junk_threshold} onChange={(v) => upd("spam", { junk_threshold: v })} />
                </div>
                <Separator />
                <SwitchRow label={t("settings.greylisting")} help={t("settings.greylistingHelp")} checked={config.spam.greylisting_enabled} onChange={(v) => upd("spam", { greylisting_enabled: v })} />
                <NumberRow label={t("settings.greylistDelay")} value={config.spam.greylist_delay_secs} onChange={(v) => upd("spam", { greylist_delay_secs: v })} />
                <Separator />
                <SwitchRow label={t("settings.bayesianClassifier")} checked={config.spam.bayesian_enabled} onChange={(v) => upd("spam", { bayesian_enabled: v })} />
                <SwitchRow label={t("settings.bayesianAutoTrain")} checked={config.spam.bayesian_auto_train} onChange={(v) => upd("spam", { bayesian_auto_train: v })} />
              </SectionCard>

              <SectionCard title={t("settings.antivirusClamav")} icon={<Shield className="h-5 w-5" />}>
                <SwitchRow label={t("settings.enableAntivirus")} checked={config.av.enabled} onChange={(v) => upd("av", { enabled: v })} />
                <div className="grid gap-4 sm:grid-cols-3">
                  <TextRow label={t("settings.clamavAddress")} value={config.av.addr} placeholder="127.0.0.1:3310" onChange={(v) => upd("av", { addr: v })} />
                  <NumberRow label={t("settings.timeout")} value={config.av.timeout_secs} onChange={(v) => upd("av", { timeout_secs: v })} />
                  <TextRow label={t("settings.action")} value={config.av.action} placeholder="reject | quarantine | tag" onChange={(v) => upd("av", { action: v })} />
                </div>
              </SectionCard>

              <SectionCard title={t("settings.dmarcReporting")} icon={<Mail className="h-5 w-5" />}>
                <SwitchRow label={t("settings.enableDmarcReports")} checked={config.dmarc.enabled} onChange={(v) => upd("dmarc", { enabled: v })} />
                <div className="grid gap-4 sm:grid-cols-2">
                  <TextRow label={t("settings.organizationName")} value={config.dmarc.org_name} onChange={(v) => upd("dmarc", { org_name: v })} />
                  <TextRow label={t("settings.fromEmail")} value={config.dmarc.from_email} onChange={(v) => upd("dmarc", { from_email: v })} />
                  <TextRow label={t("settings.reportEmail")} value={config.dmarc.report_email} onChange={(v) => upd("dmarc", { report_email: v })} />
                  <TextRow label={t("settings.interval")} value={config.dmarc.interval} placeholder="24h" onChange={(v) => upd("dmarc", { interval: v })} />
                </div>
              </SectionCard>

              <SectionCard title={`${t("settings.dkimSigning")} (${t("settings.restartRequired")})`} icon={<KeyRound className="h-5 w-5" />}>
                <SwitchRow
                  label={t("settings.signDkimGlobal")}
                  help={t("settings.dkimGlobalHelp")}
                  checked={config.signing.enabled}
                  onChange={(v) => upd("signing", { enabled: v })}
                />
                <TextRow label={t("settings.signingKeyDir")} value={config.signing.key_dir} onChange={(v) => upd("signing", { key_dir: v })} />
              </SectionCard>
            </>
          )}
          {saveBar}
        </TabsContent>

        {/* ------------------------------ Security ----------------------------- */}
        <TabsContent value="security" className="space-y-6">
          {config && (
            <>
              <SectionCard title={`${t("settings.tlsAcme")} (${t("settings.restartRequired")})`} icon={<Shield className="h-5 w-5" />}>
                <SwitchRow label={t("settings.automaticTls")} checked={config.tls.acme.enabled} onChange={(v) => setConfig((p) => (p ? { ...p, tls: { ...p.tls, acme: { ...p.tls.acme, enabled: v } } } : p))} />
                <div className="grid gap-4 sm:grid-cols-2">
                  <TextRow label={t("settings.acmeEmail")} value={config.tls.acme.email} onChange={(v) => setConfig((p) => (p ? { ...p, tls: { ...p.tls, acme: { ...p.tls.acme, email: v } } } : p))} />
                  <TextRow label={t("settings.acmeProvider")} value={config.tls.acme.provider} placeholder="letsencrypt" onChange={(v) => setConfig((p) => (p ? { ...p, tls: { ...p.tls, acme: { ...p.tls.acme, provider: v } } } : p))} />
                  <TextRow label={t("settings.certificateFile")} value={config.tls.cert_file} onChange={(v) => upd("tls", { cert_file: v })} />
                  <TextRow label={t("settings.keyFile")} value={config.tls.key_file} onChange={(v) => upd("tls", { key_file: v })} />
                  <TextRow label={t("settings.minTlsVersion")} value={config.tls.min_version} placeholder="1.2 | 1.3" onChange={(v) => upd("tls", { min_version: v })} />
                </div>
              </SectionCard>

              <SectionCard title={t("settings.authLimits")} icon={<Shield className="h-5 w-5" />}>
                <div className="grid gap-4 sm:grid-cols-2">
                  <NumberRow label={t("settings.maxLoginAttempts")} value={config.security.max_login_attempts} onChange={(v) => upd("security", { max_login_attempts: v })} />
                  <NumberRow label={t("settings.lockoutDuration")} value={config.security.lockout_secs} onChange={(v) => upd("security", { lockout_secs: v })} />
                  <NumberRow label={t("settings.spfCacheTtl")} value={config.security.spf_cache_ttl_secs} onChange={(v) => upd("security", { spf_cache_ttl_secs: v })} />
                </div>
              </SectionCard>

              <SectionCard title={t("settings.rateLimiting")} description={t("settings.rateLimitingDescription")} icon={<Shield className="h-5 w-5" />}>
                <div className="grid gap-4 sm:grid-cols-2">
                  <NumberRow label={t("settings.messagesPerUserHour")} value={config.security.rate_limit.user_per_hour} onChange={(v) => updRateLimit({ user_per_hour: v })} />
                  <NumberRow label={t("settings.messagesPerUserDay")} value={config.security.rate_limit.user_per_day} onChange={(v) => updRateLimit({ user_per_day: v })} />
                  <NumberRow label={t("settings.maxRecipientsPerMessage")} value={config.security.rate_limit.user_max_recipients} onChange={(v) => updRateLimit({ user_max_recipients: v })} />
                  <NumberRow label={t("settings.connectionsPerIp")} value={config.security.rate_limit.ip_connections} onChange={(v) => updRateLimit({ ip_connections: v })} />
                  <NumberRow label={t("settings.globalMessagesMinute")} value={config.security.rate_limit.global_per_minute} onChange={(v) => updRateLimit({ global_per_minute: v })} />
                  <NumberRow label={t("settings.httpRequestsMinute")} value={config.security.rate_limit.http_requests_per_minute} onChange={(v) => updRateLimit({ http_requests_per_minute: v })} />
                </div>
              </SectionCard>

              <SectionCard title={t("settings.jwtSigningKey")} description={t("settings.jwtSigningKeyDescription")} icon={<KeyRound className="h-5 w-5" />}>
                <div className="flex items-center justify-between">
                  <div className="space-y-0.5">
                    <Label>{t("settings.activeSigningKeys")}</Label>
                    <p className="text-xs text-muted-foreground">
                      {jwtStatus ? t("settings.jwtKeyStatus", { count: String(jwtStatus.activeKeys), kid: jwtStatus.currentKid }) : t("settings.loadingKeyStatus")}
                    </p>
                  </div>
                  <Button variant="outline" onClick={() => setJwtDialogOpen(true)} disabled={jwtRotating}>
                    <RefreshCw className={`mr-2 h-4 w-4 ${jwtRotating ? "animate-spin" : ""}`} />
                    {jwtRotating ? t("settings.rotating") : t("settings.rotateKey")}
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
              <SectionCard title={`${t("settings.ldapAuth")} (${t("settings.restartRequired")})`} icon={<Plug className="h-5 w-5" />}>
                <SwitchRow label={t("settings.enableLdap")} help={t("settings.ldapBindPasswordHelp")} checked={config.ldap.enabled} onChange={(v) => upd("ldap", { enabled: v })} />
                <div className="grid gap-4 sm:grid-cols-2">
                  <TextRow label={t("settings.url")} value={config.ldap.url} placeholder="ldaps://ldap.example.com" onChange={(v) => upd("ldap", { url: v })} />
                  <TextRow label={t("settings.bindDn")} value={config.ldap.bind_dn} onChange={(v) => upd("ldap", { bind_dn: v })} />
                  <TextRow label={t("settings.baseDn")} value={config.ldap.base_dn} onChange={(v) => upd("ldap", { base_dn: v })} />
                  <TextRow label={t("settings.userFilter")} value={config.ldap.user_filter} onChange={(v) => upd("ldap", { user_filter: v })} />
                </div>
                <SwitchRow label={t("settings.startTls")} checked={config.ldap.start_tls} onChange={(v) => upd("ldap", { start_tls: v })} />
              </SectionCard>

              <SectionCard title={`${t("settings.alerting")} (${t("settings.restartRequired")})`} icon={<Bell className="h-5 w-5" />}>
                <SwitchRow label={t("settings.enableAlerts")} help={t("settings.alertSecretsHelp")} checked={config.alert.enabled} onChange={(v) => upd("alert", { enabled: v })} />
                <div className="grid gap-4 sm:grid-cols-2">
                  <TextRow label={t("settings.webhookUrl")} value={config.alert.webhook_url} onChange={(v) => upd("alert", { webhook_url: v })} />
                  <TextRow label={t("settings.smtpServer")} value={config.alert.smtp_server} onChange={(v) => upd("alert", { smtp_server: v })} />
                  <TextRow label={t("settings.fromAddress")} value={config.alert.from_address} onChange={(v) => upd("alert", { from_address: v })} />
                  <NumberRow label={t("settings.queueThreshold")} value={config.alert.queue_threshold} onChange={(v) => upd("alert", { queue_threshold: v })} />
                </div>
              </SectionCard>

              <SectionCard title={`${t("settings.webPush")} (${t("settings.restartRequired")})`} icon={<Bell className="h-5 w-5" />}>
                <SwitchRow label={t("settings.enableWebPush")} help={t("settings.vapidHelp")} checked={config.push.enabled} onChange={(v) => upd("push", { enabled: v })} />
                <TextRow label={t("settings.pushSubject")} value={config.push.subject} onChange={(v) => upd("push", { subject: v })} />
              </SectionCard>

              <SectionCard title={`${t("settings.tracing")} (${t("settings.restartRequired")})`} icon={<Plug className="h-5 w-5" />}>
                <SwitchRow label={t("settings.enableTracing")} checked={config.tracing.enabled} onChange={(v) => upd("tracing", { enabled: v })} />
                <div className="grid gap-4 sm:grid-cols-2">
                  <TextRow label={t("settings.exporter")} value={config.tracing.exporter} placeholder="otlp | stdout | noop" onChange={(v) => upd("tracing", { exporter: v })} />
                  <TextRow label={t("settings.otlpEndpoint")} value={config.tracing.otlp_endpoint} onChange={(v) => upd("tracing", { otlp_endpoint: v })} />
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
              <SectionCard title={t("settings.oofDefaults")} icon={<Bell className="h-5 w-5" />}>
                <SwitchRow label={t("settings.defaultEnabled")} checked={config.oof.default_enabled} onChange={(v) => upd("oof", { default_enabled: v })} />
                <SwitchRow label={t("settings.internalSendersOnly")} checked={config.oof.internal_only} onChange={(v) => upd("oof", { internal_only: v })} />
                <TextRow label={t("settings.defaultSubject")} value={config.oof.default_subject} onChange={(v) => upd("oof", { default_subject: v })} />
                <div className="space-y-2">
                  <Label>{t("settings.defaultMessage")}</Label>
                  <Input value={config.oof.default_message} onChange={(e) => upd("oof", { default_message: e.target.value })} />
                </div>
              </SectionCard>

              <SectionCard title={t("settings.notificationPreferences")} icon={<Bell className="h-5 w-5" />}>
                <SwitchRow label={t("settings.queueAlerts")} help={t("settings.queueAlertsHelp")} checked={config.notifications.queue_alerts} onChange={(v) => upd("notifications", { queue_alerts: v })} />
                <SwitchRow label={t("settings.securityAlerts")} help={t("settings.securityAlertsHelp")} checked={config.notifications.security_alerts} onChange={(v) => upd("notifications", { security_alerts: v })} />
                <SwitchRow label={t("settings.weeklyReports")} help={t("settings.weeklyReportsHelp")} checked={config.notifications.weekly_reports} onChange={(v) => upd("notifications", { weekly_reports: v })} />
              </SectionCard>
            </>
          )}
          {saveBar}
        </TabsContent>
      </Tabs>

      <Dialog open={jwtDialogOpen} onOpenChange={setJwtDialogOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t("settings.rotateJwtTitle")}</DialogTitle>
            <DialogDescription>
              {t("settings.rotateJwtDescription")}
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="outline" onClick={() => setJwtDialogOpen(false)} disabled={jwtRotating}>
              {t("common.cancel")}
            </Button>
            <Button onClick={handleRotateJWT} disabled={jwtRotating}>
              {jwtRotating ? t("settings.rotating") : t("settings.rotateKey")}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
}
