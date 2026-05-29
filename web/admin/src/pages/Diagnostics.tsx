import { useState, useEffect } from "react";
import {
  ActivitySquare,
  Search,
  RefreshCw,
  Server,
  AlertCircle,
  CheckCircle,
  Database,
  Bell,
  XCircle,
} from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Label } from "@/components/ui/label";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Skeleton } from "@/components/ui/skeleton";
import { Progress } from "@/components/ui/progress";
import { cn } from "@/lib/utils";
import { useAccounts } from "@/hooks/useApi";

interface MailboxDiagnostics {
  email: string;
  syncState: "healthy" | "degraded" | "error";
  lastSync: string;
  subscriptionBacklog: number;
  protocolFailures: number;
  policyBlocks: number;
  oofActive: boolean;
  rulesCount: number;
  totalFolders: number;
  totalItems: number;
}

interface SubscriptionInfo {
  id: string;
  mailbox: string;
  type: string;
  status: "active" | "expiring" | "expired";
  watermark: string;
  createdAt: string;
  lastEvent: string;
}

interface ProtocolFailure {
  id: string;
  mailbox: string;
  protocol: string;
  error: string;
  timestamp: string;
}

export function Diagnostics() {
  const { loading, fetchAccounts } = useAccounts();
  const [searchQuery, setSearchQuery] = useState("");
  const [activeTab, setActiveTab] = useState("overview");
  const [diagnostics, setDiagnostics] = useState<MailboxDiagnostics[]>([]);
  const [subscriptions, setSubscriptions] = useState<SubscriptionInfo[]>([]);
  const [failures, setFailures] = useState<ProtocolFailure[]>([]);
  const [selectedMailbox, setSelectedMailbox] = useState<string>("");

  useEffect(() => {
    fetchAccounts();
    fetchDiagnosticsSummary();
    fetchSubscriptions();
    fetchFailures();
  }, []);

  const fetchDiagnosticsSummary = async () => {
    // Placeholder - would fetch from /api/v1/admin/diagnostics
    setDiagnostics([
      {
        email: "admin@local.test",
        syncState: "healthy",
        lastSync: new Date().toISOString(),
        subscriptionBacklog: 0,
        protocolFailures: 0,
        policyBlocks: 0,
        oofActive: false,
        rulesCount: 2,
        totalFolders: 15,
        totalItems: 142,
      },
      {
        email: "user@local.test",
        syncState: "degraded",
        lastSync: new Date(Date.now() - 300000).toISOString(),
        subscriptionBacklog: 5,
        protocolFailures: 1,
        policyBlocks: 0,
        oofActive: true,
        rulesCount: 1,
        totalFolders: 8,
        totalItems: 67,
      },
    ]);
  };

  const fetchSubscriptions = async () => {
    setSubscriptions([
      {
        id: "sub-1",
        mailbox: "admin@local.test",
        type: "pull",
        status: "active",
        watermark: "12345",
        createdAt: new Date().toISOString(),
        lastEvent: new Date().toISOString(),
      },
    ]);
  };

  const fetchFailures = async () => {
    setFailures([
      {
        id: "fail-1",
        mailbox: "user@local.test",
        protocol: "IMAP",
        error: "Connection timeout",
        timestamp: new Date(Date.now() - 60000).toISOString(),
      },
    ]);
  };

  const fetchMailboxDetails = async (email: string) => {
    // Placeholder - would fetch detailed diagnostics from /api/v1/admin/diagnostics/{email}
    setSelectedMailbox(email);
  };

  const filteredDiagnostics = diagnostics.filter((d) =>
    d.email.toLowerCase().includes(searchQuery.toLowerCase())
  );

  const getSyncStateIcon = (state: string) => {
    switch (state) {
      case "healthy":
        return <CheckCircle className="h-4 w-4 text-emerald-500" />;
      case "degraded":
        return <AlertCircle className="h-4 w-4 text-amber-500" />;
      case "error":
        return <XCircle className="h-4 w-4 text-red-500" />;
      default:
        return <ActivitySquare className="h-4 w-4 text-muted-foreground" />;
    }
  };

  const getSyncStateColor = (state: string) => {
    switch (state) {
      case "healthy":
        return "bg-emerald-500/10 text-emerald-500";
      case "degraded":
        return "bg-amber-500/10 text-amber-500";
      case "error":
        return "bg-red-500/10 text-red-500";
      default:
        return "bg-muted text-muted-foreground";
    }
  };

  return (
    <div className="space-y-6">
      {/* Header */}
      <div className="flex flex-col gap-4 sm:flex-row sm:items-center sm:justify-between">
        <div>
          <h1 className="text-3xl font-bold tracking-tight">Diagnostics</h1>
          <p className="text-muted-foreground mt-1">
            Monitor sync state, subscriptions, and protocol health
          </p>
        </div>
        <Button
          variant="outline"
          onClick={() => {
            fetchDiagnosticsSummary();
            fetchSubscriptions();
            fetchFailures();
          }}
          disabled={loading}
        >
          <RefreshCw className={cn("mr-2 h-4 w-4", loading && "animate-spin")} />
          Refresh
        </Button>
      </div>

      {/* Tabs */}
      <Tabs value={activeTab} onValueChange={setActiveTab}>
        <TabsList>
          <TabsTrigger value="overview">
            <Server className="h-4 w-4 mr-2" />
            Mailbox Overview
          </TabsTrigger>
          <TabsTrigger value="subscriptions">
            <Bell className="h-4 w-4 mr-2" />
            Subscriptions
          </TabsTrigger>
          <TabsTrigger value="failures">
            <AlertCircle className="h-4 w-4 mr-2" />
            Protocol Failures
          </TabsTrigger>
        </TabsList>

        <TabsContent value="overview" className="space-y-4">
          <Card>
            <CardHeader>
              <CardTitle>Mailbox Sync Health</CardTitle>
              <CardDescription>
                Per-mailbox sync state, subscription backlog, and policy enforcement
              </CardDescription>
            </CardHeader>
            <CardContent>
              <div className="mb-4">
                <div className="relative max-w-sm">
                  <Search className="absolute left-3 top-1/2 -translate-y-1/2 h-4 w-4 text-muted-foreground" />
                  <Input
                    placeholder="Search mailboxes..."
                    value={searchQuery}
                    onChange={(e) => setSearchQuery(e.target.value)}
                    className="pl-10"
                  />
                </div>
              </div>

              {loading ? (
                <div className="space-y-4">
                  <Skeleton className="h-20 w-full" />
                  <Skeleton className="h-20 w-full" />
                </div>
              ) : filteredDiagnostics.length === 0 ? (
                <div className="text-center py-8">
                  <Server className="h-12 w-12 mx-auto text-muted-foreground mb-4" />
                  <h3 className="text-lg font-medium">No mailboxes found</h3>
                </div>
              ) : (
                <div className="space-y-3">
                  {filteredDiagnostics.map((mbox) => (
                    <div
                      key={mbox.email}
                      className="p-4 rounded-lg border hover:bg-muted/50 transition-colors cursor-pointer"
                      onClick={() => fetchMailboxDetails(mbox.email)}
                    >
                      <div className="flex items-center justify-between">
                        <div className="flex items-center gap-3">
                          <div className={cn("p-2 rounded-lg", getSyncStateColor(mbox.syncState))}>
                            {getSyncStateIcon(mbox.syncState)}
                          </div>
                          <div>
                            <div className="font-medium">{mbox.email}</div>
                            <div className="text-sm text-muted-foreground">
                              Last sync: {new Date(mbox.lastSync).toLocaleString()}
                            </div>
                          </div>
                        </div>
                        <div className="flex items-center gap-4">
                          {mbox.oofActive && (
                            <Badge variant="outline" className="text-xs">OOF Active</Badge>
                          )}
                          {mbox.rulesCount > 0 && (
                            <Badge variant="secondary" className="text-xs">{mbox.rulesCount} rules</Badge>
                          )}
                          <div className="text-right">
                            <div className="text-sm font-medium">{mbox.totalFolders} folders</div>
                            <div className="text-xs text-muted-foreground">{mbox.totalItems} items</div>
                          </div>
                          <Badge
                            variant="secondary"
                            className={cn(
                              "text-xs",
                              mbox.syncState === "healthy" && "bg-emerald-500/10 text-emerald-500",
                              mbox.syncState === "degraded" && "bg-amber-500/10 text-amber-500",
                              mbox.syncState === "error" && "bg-red-500/10 text-red-500"
                            )}
                          >
                            {mbox.syncState}
                          </Badge>
                        </div>
                      </div>

                      {/* Expanded detail view */}
                      {selectedMailbox === mbox.email && (
                        <div className="mt-4 pt-4 border-t space-y-3">
                          <div className="grid gap-4 md:grid-cols-4">
                            <div className="p-3 rounded-lg bg-muted">
                              <Label className="text-xs text-muted-foreground">Subscription Backlog</Label>
                              <div className="text-xl font-bold">{mbox.subscriptionBacklog}</div>
                            </div>
                            <div className="p-3 rounded-lg bg-muted">
                              <Label className="text-xs text-muted-foreground">Protocol Failures</Label>
                              <div className="text-xl font-bold">{mbox.protocolFailures}</div>
                            </div>
                            <div className="p-3 rounded-lg bg-muted">
                              <Label className="text-xs text-muted-foreground">Policy Blocks</Label>
                              <div className="text-xl font-bold">{mbox.policyBlocks}</div>
                            </div>
                            <div className="p-3 rounded-lg bg-muted">
                              <Label className="text-xs text-muted-foreground">Sync Health</Label>
                              <div className="flex items-center gap-2">
                                <Progress
                                  value={mbox.syncState === "healthy" ? 100 : mbox.syncState === "degraded" ? 50 : 0}
                                  className="h-2 flex-1"
                                />
                                <span className="text-sm font-medium capitalize">{mbox.syncState}</span>
                              </div>
                            </div>
                          </div>
                          <div className="flex gap-2">
                            <Button variant="outline" size="sm">
                              <Database className="mr-2 h-4 w-4" />
                              View Sync State
                            </Button>
                            <Button variant="outline" size="sm">
                              <Bell className="mr-2 h-4 w-4" />
                              View Subscriptions
                            </Button>
                          </div>
                        </div>
                      )}
                    </div>
                  ))}
                </div>
              )}
            </CardContent>
          </Card>
        </TabsContent>

        <TabsContent value="subscriptions" className="space-y-4">
          <Card>
            <CardHeader>
              <CardTitle>Active Subscriptions</CardTitle>
              <CardDescription>
                Long-lived event subscriptions per mailbox
              </CardDescription>
            </CardHeader>
            <CardContent>
              {loading ? (
                <div className="space-y-4">
                  <Skeleton className="h-16 w-full" />
                  <Skeleton className="h-16 w-full" />
                </div>
              ) : subscriptions.length === 0 ? (
                <div className="text-center py-8">
                  <Bell className="h-12 w-12 mx-auto text-muted-foreground mb-4" />
                  <h3 className="text-lg font-medium">No active subscriptions</h3>
                  <p className="text-muted-foreground mt-1">
                    Subscription state will appear here
                  </p>
                </div>
              ) : (
                <div className="space-y-2">
                  {subscriptions.map((sub) => (
                    <div
                      key={sub.id}
                      className="flex items-center justify-between p-4 rounded-lg border hover:bg-muted/50 transition-colors"
                    >
                      <div className="flex items-center gap-3">
                        <div className={cn("p-2 rounded-lg",
                          sub.status === "active" ? "bg-emerald-500/10" :
                          sub.status === "expiring" ? "bg-amber-500/10" : "bg-red-500/10"
                        )}>
                          <Bell className={cn("h-4 w-4",
                            sub.status === "active" ? "text-emerald-500" :
                            sub.status === "expiring" ? "text-amber-500" : "text-red-500"
                          )} />
                        </div>
                        <div>
                          <div className="font-medium">{sub.mailbox}</div>
                          <div className="text-sm text-muted-foreground">
                            {sub.type} subscription
                          </div>
                        </div>
                      </div>
                      <div className="flex items-center gap-4">
                        <div className="text-right">
                          <div className="text-sm">Watermark: {sub.watermark}</div>
                          <div className="text-xs text-muted-foreground">
                            Last event: {new Date(sub.lastEvent).toLocaleString()}
                          </div>
                        </div>
                        <Badge
                          variant="secondary"
                          className={cn(
                            sub.status === "active" && "bg-emerald-500/10 text-emerald-500",
                            sub.status === "expiring" && "bg-amber-500/10 text-amber-500",
                            sub.status === "expired" && "bg-red-500/10 text-red-500"
                          )}
                        >
                          {sub.status}
                        </Badge>
                      </div>
                    </div>
                  ))}
                </div>
              )}
            </CardContent>
          </Card>
        </TabsContent>

        <TabsContent value="failures" className="space-y-4">
          <Card>
            <CardHeader>
              <CardTitle>Protocol Failures</CardTitle>
              <CardDescription>
                Recent protocol failures and policy blocks
              </CardDescription>
            </CardHeader>
            <CardContent>
              {loading ? (
                <div className="space-y-4">
                  <Skeleton className="h-16 w-full" />
                  <Skeleton className="h-16 w-full" />
                </div>
              ) : failures.length === 0 ? (
                <div className="text-center py-8">
                  <CheckCircle className="h-12 w-12 mx-auto text-emerald-500 mb-4" />
                  <h3 className="text-lg font-medium">No recent failures</h3>
                  <p className="text-muted-foreground mt-1">
                    Protocol failures will appear here
                  </p>
                </div>
              ) : (
                <div className="space-y-2">
                  {failures.map((failure) => (
                    <div
                      key={failure.id}
                      className="flex items-center justify-between p-4 rounded-lg border hover:bg-muted/50 transition-colors"
                    >
                      <div className="flex items-center gap-3">
                        <div className="p-2 rounded-lg bg-red-500/10">
                          <XCircle className="h-4 w-4 text-red-500" />
                        </div>
                        <div>
                          <div className="font-medium">{failure.mailbox}</div>
                          <div className="text-sm text-muted-foreground">
                            {failure.protocol}: {failure.error}
                          </div>
                        </div>
                      </div>
                      <div className="text-right">
                        <div className="text-sm text-muted-foreground">
                          {new Date(failure.timestamp).toLocaleString()}
                        </div>
                        <Badge variant="destructive" className="text-xs">
                          {failure.protocol}
                        </Badge>
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
