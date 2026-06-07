import { useCallback, useEffect, useState } from "react";
import {
  Network,
  RefreshCw,
  Crown,
  Server,
  CheckCircle,
  AlertCircle,
  XCircle,
  Power,
} from "lucide-react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Skeleton } from "@/components/ui/skeleton";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { cn } from "@/lib/utils";
import { useCluster } from "@/hooks/useApi";
import { useI18n } from "@/hooks/useI18n";
import type { ClusterInstance } from "@/types";

type TranslateFn = (key: string, params?: Record<string, string>) => string;

function statusBadge(status: ClusterInstance["status"], t: TranslateFn) {
  switch (status) {
    case "healthy":
      return (
        <Badge className="bg-emerald-500/10 text-emerald-500 hover:bg-emerald-500/10">
          <CheckCircle className="mr-1 h-3 w-3" />
          {t("cluster.healthy")}
        </Badge>
      );
    case "degraded":
      return (
        <Badge className="bg-amber-500/10 text-amber-500 hover:bg-amber-500/10">
          <AlertCircle className="mr-1 h-3 w-3" />
          {t("cluster.degraded")}
        </Badge>
      );
    default:
      return (
        <Badge className="bg-red-500/10 text-red-500 hover:bg-red-500/10">
          <XCircle className="mr-1 h-3 w-3" />
          {t("cluster.offline")}
        </Badge>
      );
  }
}

function heartbeatAge(iso: string, t: TranslateFn): string {
  const beat = new Date(iso).getTime();
  if (Number.isNaN(beat)) return "—";
  const seconds = Math.max(0, Math.round((Date.now() - beat) / 1000));
  if (seconds < 60) return t("cluster.secondsAgo", { seconds: String(seconds) });
  if (seconds < 3600) return t("cluster.minutesAgo", { minutes: String(Math.floor(seconds / 60)) });
  return new Date(iso).toLocaleString();
}

