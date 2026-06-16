import { useState, useEffect } from "react";
import {
  ActivitySquare,
  Search,
  RefreshCw,
  Server,
  AlertCircle,
  CheckCircle,
  Bell,
  XCircle,
  Smartphone,
  Clock,
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
import { useDiagnostics, useSyncActivity } from "@/hooks/useApi";
import { useI18n } from "@/hooks/useI18n";

export function Diagnostics() {
  const { t } = useI18n();
  const { mailboxes, subscriptions, failures, loading, fetchDiagnostics } = useDiagnostics();
  const { summary: syncActivity, loading: syncLoading, error: syncError, fetchSyncActivity } = useSyncActivity();
  const [searchQuery, setSearchQuery] = useState("");
  const [activeTab, setActiveTab] = useState("overview");
  const [selectedMailbox, setSelectedMailbox] = useState<string>("");

  useEffect(() => {
    fetchDiagnostics().catch(() => {
      /* error surfaced via hook state */
    });
    fetchSyncActivity().catch(() => {
      /* error surfaced via hook state */
    });
  }, [fetchDiagnostics, fetchSyncActivity]);

  const fetchMailboxDetails = (email: string) => {
    setSelectedMailbox((prev) => (prev === email ? "" : email));
  };

  const formatSync = (lastSync: string) => {
    if (!lastSync) return "—";
    const d = new Date(lastSync);
    return isNaN(d.getTime()) ? "—" : d.toLocaleString();
  };

  const filteredDiagnostics = mailboxes.filter((d) =>
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

  const getSyncStateLabel = (state: string) => {
    switch (state) {
      case "healthy":
        return t("diagnostics.syncStates.healthy");
      case "degraded":
        return t("diagnostics.syncStates.degraded");
      case "error":
        return t("common.error");
      default:
        return state;
    }
  };

  const getSubStatusLabel = (status: string) => {
    switch (status) {
      case "active":
        return t("common.active");
      case "expiring":
        return t("diagnostics.subscriptionStatuses.expiring");
      case "expired":
        return t("diagnostics.subscriptionStatuses.expired");
      default:
        return status;
    }
  };

  return (
    <div className="space-y-6">
      {/* Header */}
      <div className="flex flex-col gap-4 sm:flex-row sm:items-center sm:justify-between">
        <div>
          <h1 className="text-3xl font-bold tracking-tight">{t("diagnostics.title")}</h1>
          <p className="text-muted-foreground mt-1">
            {t("diagnostics.description")}
          </p>
        </div>
        <Button
          variant="outline"
          onClick={() => fetchDiagnostics().catch(() => {})}
          disabled={loading}
        >
          <RefreshCw className={cn("mr-2 h-4 w-4", loading && "animate-spin")} />
          {t("common.refresh")}
        </Button>
      </div>

      {/* Tabs */}
      <Tabs value={activeTab} onValueChange={setActiveTab}>
        <TabsList>
          <TabsTrigger value="overview">
            <Server className="h-4 w-4 mr-2" />
            {t("diagnostics.mailboxOverview")}
          </TabsTrigger>
          <TabsTrigger value="subscriptions">
            <Bell className="h-4 w-4 mr-2" />
            {t("diagnostics.subscriptions")}
          </TabsTrigger>
          <TabsTrigger value="failures">
            <AlertCircle className="h-4 w-4 mr-2" />
            {t("diagnostics.protocolFailures")}
          </TabsTrigger>
          <TabsTrigger value="syncActivity">
            <Smartphone className="h-4 w-4 mr-2" />
            {t("diagnostics.syncActivity.title")}
          </TabsTrigger>
        </TabsList>

        <TabsContent value="overview" className="space-y-4">
          <Card>
            <CardHeader>
              <CardTitle>{t("diagnostics.mailboxSyncHealth")}</CardTitle>
              <CardDescription>
                {t("diagnostics.mailboxSyncHealthDescription")}
              </CardDescription>
            </CardHeader>
            <CardContent>
              <div className="mb-4">
                <div className="relative max-w-sm">
                  <Search className="absolute left-3 top-1/2 -translate-y-1/2 h-4 w-4 text-muted-foreground" />
                  <Input
                    placeholder={t("diagnostics.searchMailboxes")}
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
                  <h3 className="text-lg font-medium">{t("diagnostics.noMailboxesFound")}</h3>
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
                              {t("diagnostics.lastSync", { time: formatSync(mbox.lastSync) })}
                            </div>
                          </div>
                        </div>
                        <div className="flex items-center gap-4">
                          {mbox.oofActive && (
                            <Badge variant="outline" className="text-xs">{t("diagnostics.oofActive")}</Badge>
                          )}
                          {mbox.rulesCount > 0 && (
                            <Badge variant="secondary" className="text-xs">{t("diagnostics.rulesCount", { count: String(mbox.rulesCount) })}</Badge>
                          )}
                          <div className="text-right">
                            <div className="text-sm font-medium">{t("diagnostics.foldersCount", { count: String(mbox.totalFolders) })}</div>
                            <div className="text-xs text-muted-foreground">{t("diagnostics.itemsCount", { count: String(mbox.totalItems) })}</div>
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
                            {getSyncStateLabel(mbox.syncState)}
                          </Badge>
                        </div>
                      </div>

                      {/* Expanded detail view */}
                      {selectedMailbox === mbox.email && (
                        <div className="mt-4 pt-4 border-t space-y-3">
                          <div className="grid gap-4 md:grid-cols-4">
                            <div className="p-3 rounded-lg bg-muted">
                              <Label className="text-xs text-muted-foreground">{t("diagnostics.subscriptionBacklog")}</Label>
                              <div className="text-xl font-bold">{mbox.subscriptionBacklog}</div>
                            </div>
                            <div className="p-3 rounded-lg bg-muted">
                              <Label className="text-xs text-muted-foreground">{t("diagnostics.protocolFailures")}</Label>
                              <div className="text-xl font-bold">{mbox.protocolFailures}</div>
                            </div>
                            <div className="p-3 rounded-lg bg-muted">
                              <Label className="text-xs text-muted-foreground">{t("diagnostics.policyBlocks")}</Label>
                              <div className="text-xl font-bold">{mbox.policyBlocks}</div>
                            </div>
                            <div className="p-3 rounded-lg bg-muted">
                              <Label className="text-xs text-muted-foreground">{t("diagnostics.syncHealth")}</Label>
                              <div className="flex items-center gap-2">
                                <Progress
                                  value={mbox.syncState === "healthy" ? 100 : mbox.syncState === "degraded" ? 50 : 0}
                                  className="h-2 flex-1"
                                />
                                <span className="text-sm font-medium capitalize">{getSyncStateLabel(mbox.syncState)}</span>
                              </div>
                            </div>
                          </div>
                          <div className="flex gap-2">
                            {/* The sync state is already shown inline above
                                (Sync Health); this jumps to the per-mailbox
                                subscription list on the Subscriptions tab. */}
                            <Button
                              variant="outline"
                              size="sm"
                              onClick={(e) => {
                                e.stopPropagation();
                                setActiveTab("subscriptions");
                              }}
                            >
                              <Bell className="mr-2 h-4 w-4" />
                              {t("diagnostics.viewSubscriptions")}
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
              <CardTitle>{t("diagnostics.activeSubscriptions")}</CardTitle>
              <CardDescription>
                {t("diagnostics.activeSubscriptionsDescription")}
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
                  <h3 className="text-lg font-medium">{t("diagnostics.noActiveSubscriptions")}</h3>
                  <p className="text-muted-foreground mt-1">
                    {t("diagnostics.subscriptionStateHint")}
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
                            {t("diagnostics.typeSubscription", { type: sub.type })}
                          </div>
                        </div>
                      </div>
                      <div className="flex items-center gap-4">
                        <div className="text-right">
                          <div className="text-sm">{t("diagnostics.watermark", { value: String(sub.watermark) })}</div>
                          <div className="text-xs text-muted-foreground">
                            {t("diagnostics.lastEvent", { time: new Date(sub.lastEvent).toLocaleString() })}
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
                          {getSubStatusLabel(sub.status)}
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
              <CardTitle>{t("diagnostics.protocolFailures")}</CardTitle>
              <CardDescription>
                {t("diagnostics.protocolFailuresDescription")}
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
                  <h3 className="text-lg font-medium">{t("diagnostics.noRecentFailures")}</h3>
                  <p className="text-muted-foreground mt-1">
                    {t("diagnostics.failuresHint")}
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

        <TabsContent value="syncActivity" className="space-y-4">
          <Card>
            <CardHeader>
              <CardTitle>{t("diagnostics.syncActivity.title")}</CardTitle>
              <CardDescription>
                {t("diagnostics.syncActivity.description")}
              </CardDescription>
            </CardHeader>
            <CardContent className="space-y-4">
              {syncError ? (
                <div className="text-center py-8">
                  <XCircle className="h-12 w-12 mx-auto text-red-500 mb-4" />
                  <h3 className="text-lg font-medium">{t("diagnostics.syncActivity.errorTitle")}</h3>
                  <p className="text-muted-foreground mt-1">{syncError.message || t("common.error")}</p>
                  <Button
                    variant="outline"
                    className="mt-4"
                    onClick={() => fetchSyncActivity().catch(() => {})}
                  >
                    <RefreshCw className="mr-2 h-4 w-4" />
                    {t("common.retry")}
                  </Button>
                </div>
              ) : syncLoading && !syncActivity ? (
                <div className="space-y-4">
                  <Skeleton className="h-20 w-full" />
                  <Skeleton className="h-16 w-full" />
                  <Skeleton className="h-16 w-full" />
                </div>
              ) : syncActivity ? (
                <>
                  <div className="grid gap-4 md:grid-cols-4">
                    <div className="p-3 rounded-lg bg-muted" data-testid="sync-activity-total">
                      <Label className="text-xs text-muted-foreground">{t("diagnostics.syncActivity.totalLabel")}</Label>
                      <div className="text-xl font-bold">{syncActivity.total}</div>
                    </div>
                    <div className="p-3 rounded-lg bg-muted" data-testid="sync-activity-active-1d">
                      <Label className="text-xs text-muted-foreground">{t("diagnostics.syncActivity.active1dLabel")}</Label>
                      <div className="text-xl font-bold text-emerald-500">{syncActivity.active_1d}</div>
                    </div>
                    <div className="p-3 rounded-lg bg-muted" data-testid="sync-activity-active-7d">
                      <Label className="text-xs text-muted-foreground">{t("diagnostics.syncActivity.active7dLabel")}</Label>
                      <div className="text-xl font-bold text-emerald-500">{syncActivity.active_7d}</div>
                    </div>
                    <div className="p-3 rounded-lg bg-muted" data-testid="sync-activity-stale">
                      <Label className="text-xs text-muted-foreground">{t("diagnostics.syncActivity.staleLabel")}</Label>
                      <div className={cn("text-xl font-bold", syncActivity.stale > 0 ? "text-amber-500" : "text-muted-foreground")}>{syncActivity.stale}</div>
                    </div>
                  </div>

                  <div className="flex items-center gap-4 text-xs text-muted-foreground">
                    <span className="flex items-center gap-1">
                      <Clock className="h-3 w-3" />
                      {t("diagnostics.syncActivity.generated", { time: new Date(syncActivity.generated).toLocaleString() })}
                    </span>
                    <span>{t("diagnostics.syncActivity.staleAfter", { time: new Date(syncActivity.stale_after).toLocaleString() })}</span>
                  </div>

                  {syncActivity.devices.length === 0 ? (
                    <div className="text-center py-8">
                      <Smartphone className="h-12 w-12 mx-auto text-muted-foreground mb-4" />
                      <h3 className="text-lg font-medium">{t("diagnostics.syncActivity.emptyTitle")}</h3>
                      <p className="text-muted-foreground mt-1">{t("diagnostics.syncActivity.emptyHint")}</p>
                    </div>
                  ) : (
                    <div className="rounded-lg border overflow-hidden">
                      <table className="w-full text-sm" data-testid="sync-activity-table">
                        <thead className="bg-muted">
                          <tr>
                            <th className="text-left p-3 font-medium">{t("diagnostics.syncActivity.columnAccount")}</th>
                            <th className="text-left p-3 font-medium">{t("diagnostics.syncActivity.columnDevice")}</th>
                            <th className="text-left p-3 font-medium">{t("diagnostics.syncActivity.columnProtocol")}</th>
                            <th className="text-left p-3 font-medium">{t("diagnostics.syncActivity.columnLastSync")}</th>
                            <th className="text-left p-3 font-medium">{t("diagnostics.syncActivity.columnFreshness")}</th>
                          </tr>
                        </thead>
                        <tbody>
                          {syncActivity.devices.map((device) => (
                            <tr
                              key={`${device.email}/${device.device_id}`}
                              className="border-t hover:bg-muted/50"
                              data-testid={`sync-activity-row-${device.device_id}`}
                            >
                              <td className="p-3 font-medium">{device.email}</td>
                              <td className="p-3">
                                <div>{device.friendly_name || device.device_id}</div>
                                <div className="text-xs text-muted-foreground">{device.device_type} · {device.device_id}</div>
                              </td>
                              <td className="p-3 text-muted-foreground">{device.protocol}</td>
                              <td className="p-3 text-muted-foreground">
                                {device.last_sync ? new Date(device.last_sync).toLocaleString() : t("diagnostics.syncActivity.neverSynced")}
                              </td>
                              <td className="p-3">
                                {device.stale ? (
                                  <Badge variant="outline" className="bg-amber-500/10 text-amber-500 border-amber-500/30 text-xs">
                                    {t("diagnostics.syncActivity.staleBadge", { days: String(device.freshness_days) })}
                                  </Badge>
                                ) : (
                                  <Badge variant="outline" className="bg-emerald-500/10 text-emerald-500 border-emerald-500/30 text-xs">
                                    {t("diagnostics.syncActivity.freshnessDays", { days: String(device.freshness_days) })}
                                  </Badge>
                                )}
                              </td>
                            </tr>
                          ))}
                        </tbody>
                      </table>
                    </div>
                  )}
                </>
              ) : null}
            </CardContent>
          </Card>
        </TabsContent>
      </Tabs>
    </div>
  );
}
