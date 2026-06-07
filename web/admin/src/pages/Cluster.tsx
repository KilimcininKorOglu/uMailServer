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
import type { ClusterInstance } from "@/types";

function statusBadge(status: ClusterInstance["status"]) {
  switch (status) {
    case "healthy":
      return (
        <Badge className="bg-emerald-500/10 text-emerald-500 hover:bg-emerald-500/10">
          <CheckCircle className="mr-1 h-3 w-3" />
          Healthy
        </Badge>
      );
    case "degraded":
      return (
        <Badge className="bg-amber-500/10 text-amber-500 hover:bg-amber-500/10">
          <AlertCircle className="mr-1 h-3 w-3" />
          Degraded
        </Badge>
      );
    default:
      return (
        <Badge className="bg-red-500/10 text-red-500 hover:bg-red-500/10">
          <XCircle className="mr-1 h-3 w-3" />
          Offline
        </Badge>
      );
  }
}

function heartbeatAge(iso: string): string {
  const beat = new Date(iso).getTime();
  if (Number.isNaN(beat)) return "—";
  const seconds = Math.max(0, Math.round((Date.now() - beat) / 1000));
  if (seconds < 60) return `${seconds}s ago`;
  if (seconds < 3600) return `${Math.floor(seconds / 60)}m ago`;
  return new Date(iso).toLocaleString();
}

export function Cluster() {
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
      toast.success(`Failover triggered — new leader: ${result.new_leader}`);
      setFailoverOpen(false);
      refresh();
    } catch (err) {
      const message = (err as { message?: string }).message || "Failover failed";
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
          <h1 className="text-3xl font-bold tracking-tight">Cluster</h1>
          <p className="text-muted-foreground mt-1">
            High-availability node health, leadership, and failover
          </p>
        </div>
        <div className="flex items-center gap-2">
          {canFailover && (
            <Button variant="destructive" onClick={() => setFailoverOpen(true)}>
              <Power className="mr-2 h-4 w-4" />
              Trigger Failover
            </Button>
          )}
          <Button variant="outline" onClick={refresh} disabled={loading}>
            <RefreshCw className={cn("mr-2 h-4 w-4", loading && "animate-spin")} />
            Refresh
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
            <h3 className="text-lg font-medium">Cluster mode is disabled</h3>
            <p className="text-muted-foreground mt-1 max-w-md mx-auto">
              This server runs as a single node. Enable clustering with the
              `cluster` section of the server configuration (Redis URL plus a
              shared JWT secret) and restart to join nodes into a cluster.
            </p>
          </CardContent>
        </Card>
      ) : (
        <>
          {/* Summary cards */}
          <div className="grid gap-4 sm:grid-cols-3">
            <Card>
              <CardHeader className="pb-2">
                <CardDescription>This instance</CardDescription>
                <CardTitle className="flex items-center gap-2 text-lg font-mono">
                  <Server className="h-4 w-4 shrink-0" />
                  <span className="truncate">{status.instance_id || "—"}</span>
                </CardTitle>
              </CardHeader>
              <CardContent>
                {status.is_leader ? (
                  <Badge className="bg-amber-500/10 text-amber-500 hover:bg-amber-500/10">
                    <Crown className="mr-1 h-3 w-3" />
                    Leader
                  </Badge>
                ) : (
                  <Badge variant="secondary">Follower</Badge>
                )}
              </CardContent>
            </Card>
            <Card>
              <CardHeader className="pb-2">
                <CardDescription>Instances</CardDescription>
                <CardTitle className="text-3xl">{instances.length}</CardTitle>
              </CardHeader>
              <CardContent>
                <p className="text-sm text-muted-foreground">
                  {healthyCount} healthy / {instances.length - healthyCount} unhealthy
                </p>
              </CardContent>
            </Card>
            <Card>
              <CardHeader className="pb-2">
                <CardDescription>Current leader</CardDescription>
                <CardTitle className="text-lg font-mono truncate">
                  {leader ? leader.instance_id : "—"}
                </CardTitle>
              </CardHeader>
              <CardContent>
                <p className="text-sm text-muted-foreground">
                  {leader ? "Holds the election lock and runs singleton jobs" : "No leader elected"}
                </p>
              </CardContent>
            </Card>
          </div>

          {/* Instances table */}
          <Card>
            <CardHeader>
              <CardTitle className="flex items-center gap-2">
                <Network className="h-5 w-5" />
                Nodes
              </CardTitle>
              <CardDescription>
                Every instance that reported a heartbeat to the cluster store
              </CardDescription>
            </CardHeader>
            <CardContent>
              {instances.length === 0 ? (
                <div className="text-center py-8">
                  <AlertCircle className="h-12 w-12 mx-auto text-muted-foreground mb-4" />
                  <h3 className="text-lg font-medium">No heartbeats recorded</h3>
                  <p className="text-muted-foreground mt-1">
                    Cluster mode is on, but no instance has reported health yet
                  </p>
                </div>
              ) : (
                <Table>
                  <TableHeader>
                    <TableRow>
                      <TableHead>Instance</TableHead>
                      <TableHead>Status</TableHead>
                      <TableHead>Role</TableHead>
                      <TableHead className="text-right">Connections</TableHead>
                      <TableHead className="text-right">Last heartbeat</TableHead>
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    {instances.map((inst) => (
                      <TableRow key={inst.instance_id}>
                        <TableCell className="font-mono">
                          <span className="flex items-center gap-2">
                            {inst.instance_id}
                            {inst.instance_id === status.instance_id && (
                              <Badge variant="outline">this node</Badge>
                            )}
                          </span>
                        </TableCell>
                        <TableCell>{statusBadge(inst.status)}</TableCell>
                        <TableCell>
                          {inst.is_leader ? (
                            <Badge className="bg-amber-500/10 text-amber-500 hover:bg-amber-500/10">
                              <Crown className="mr-1 h-3 w-3" />
                              Leader
                            </Badge>
                          ) : (
                            <span className="text-muted-foreground">Follower</span>
                          )}
                        </TableCell>
                        <TableCell className="text-right">{inst.connections}</TableCell>
                        <TableCell className="text-right text-muted-foreground">
                          {heartbeatAge(inst.last_heartbeat)}
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
            <DialogTitle>Trigger failover?</DialogTitle>
            <DialogDescription>
              This node releases cluster leadership so another healthy instance
              takes over singleton duties (scheduled jobs, alerts). Client
              traffic is not interrupted.
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="outline" onClick={() => setFailoverOpen(false)} disabled={failingOver}>
              Cancel
            </Button>
            <Button variant="destructive" onClick={handleFailover} disabled={failingOver}>
              {failingOver && <RefreshCw className="mr-2 h-4 w-4 animate-spin" />}
              Trigger Failover
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
}