export function Cluster() {
  const { t } = useI18n();
  const { status, loading, fetchStatus, triggerFailover } = useCluster();
  const [failoverOpen, setFailoverOpen] = useState(false);
  const [failingOver, setFailingOver] = useState(false);

  const refresh = useCallback(() => {
    fetchStatus().catch(() => {
      /* error surfaced via hook state */
    });
  }, [fetchStatus]);

  useEffect(() => {
    refresh();
    // Heartbeat ages go stale quickly; keep the view live with a light poll.
    const interval = setInterval(refresh, 10000);
    return () => clearInterval(interval);
  }, [refresh]);

  const instances = status?.instances ?? [];
  const healthyCount = instances.filter((i) => i.status === "healthy").length;
  const leader = instances.find((i) => i.is_leader);
  const canFailover = Boolean(status?.is_leader) && healthyCount > 1;

  const handleFailover = async () => {
    setFailingOver(true);
    try {
      const result = await triggerFailover();
      toast.success(t("cluster.failoverTriggered", { leader: result.new_leader }));
      setFailoverOpen(false);
      refresh();
    } catch (err) {
      const message = (err as { message?: string }).message || t("cluster.failoverFailed");
      toast.error(message);
    } finally {
      setFailingOver(false);
    }
  };

  return (
    <div className="space-y-6">
      {/* Header */}
      <div className="flex flex-col gap-4 sm:flex-row sm:items-center sm:justify-between">
        <div>
          <h1 className="text-3xl font-bold tracking-tight">{t("cluster.title")}</h1>
          <p className="text-muted-foreground mt-1">
            {t("cluster.description")}
          </p>
        </div>
        <div className="flex items-center gap-2">
          {canFailover && (
            <Button variant="destructive" onClick={() => setFailoverOpen(true)}>
              <Power className="mr-2 h-4 w-4" />
              {t("cluster.triggerFailover")}
            </Button>
          )}
          <Button variant="outline" onClick={refresh} disabled={loading}>
            <RefreshCw className={cn("mr-2 h-4 w-4", loading && "animate-spin")} />
            {t("common.refresh")}
          </Button>
        </div>
      </div>

      {!status && loading ? (
        <div className="space-y-4">
          <Skeleton className="h-24 w-full" />
          <Skeleton className="h-48 w-full" />
        </div>
      ) : !status?.enabled ? (
        <Card>
          <CardContent className="py-12 text-center">
            <Network className="h-12 w-12 mx-auto text-muted-foreground mb-4" />
            <h3 className="text-lg font-medium">{t("cluster.disabledTitle")}</h3>
            <p className="text-muted-foreground mt-1 max-w-md mx-auto">
              {t("cluster.disabledDescription")}
            </p>
          </CardContent>
        </Card>
      ) : (
        <>
          {/* Summary cards */}
          <div className="grid gap-4 sm:grid-cols-3">
            <Card>
              <CardHeader className="pb-2">
                <CardDescription>{t("cluster.thisInstance")}</CardDescription>
                <CardTitle className="flex items-center gap-2 text-lg font-mono">
                  <Server className="h-4 w-4 shrink-0" />
                  <span className="truncate">{status.instance_id || "—"}</span>
                </CardTitle>
              </CardHeader>
              <CardContent>
                {status.is_leader ? (
                  <Badge className="bg-amber-500/10 text-amber-500 hover:bg-amber-500/10">
                    <Crown className="mr-1 h-3 w-3" />
                    {t("cluster.leader")}
                  </Badge>
                ) : (
                  <Badge variant="secondary">{t("cluster.follower")}</Badge>
                )}
              </CardContent>
            </Card>
            <Card>
              <CardHeader className="pb-2">
                <CardDescription>{t("cluster.instances")}</CardDescription>
                <CardTitle className="text-3xl">{instances.length}</CardTitle>
              </CardHeader>
              <CardContent>
                <p className="text-sm text-muted-foreground">
                  {t("cluster.healthSummary", {
                    healthy: String(healthyCount),
                    unhealthy: String(instances.length - healthyCount),
                  })}
                </p>
              </CardContent>
            </Card>
            <Card>
              <CardHeader className="pb-2">
                <CardDescription>{t("cluster.currentLeader")}</CardDescription>
                <CardTitle className="text-lg font-mono truncate">
                  {leader ? leader.instance_id : "—"}
                </CardTitle>
              </CardHeader>
              <CardContent>
                <p className="text-sm text-muted-foreground">
                  {leader ? t("cluster.leaderDescription") : t("cluster.noLeaderElected")}
                </p>
              </CardContent>
            </Card>
          </div>

          {/* Instances table */}
          <Card>
            <CardHeader>
              <CardTitle className="flex items-center gap-2">
                <Network className="h-5 w-5" />
                {t("cluster.nodes")}
              </CardTitle>
              <CardDescription>
                {t("cluster.nodesDescription")}
              </CardDescription>
            </CardHeader>
            <CardContent>
              {instances.length === 0 ? (
                <div className="text-center py-8">
                  <AlertCircle className="h-12 w-12 mx-auto text-muted-foreground mb-4" />
                  <h3 className="text-lg font-medium">{t("cluster.noHeartbeatsTitle")}</h3>
                  <p className="text-muted-foreground mt-1">
                    {t("cluster.noHeartbeatsDescription")}
                  </p>
                </div>
              ) : (
                <Table>
                  <TableHeader>
                    <TableRow>
                      <TableHead>{t("cluster.instance")}</TableHead>
                      <TableHead>{t("common.status")}</TableHead>
                      <TableHead>{t("cluster.role")}</TableHead>
                      <TableHead className="text-right">{t("cluster.connections")}</TableHead>
                      <TableHead className="text-right">{t("cluster.lastHeartbeat")}</TableHead>
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    {instances.map((inst) => (
                      <TableRow key={inst.instance_id}>
                        <TableCell className="font-mono">
                          <span className="flex items-center gap-2">
                            {inst.instance_id}
                            {inst.instance_id === status.instance_id && (
                              <Badge variant="outline">{t("cluster.thisNode")}</Badge>
                            )}
                          </span>
                        </TableCell>
                        <TableCell>{statusBadge(inst.status, t)}</TableCell>
                        <TableCell>
                          {inst.is_leader ? (
                            <Badge className="bg-amber-500/10 text-amber-500 hover:bg-amber-500/10">
                              <Crown className="mr-1 h-3 w-3" />
                              {t("cluster.leader")}
                            </Badge>
                          ) : (
                            <span className="text-muted-foreground">{t("cluster.follower")}</span>
                          )}
                        </TableCell>
                        <TableCell className="text-right">{inst.connections}</TableCell>
                        <TableCell className="text-right text-muted-foreground">
                          {heartbeatAge(inst.last_heartbeat, t)}
                        </TableCell>
                      </TableRow>
                    ))}
                  </TableBody>
                </Table>
              )}
            </CardContent>
          </Card>
        </>
      )}

      {/* Failover confirm */}
      <Dialog open={failoverOpen} onOpenChange={setFailoverOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t("cluster.failoverConfirmTitle")}</DialogTitle>
            <DialogDescription>
              {t("cluster.failoverConfirmDescription")}
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="outline" onClick={() => setFailoverOpen(false)} disabled={failingOver}>
              {t("common.cancel")}
            </Button>
            <Button variant="destructive" onClick={handleFailover} disabled={failingOver}>
              {failingOver && <RefreshCw className="mr-2 h-4 w-4 animate-spin" />}
              {t("cluster.triggerFailover")}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
}
