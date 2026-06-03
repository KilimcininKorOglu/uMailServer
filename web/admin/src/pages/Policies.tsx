import { useState, useEffect } from "react";
import {
  Shield,
  MoreHorizontal,
  Trash2,
  RefreshCw,
  Mail,
  Bell,
  Clock,
  AlertCircle,
} from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { Badge } from "@/components/ui/badge";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Skeleton } from "@/components/ui/skeleton";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { cn } from "@/lib/utils";
import { useAdminRules, useConfig } from "@/hooks/useApi";
import type { PolicyRule, ServerConfig } from "@/types";

interface OOFSettings {
  enabled: boolean;
  subject: string;
  message: string;
  startDate: string;
  endDate: string;
  internalOnly: boolean;
}

// rateLimitFields reads the throttling values shown here from the same
// /admin/config source the Settings page edits, so the two screens stay
// consistent. Read-only here (edit them under Settings → Security).
function rateLimitFields(cfg: ServerConfig): { label: string; description: string; value: number }[] {
  return [
    { label: "Max emails per user", description: "per hour", value: cfg.security.rate_limit.user_per_hour },
    { label: "Max auth attempts", description: "before lockout", value: cfg.security.max_login_attempts },
  ];
}

export function Policies() {
  const { rules, loading: rulesLoading, fetchRules, toggleRule, deleteRule } = useAdminRules();
  const { config: rateLimitConfig, loading: rateLoading, fetchConfig: fetchRateLimitConfig, updateConfig } = useConfig();

  const [activeTab, setActiveTab] = useState("oof");
  const [oofLoading, setOofLoading] = useState(false);
  const [activeOOFCount, setActiveOOFCount] = useState(0);
  const [formError, setFormError] = useState<string | null>(null);
  const [oofSettings, setOofSettings] = useState<OOFSettings>({
    enabled: false,
    subject: "",
    message: "",
    startDate: "",
    endDate: "",
    internalOnly: false,
  });

  const [savingOOF, setSavingOOF] = useState(false);

  useEffect(() => {
    fetchOOF();
    fetchRules().catch(() => {});
    fetchRateLimitConfig().catch(() => {});
  }, [fetchRules, fetchRateLimitConfig]);

  // Seed the OOF defaults form from the persisted server config once it loads.
  useEffect(() => {
    if (!rateLimitConfig) return;
    setOofSettings((prev) => ({
      ...prev,
      enabled: rateLimitConfig.oof.default_enabled,
      internalOnly: rateLimitConfig.oof.internal_only,
      subject: rateLimitConfig.oof.default_subject,
      message: rateLimitConfig.oof.default_message,
    }));
  }, [rateLimitConfig]);

  const saveOOFDefaults = async () => {
    if (!rateLimitConfig) return;
    setSavingOOF(true);
    try {
      await updateConfig({
        ...rateLimitConfig,
        oof: {
          ...rateLimitConfig.oof,
          default_enabled: oofSettings.enabled,
          internal_only: oofSettings.internalOnly,
          default_subject: oofSettings.subject,
          default_message: oofSettings.message,
        },
      });
      await fetchRateLimitConfig().catch(() => {});
      setFormError(null);
    } catch (err) {
      setFormError((err as { message?: string }).message || "Failed to save OOF defaults");
    } finally {
      setSavingOOF(false);
    }
  };

  const fetchOOF = async () => {
    setOofLoading(true);
    try {
      const response = await fetch("/api/v1/admin/vacations", {
        credentials: "include",
      });
      if (response.ok) {
        const data = await response.json();
        setActiveOOFCount(data.count ?? (data.active_vacations?.length ?? 0));
      }
    } catch (err) {
      console.error("Failed to fetch OOF settings:", err);
    } finally {
      setOofLoading(false);
    }
  };

  const handleToggleRule = async (rule: PolicyRule) => {
    try {
      await toggleRule(rule.id, !rule.enabled);
      setFormError(null);
    } catch (err) {
      setFormError((err as { message?: string }).message || "Failed to update rule");
    }
  };

  const handleDeleteRule = async (id: string) => {
    try {
      await deleteRule(id);
      setFormError(null);
    } catch (err) {
      setFormError((err as { message?: string }).message || "Failed to delete rule");
    }
  };

  const refreshAll = () => {
    fetchOOF();
    fetchRules().catch(() => {});
    fetchRateLimitConfig().catch(() => {});
  };

  const loading = rulesLoading || rateLoading || oofLoading;

  return (
    <div className="space-y-6">
      {/* Header */}
      <div className="flex flex-col gap-4 sm:flex-row sm:items-center sm:justify-between">
        <div>
          <h1 className="text-3xl font-bold tracking-tight">Policies</h1>
          <p className="text-muted-foreground mt-1">
            Manage OOF, inbox rules, and Exchange protocol settings
          </p>
        </div>
        <Button variant="outline" onClick={refreshAll} disabled={loading}>
          <RefreshCw className={cn("mr-2 h-4 w-4", loading && "animate-spin")} />
          Refresh
        </Button>
      </div>

      {formError && (
        <Alert variant="destructive">
          <AlertCircle className="h-4 w-4" />
          <AlertDescription>{formError}</AlertDescription>
        </Alert>
      )}

      {/* Active OOF Summary */}
      <Card>
        <CardHeader>
          <CardTitle className="flex items-center gap-2">
            <Mail className="h-5 w-5" />
            Out of Office Summary
          </CardTitle>
          <CardDescription>
            Active OOF configurations across mailboxes
          </CardDescription>
        </CardHeader>
        <CardContent>
          {oofLoading ? (
            <div className="space-y-2">
              <Skeleton className="h-12 w-full" />
            </div>
          ) : (
            <div className="space-y-4">
              <Alert className="bg-blue-500/10 border-blue-500/20">
                <Bell className="h-4 w-4" />
                <AlertDescription>
                  Click into a specific mailbox to view or edit its OOF settings
                </AlertDescription>
              </Alert>
              <div className="grid gap-4 md:grid-cols-1 max-w-xs">
                <div className="p-4 rounded-lg bg-muted">
                  <div className="text-2xl font-bold">{activeOOFCount}</div>
                  <div className="text-sm text-muted-foreground">Active OOF</div>
                </div>
              </div>
            </div>
          )}
        </CardContent>
      </Card>

      {/* Tabs for OOF, Rules, and Rate Limits */}
      <Tabs value={activeTab} onValueChange={setActiveTab}>
        <TabsList>
          <TabsTrigger value="oof">
            <Mail className="h-4 w-4 mr-2" />
            Out of Office
          </TabsTrigger>
          <TabsTrigger value="rules">
            <Shield className="h-4 w-4 mr-2" />
            Inbox Rules
          </TabsTrigger>
          <TabsTrigger value="rate-limits">
            <Clock className="h-4 w-4 mr-2" />
            Rate Limits
          </TabsTrigger>
        </TabsList>

        <TabsContent value="oof" className="space-y-4">
          <Card>
            <CardHeader>
              <CardTitle>OOF Defaults</CardTitle>
              <CardDescription>
                Server-wide default out-of-office template. Per-mailbox OOF is
                managed by each user; these defaults are persisted server-side.
              </CardDescription>
            </CardHeader>
            <CardContent className="space-y-4">
              <div className="flex items-center justify-between">
                <div className="space-y-0.5">
                  <Label>OOF Enabled</Label>
                  <p className="text-xs text-muted-foreground">
                    Allow users to configure out-of-office replies
                  </p>
                </div>
                <Switch
                  checked={oofSettings.enabled}
                  onCheckedChange={(checked) => setOofSettings((prev) => ({ ...prev, enabled: checked }))}
                />
              </div>
              <div className="flex items-center justify-between">
                <div className="space-y-0.5">
                  <Label>Internal Only</Label>
                  <p className="text-xs text-muted-foreground">
                    Only send OOF replies to internal senders
                  </p>
                </div>
                <Switch
                  checked={oofSettings.internalOnly}
                  onCheckedChange={(checked) => setOofSettings((prev) => ({ ...prev, internalOnly: checked }))}
                />
              </div>
              <div className="space-y-2">
                <Label htmlFor="oof-subject">Default Subject</Label>
                <Input
                  id="oof-subject"
                  placeholder="Out of Office"
                  value={oofSettings.subject}
                  onChange={(e) => setOofSettings((prev) => ({ ...prev, subject: e.target.value }))}
                />
              </div>
              <div className="space-y-2">
                <Label htmlFor="oof-message">Default Message</Label>
                <Input
                  id="oof-message"
                  placeholder="I am currently out of office..."
                  value={oofSettings.message}
                  onChange={(e) => setOofSettings((prev) => ({ ...prev, message: e.target.value }))}
                />
              </div>
              <div className="flex justify-end">
                <Button onClick={saveOOFDefaults} disabled={savingOOF || !rateLimitConfig}>
                  Save Defaults
                </Button>
              </div>
            </CardContent>
          </Card>
        </TabsContent>

        <TabsContent value="rules" className="space-y-4">
          <Card>
            <CardHeader>
              <CardTitle>Inbox Rules Policy</CardTitle>
              <CardDescription>
                All inbox rules across mailboxes. Toggle to enable/disable or remove a rule.
              </CardDescription>
            </CardHeader>
            <CardContent>
              {rulesLoading ? (
                <div className="space-y-4">
                  <Skeleton className="h-16 w-full" />
                  <Skeleton className="h-16 w-full" />
                </div>
              ) : rules.length === 0 ? (
                <div className="text-center py-8">
                  <Shield className="h-12 w-12 mx-auto text-muted-foreground mb-4" />
                  <h3 className="text-lg font-medium">No rules configured</h3>
                  <p className="text-muted-foreground mt-1">
                    Inbox rules created by users will appear here
                  </p>
                </div>
              ) : (
                <div className="space-y-2">
                  {rules.map((rule) => (
                    <div
                      key={rule.id}
                      className="flex items-center justify-between p-4 rounded-lg border hover:bg-muted/50 transition-colors"
                    >
                      <div className="flex items-center gap-3">
                        <div className={cn("p-2 rounded-lg", rule.enabled ? "bg-emerald-500/10" : "bg-muted")}>
                          <Shield className={cn("h-4 w-4", rule.enabled ? "text-emerald-500" : "text-muted-foreground")} />
                        </div>
                        <div>
                          <div className="font-medium">{rule.name || "(unnamed rule)"}</div>
                          <div className="text-sm text-muted-foreground">
                            {rule.mailbox} &middot; {rule.conditions} &rarr; {rule.actions}
                          </div>
                        </div>
                      </div>
                      <div className="flex items-center gap-2">
                        <Badge variant="secondary">{rule.priority}</Badge>
                        <Switch checked={rule.enabled} onCheckedChange={() => handleToggleRule(rule)} />
                        <DropdownMenu>
                          {/* @ts-expect-error asChild prop not typed in Base UI but works at runtime */}
                          <DropdownMenuTrigger asChild>
                            <Button variant="ghost" size="icon" className="h-8 w-8">
                              <MoreHorizontal className="h-4 w-4" />
                            </Button>
                          </DropdownMenuTrigger>
                          <DropdownMenuContent align="end">
                            <DropdownMenuItem
                              className="text-red-600"
                              onClick={() => handleDeleteRule(rule.id)}
                            >
                              <Trash2 className="mr-2 h-4 w-4" />
                              Delete
                            </DropdownMenuItem>
                          </DropdownMenuContent>
                        </DropdownMenu>
                      </div>
                    </div>
                  ))}
                </div>
              )}
            </CardContent>
          </Card>
        </TabsContent>

        <TabsContent value="rate-limits" className="space-y-4">
          <Card>
            <CardHeader>
              <CardTitle>Rate Limiting Policy</CardTitle>
              <CardDescription>
                Current global throttling configuration (read-only here; edit under Settings)
              </CardDescription>
            </CardHeader>
            <CardContent>
              {rateLoading ? (
                <div className="space-y-4">
                  <Skeleton className="h-16 w-full" />
                  <Skeleton className="h-16 w-full" />
                </div>
              ) : !rateLimitConfig ? (
                <div className="text-center py-8">
                  <Clock className="h-12 w-12 mx-auto text-muted-foreground mb-4" />
                  <h3 className="text-lg font-medium">Rate limiting not available</h3>
                </div>
              ) : (
                <div className="space-y-2">
                  {rateLimitFields(rateLimitConfig).map((field) => (
                    <div
                      key={field.label}
                      className="flex items-center justify-between p-4 rounded-lg border"
                    >
                      <div className="flex items-center gap-3">
                        <div className="p-2 rounded-lg bg-blue-500/10">
                          <Clock className="h-4 w-4 text-blue-500" />
                        </div>
                        <div>
                          <div className="font-medium">{field.label}</div>
                          <div className="text-sm text-muted-foreground">{field.description}</div>
                        </div>
                      </div>
                      <Badge variant="secondary">{field.value}</Badge>
                    </div>
                  ))}
                </div>
              )}
            </CardContent>
          </Card>
        </TabsContent>
      </Tabs>
    </div>
  );
}
