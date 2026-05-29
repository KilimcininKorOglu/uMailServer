import { useState, useEffect } from "react";
import {
  Shield,
  MoreHorizontal,
  Edit,
  Trash2,
  RefreshCw,
  Mail,
  Bell,
  Clock,
} from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { Badge } from "@/components/ui/badge";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Skeleton } from "@/components/ui/skeleton";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { cn } from "@/lib/utils";

interface PolicyRule {
  id: string;
  name: string;
  enabled: boolean;
  priority: number;
  conditions: string;
  actions: string;
}

interface OOFSettings {
  enabled: boolean;
  subject: string;
  message: string;
  startDate: string;
  endDate: string;
  internalOnly: boolean;
}

interface RateLimitPolicy {
  id: string;
  name: string;
  type: "ip" | "user" | "domain";
  limit: number;
  window: number;
  enabled: boolean;
}

export function Policies() {
  const [activeTab, setActiveTab] = useState("oof");
  const [loading, setLoading] = useState(false);
  const [oofSettings, setOofSettings] = useState<OOFSettings>({
    enabled: false,
    subject: "",
    message: "",
    startDate: "",
    endDate: "",
    internalOnly: false,
  });
  const [rules, setRules] = useState<PolicyRule[]>([]);
  const [rateLimits, setRateLimits] = useState<RateLimitPolicy[]>([]);

  useEffect(() => {
    // Load initial data
    fetchOOF();
    fetchRules();
    fetchRateLimits();
  }, []);

  const fetchOOF = async () => {
    setLoading(true);
    try {
      const response = await fetch("/api/v1/admin/vacations", {
        credentials: "include",
      });
      if (response.ok) {
        // Active vacations list - would need to show actual OOF settings per mailbox
        await response.json();
      }
    } catch (err) {
      console.error("Failed to fetch OOF settings:", err);
    } finally {
      setLoading(false);
    }
  };

  const fetchRules = async () => {
    // Placeholder - would fetch from /api/v1/admin/rules
    setRules([
      { id: "1", name: "Auto-reply to external", enabled: true, priority: 1, conditions: "From external domains", actions: "Set vacation" },
      { id: "2", name: "Block spam sender", enabled: true, priority: 2, conditions: "Header contains spam", actions: "Move to junk" },
    ]);
  };

  const fetchRateLimits = async () => {
    // Placeholder - would fetch from /api/v1/admin/ratelimits/config
    setRateLimits([
      { id: "1", name: "SMTP per IP", type: "ip", limit: 100, window: 60, enabled: true },
      { id: "2", name: "SMTP per user", type: "user", limit: 500, window: 3600, enabled: true },
      { id: "3", name: "HTTP requests", type: "user", limit: 1000, window: 60, enabled: true },
    ]);
  };

  const toggleRule = async (rule: PolicyRule) => {
    // Toggle rule enabled state
    setRules((prev) =>
      prev.map((r) => (r.id === rule.id ? { ...r, enabled: !r.enabled } : r))
    );
  };

  const toggleRateLimit = async (limit: RateLimitPolicy) => {
    setRateLimits((prev) =>
      prev.map((r) => (r.id === limit.id ? { ...r, enabled: !r.enabled } : r))
    );
  };

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
        <Button variant="outline" onClick={() => { fetchOOF(); fetchRules(); fetchRateLimits(); }} disabled={loading}>
          <RefreshCw className={cn("mr-2 h-4 w-4", loading && "animate-spin")} />
          Refresh
        </Button>
      </div>

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
          {loading ? (
            <div className="space-y-2">
              <Skeleton className="h-12 w-full" />
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
              <div className="grid gap-4 md:grid-cols-3">
                <div className="p-4 rounded-lg bg-muted">
                  <div className="text-2xl font-bold">0</div>
                  <div className="text-sm text-muted-foreground">Active OOF</div>
                </div>
                <div className="p-4 rounded-lg bg-muted">
                  <div className="text-2xl font-bold">0</div>
                  <div className="text-sm text-muted-foreground">Scheduled</div>
                </div>
                <div className="p-4 rounded-lg bg-muted">
                  <div className="text-2xl font-bold">0</div>
                  <div className="text-sm text-muted-foreground">Expired</div>
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
              <CardTitle>OOF Settings</CardTitle>
              <CardDescription>
                Configure default OOF behavior and Exchange protocol policy gates
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
            </CardContent>
          </Card>
        </TabsContent>

        <TabsContent value="rules" className="space-y-4">
          <Card>
            <CardHeader>
              <CardTitle>Inbox Rules Policy</CardTitle>
              <CardDescription>
                Manage global inbox rules that apply across mailboxes
              </CardDescription>
            </CardHeader>
            <CardContent>
              {loading ? (
                <div className="space-y-4">
                  <Skeleton className="h-16 w-full" />
                  <Skeleton className="h-16 w-full" />
                </div>
              ) : rules.length === 0 ? (
                <div className="text-center py-8">
                  <Shield className="h-12 w-12 mx-auto text-muted-foreground mb-4" />
                  <h3 className="text-lg font-medium">No rules configured</h3>
                  <p className="text-muted-foreground mt-1">
                    Global inbox rules will appear here
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
                          <div className="font-medium">{rule.name}</div>
                          <div className="text-sm text-muted-foreground">{rule.conditions}</div>
                        </div>
                      </div>
                      <div className="flex items-center gap-2">
                        <Badge variant="secondary">{rule.priority}</Badge>
                        <Switch checked={rule.enabled} onCheckedChange={() => toggleRule(rule)} />
                        <DropdownMenu>
                          {/* @ts-expect-error asChild prop not typed in Base UI but works at runtime */}
                          <DropdownMenuTrigger asChild>
                            <Button variant="ghost" size="icon" className="h-8 w-8">
                              <MoreHorizontal className="h-4 w-4" />
                            </Button>
                          </DropdownMenuTrigger>
                          <DropdownMenuContent align="end">
                            <DropdownMenuItem>
                              <Edit className="mr-2 h-4 w-4" />
                              Edit
                            </DropdownMenuItem>
                            <DropdownMenuSeparator />
                            <DropdownMenuItem className="text-red-600">
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
                Configure throttling and rate limits for protocol access
              </CardDescription>
            </CardHeader>
            <CardContent>
              {loading ? (
                <div className="space-y-4">
                  <Skeleton className="h-16 w-full" />
                  <Skeleton className="h-16 w-full" />
                </div>
              ) : (
                <div className="space-y-2">
                  {rateLimits.map((limit) => (
                    <div
                      key={limit.id}
                      className="flex items-center justify-between p-4 rounded-lg border hover:bg-muted/50 transition-colors"
                    >
                      <div className="flex items-center gap-3">
                        <div className={cn("p-2 rounded-lg", limit.enabled ? "bg-blue-500/10" : "bg-muted")}>
                          <Clock className={cn("h-4 w-4", limit.enabled ? "text-blue-500" : "text-muted-foreground")} />
                        </div>
                        <div>
                          <div className="font-medium">{limit.name}</div>
                          <div className="text-sm text-muted-foreground">
                            {limit.limit} requests per {limit.window} seconds
                          </div>
                        </div>
                      </div>
                      <div className="flex items-center gap-2">
                        <Badge variant="secondary">{limit.type}</Badge>
                        <Switch checked={limit.enabled} onCheckedChange={() => toggleRateLimit(limit)} />
                        <DropdownMenu>
                          {/* @ts-expect-error asChild prop not typed in Base UI but works at runtime */}
                          <DropdownMenuTrigger asChild>
                            <Button variant="ghost" size="icon" className="h-8 w-8">
                              <MoreHorizontal className="h-4 w-4" />
                            </Button>
                          </DropdownMenuTrigger>
                          <DropdownMenuContent align="end">
                            <DropdownMenuItem>
                              <Edit className="mr-2 h-4 w-4" />
                              Edit
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
      </Tabs>
    </div>
  );
}
