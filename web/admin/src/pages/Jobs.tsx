import { useEffect, useState } from "react";
import {
  Briefcase,
  RefreshCw,
  Database,
  Upload,
  Download,
  Clock,
  CheckCircle,
  XCircle,
  AlertCircle,
  Loader2,
  Archive,
  Plus,
  ShieldCheck,
  RotateCcw,
  Trash2,
  HardDrive,
} from "lucide-react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Progress } from "@/components/ui/progress";
import { Skeleton } from "@/components/ui/skeleton";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { cn } from "@/lib/utils";
import { useJobs, useBackups } from "@/hooks/useApi";
import type { BackupManifest } from "@/types";
import type { BackupCreateInput, BackupRestoreInput } from "@/hooks/useApi";

function formatBytes(bytes: number): string {
  if (!bytes || bytes < 0) return "0 B";
  const units = ["B", "KB", "MB", "GB", "TB"];
  const i = Math.min(Math.floor(Math.log(bytes) / Math.log(1024)), units.length - 1);
  return `${(bytes / Math.pow(1024, i)).toFixed(i === 0 ? 0 : 1)} ${units[i]}`;
}

export function Jobs() {
  const { jobs, loading, fetchJobs } = useJobs();
  const {
    backups,
    loading: backupsLoading,
    fetchBackups,
    createBackup,
    verifyBackup,
    restoreBackup,
    deleteBackup,
  } = useBackups();

  useEffect(() => {
    fetchJobs().catch(() => {
      /* error surfaced via hook state */
    });
    fetchBackups().catch(() => {
      /* error surfaced via hook state */
    });
  }, [fetchJobs, fetchBackups]);

  // --- Backup dialogs state ---
  const [createOpen, setCreateOpen] = useState(false);
  const [createForm, setCreateForm] = useState<BackupCreateInput>({ type: "full", target: "" });
  const [creating, setCreating] = useState(false);

  const [restoreTarget, setRestoreTarget] = useState<BackupManifest | null>(null);
  const [restoreForm, setRestoreForm] = useState<BackupRestoreInput>({
    mode: "different-user",
    target_user: "",
    overwrite: false,
  });
  const [restoring, setRestoring] = useState(false);

  const [deleteTarget, setDeleteTarget] = useState<BackupManifest | null>(null);
  const [deleting, setDeleting] = useState(false);
  const [verifyingId, setVerifyingId] = useState<string | null>(null);

  const refreshBackups = () => fetchBackups().catch(() => {});

  const needsTarget = createForm.type !== "full";

  const handleCreate = async () => {
    if (needsTarget && !createForm.target?.trim()) {
      toast.error("Target is required for per-user / per-mailbox backups");
      return;
    }
    setCreating(true);
    try {
      await createBackup({ ...createForm, target: createForm.target?.trim() });
      toast.success("Backup created");
      setCreateOpen(false);
      setCreateForm({ type: "full", target: "" });
      refreshBackups();
    } catch (err) {
      toast.error((err as { message?: string }).message || "Backup failed");
    } finally {
      setCreating(false);
    }
  };

  const handleVerify = async (b: BackupManifest) => {
    setVerifyingId(b.id);
    try {
      const result = await verifyBackup(b.id);
      if (result.verified) {
        toast.success("Backup verified — archive is intact");
      } else {
        toast.error(result.message || "Verification failed");
      }
    } catch (err) {
      toast.error((err as { message?: string }).message || "Verification failed");
    } finally {
      setVerifyingId(null);
    }
  };

  const handleRestore = async () => {
    if (!restoreTarget) return;
    if (restoreForm.mode === "different-user" && !restoreForm.target_user?.trim()) {
      toast.error("Target user is required for a different-user restore");
      return;
    }
    setRestoring(true);
    try {
      const result = await restoreBackup(restoreTarget.id, {
        ...restoreForm,
        target_user: restoreForm.target_user?.trim(),
      });
      if (result.status === "completed") {
        toast.success("Restore completed");
      } else {
        toast.error(result.message || "Restore failed");
      }
      setRestoreTarget(null);
    } catch (err) {
      toast.error((err as { message?: string }).message || "Restore failed");
    } finally {
      setRestoring(false);
    }
  };

  const handleDelete = async () => {
    if (!deleteTarget) return;
    setDeleting(true);
    try {
      await deleteBackup(deleteTarget.id);
      toast.success("Backup deleted");
      setDeleteTarget(null);
      refreshBackups();
    } catch (err) {
      toast.error((err as { message?: string }).message || "Delete failed");
    } finally {
      setDeleting(false);
    }
  };

  const getJobTypeLabel = (type: string) => {
    switch (type) {
      case "backfill":
        return "Mailbox Backfill";
      case "migration":
        return "Data Migration";
      case "oab-generation":
        return "OAB Generation";
      case "backup":
        return "Backup";
      case "restore":
        return "Restore";
      default:
        return type;
    }
  };

  const getJobTypeIcon = (type: string) => {
    switch (type) {
      case "backfill":
        return <Database className="h-4 w-4" />;
      case "migration":
        return <Upload className="h-4 w-4" />;
      case "oab-generation":
        return <Download className="h-4 w-4" />;
      case "backup":
        return <Download className="h-4 w-4" />;
      case "restore":
        return <Upload className="h-4 w-4" />;
      default:
        return <Clock className="h-4 w-4" />;
    }
  };

  const getStatusIcon = (status: string) => {
    switch (status) {
      case "pending":
        return <Clock className="h-4 w-4 text-muted-foreground" />;
      case "running":
        return <Loader2 className="h-4 w-4 text-blue-500 animate-spin" />;
      case "completed":
        return <CheckCircle className="h-4 w-4 text-emerald-500" />;
      case "failed":
        return <XCircle className="h-4 w-4 text-red-500" />;
      default:
        return <AlertCircle className="h-4 w-4 text-muted-foreground" />;
    }
  };

  const getStatusColor = (status: string) => {
    switch (status) {
      case "pending":
        return "bg-muted text-muted-foreground";
      case "running":
        return "bg-blue-500/10 text-blue-500";
      case "completed":
        return "bg-emerald-500/10 text-emerald-500";
      case "failed":
        return "bg-red-500/10 text-red-500";
      default:
        return "bg-muted text-muted-foreground";
    }
  };

  const activeJobs = jobs.filter((j) => j.status === "pending" || j.status === "running");
  const completedJobs = jobs.filter((j) => j.status === "completed" || j.status === "failed");

  return (
    <div className="space-y-6">
      {/* Header */}
      <div className="flex flex-col gap-4 sm:flex-row sm:items-center sm:justify-between">
        <div>
          <h1 className="text-3xl font-bold tracking-tight">Jobs &amp; Backups</h1>
          <p className="text-muted-foreground mt-1">
            Manage backups and monitor backfill, migration, and OAB jobs
          </p>
        </div>
      </div>

      <Tabs defaultValue="backups" className="space-y-6">
        <TabsList>
          <TabsTrigger value="backups">
            <Archive className="mr-2 h-4 w-4" />
            Backups
          </TabsTrigger>
          <TabsTrigger value="jobs">
            <Briefcase className="mr-2 h-4 w-4" />
            Jobs
          </TabsTrigger>
        </TabsList>

        {/* ============================ Backups ============================ */}
        <TabsContent value="backups" className="space-y-6">
          <Card>
            <CardHeader>
              <div className="flex flex-col gap-4 sm:flex-row sm:items-center sm:justify-between">
                <div>
                  <CardTitle className="flex items-center gap-2">
                    <HardDrive className="h-5 w-5" />
                    Backups
                  </CardTitle>
                  <CardDescription>
                    Full, per-user, and per-mailbox archives of the canonical store
                  </CardDescription>
                </div>
                <div className="flex items-center gap-2">
                  <Button onClick={() => setCreateOpen(true)}>
                    <Plus className="mr-2 h-4 w-4" />
                    New Backup
                  </Button>
                  <Button variant="outline" onClick={refreshBackups} disabled={backupsLoading}>
                    <RefreshCw className={cn("mr-2 h-4 w-4", backupsLoading && "animate-spin")} />
                    Refresh
                  </Button>
                </div>
              </div>
            </CardHeader>
            <CardContent>
              {backupsLoading && backups.length === 0 ? (
                <div className="space-y-4">
                  <Skeleton className="h-12 w-full" />
                  <Skeleton className="h-12 w-full" />
                </div>
              ) : backups.length === 0 ? (
                <div className="text-center py-8">
                  <Archive className="h-12 w-12 mx-auto text-muted-foreground mb-4" />
                  <h3 className="text-lg font-medium">No backups yet</h3>
                  <p className="text-muted-foreground mt-1">
                    Create a backup to capture mailbox data you can later verify or restore
                  </p>
                </div>
              ) : (
                <Table>
                  <TableHeader>
                    <TableRow>
                      <TableHead>Type</TableHead>
                      <TableHead>Target</TableHead>
                      <TableHead className="text-right">Size</TableHead>
                      <TableHead>Created</TableHead>
                      <TableHead className="text-right">Actions</TableHead>
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    {backups.map((b) => (
                      <TableRow key={b.id}>
                        <TableCell>
                          <Badge variant="secondary">{b.type || "full"}</Badge>
                        </TableCell>
                        <TableCell className="font-mono text-sm">{b.target || "—"}</TableCell>
                        <TableCell className="text-right">{formatBytes(b.size)}</TableCell>
                        <TableCell className="text-muted-foreground">
                          {b.created_at ? new Date(b.created_at).toLocaleString() : "—"}
                        </TableCell>
                        <TableCell>
                          <div className="flex items-center justify-end gap-1">
                            <Button
                              variant="ghost"
                              size="sm"
                              onClick={() => handleVerify(b)}
                              disabled={verifyingId === b.id}
                              title="Verify archive integrity"
                            >
                              {verifyingId === b.id ? (
                                <Loader2 className="h-4 w-4 animate-spin" />
                              ) : (
                                <ShieldCheck className="h-4 w-4" />
                              )}
                            </Button>
                            <Button
                              variant="ghost"
                              size="sm"
                              onClick={() => {
                                setRestoreForm({
                                  mode: "different-user",
                                  target_user: "",
                                  overwrite: false,
                                });
                                setRestoreTarget(b);
                              }}
                              title="Restore"
                            >
                              <RotateCcw className="h-4 w-4" />
                            </Button>
                            <Button
                              variant="ghost"
                              size="sm"
                              onClick={() => setDeleteTarget(b)}
                              title="Delete"
                              className="text-red-500 hover:text-red-500"
                            >
                              <Trash2 className="h-4 w-4" />
                            </Button>
                          </div>
                        </TableCell>
                      </TableRow>
                    ))}
                  </TableBody>
                </Table>
              )}
            </CardContent>
          </Card>
        </TabsContent>

        {/* ============================== Jobs ============================== */}
        <TabsContent value="jobs" className="space-y-6">
          <div className="flex justify-end">
            <Button variant="outline" onClick={() => fetchJobs().catch(() => {})} disabled={loading}>
              <RefreshCw className={cn("mr-2 h-4 w-4", loading && "animate-spin")} />
              Refresh
            </Button>
          </div>

          {/* Active Jobs */}
          <Card>
            <CardHeader>
              <CardTitle className="flex items-center gap-2">
                <Briefcase className="h-5 w-5" />
                Active Jobs
              </CardTitle>
              <CardDescription>Currently running or pending jobs</CardDescription>
            </CardHeader>
            <CardContent>
              {loading ? (
                <div className="space-y-4">
                  <Skeleton className="h-24 w-full" />
                  <Skeleton className="h-24 w-full" />
                </div>
              ) : activeJobs.length === 0 ? (
                <div className="text-center py-8">
                  <CheckCircle className="h-12 w-12 mx-auto text-emerald-500 mb-4" />
                  <h3 className="text-lg font-medium">No active jobs</h3>
                  <p className="text-muted-foreground mt-1">
                    All jobs have completed or there are no pending jobs
                  </p>
                </div>
              ) : (
                <div className="space-y-4">
                  {activeJobs.map((job) => (
                    <div
                      key={job.id}
                      className="p-4 rounded-lg border hover:bg-muted/50 transition-colors"
                    >
                      <div className="flex items-center justify-between mb-4">
                        <div className="flex items-center gap-3">
                          <div className={cn("p-2 rounded-lg", getStatusColor(job.status))}>
                            {getJobTypeIcon(job.type)}
                          </div>
                          <div>
                            <div className="font-medium">{getJobTypeLabel(job.type)}</div>
                            <div className="text-sm text-muted-foreground">
                              {job.mailbox ? `Mailbox: ${job.mailbox}` : "System job"}
                              {job.startedAt && (
                                <span> | Started: {new Date(job.startedAt).toLocaleString()}</span>
                              )}
                            </div>
                          </div>
                        </div>
                        <div className="flex items-center gap-2">
                          {getStatusIcon(job.status)}
                          <Badge
                            variant="secondary"
                            className={cn(
                              job.status === "running" && "bg-blue-500/10 text-blue-500",
                              job.status === "pending" && "bg-muted text-muted-foreground"
                            )}
                          >
                            {job.status}
                          </Badge>
                        </div>
                      </div>
                      <div className="space-y-2">
                        <div className="flex items-center justify-between text-sm">
                          <span>Progress</span>
                          <span>{job.progress}%</span>
                        </div>
                        <Progress value={job.progress} className="h-2" />
                      </div>
                    </div>
                  ))}
                </div>
              )}
            </CardContent>
          </Card>

          {/* Job History */}
          <Card>
            <CardHeader>
              <CardTitle className="flex items-center gap-2">
                <Clock className="h-5 w-5" />
                Job History
              </CardTitle>
              <CardDescription>Completed and failed jobs</CardDescription>
            </CardHeader>
            <CardContent>
              {loading ? (
                <div className="space-y-4">
                  <Skeleton className="h-16 w-full" />
                  <Skeleton className="h-16 w-full" />
                </div>
              ) : completedJobs.length === 0 ? (
                <div className="text-center py-8">
                  <Clock className="h-12 w-12 mx-auto text-muted-foreground mb-4" />
                  <h3 className="text-lg font-medium">No job history</h3>
                  <p className="text-muted-foreground mt-1">
                    Completed and failed jobs will appear here
                  </p>
                </div>
              ) : (
                <div className="space-y-3">
                  {completedJobs.map((job) => (
                    <div
                      key={job.id}
                      className="flex items-center justify-between p-4 rounded-lg border hover:bg-muted/50 transition-colors"
                    >
                      <div className="flex items-center gap-3">
                        <div className={cn("p-2 rounded-lg", getStatusColor(job.status))}>
                          {job.status === "completed" ? (
                            <CheckCircle className="h-4 w-4" />
                          ) : (
                            <XCircle className="h-4 w-4" />
                          )}
                        </div>
                        <div>
                          <div className="font-medium">{getJobTypeLabel(job.type)}</div>
                          <div className="text-sm text-muted-foreground">
                            {job.mailbox ? `Mailbox: ${job.mailbox}` : "System job"}
                            {job.completedAt && (
                              <span> | Completed: {new Date(job.completedAt).toLocaleString()}</span>
                            )}
                          </div>
                          {job.error && (
                            <div className="text-xs text-red-500 mt-1">Error: {job.error}</div>
                          )}
                        </div>
                      </div>
                      <div className="flex items-center gap-2">
                        <Progress value={job.progress} className="w-20 h-2" />
                        <Badge
                          variant="secondary"
                          className={cn(
                            job.status === "completed" && "bg-emerald-500/10 text-emerald-500",
                            job.status === "failed" && "bg-red-500/10 text-red-500"
                          )}
                        >
                          {job.status}
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

      {/* Create backup dialog */}
      <Dialog open={createOpen} onOpenChange={setCreateOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>New backup</DialogTitle>
            <DialogDescription>
              Create a backup of the whole store or a single user / mailbox.
            </DialogDescription>
          </DialogHeader>
          <div className="space-y-4 py-2">
            <div className="space-y-2">
              <Label>Type</Label>
              <Select
                value={createForm.type}
                onValueChange={(v) =>
                  setCreateForm((f) => ({ ...f, type: (v as BackupCreateInput["type"]) ?? "full" }))
                }
              >
                <SelectTrigger>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="full">Full (entire store)</SelectItem>
                  <SelectItem value="per-user">Per user</SelectItem>
                  <SelectItem value="per-mailbox">Per mailbox</SelectItem>
                </SelectContent>
              </Select>
            </div>
            {needsTarget && (
              <div className="space-y-2">
                <Label>
                  {createForm.type === "per-mailbox" ? "Target (user/mailbox)" : "Target user"}
                </Label>
                <Input
                  placeholder={
                    createForm.type === "per-mailbox"
                      ? "qa.alice@local.test/Archive"
                      : "qa.alice@local.test"
                  }
                  value={createForm.target ?? ""}
                  onChange={(e) => setCreateForm((f) => ({ ...f, target: e.target.value }))}
                />
              </div>
            )}
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={() => setCreateOpen(false)} disabled={creating}>
              Cancel
            </Button>
            <Button onClick={handleCreate} disabled={creating}>
              {creating && <Loader2 className="mr-2 h-4 w-4 animate-spin" />}
              Create
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* Restore dialog */}
      <Dialog open={!!restoreTarget} onOpenChange={(open) => !open && setRestoreTarget(null)}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Restore backup</DialogTitle>
            <DialogDescription className="break-all">
              {restoreTarget?.filename}
            </DialogDescription>
          </DialogHeader>
          <div className="space-y-4 py-2">
            <div className="space-y-2">
              <Label>Mode</Label>
              <Select
                value={restoreForm.mode}
                onValueChange={(v) =>
                  setRestoreForm((f) => ({
                    ...f,
                    mode: (v as BackupRestoreInput["mode"]) ?? "different-user",
                  }))
                }
              >
                <SelectTrigger>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="different-user">Different user (safe)</SelectItem>
                  <SelectItem value="merge">Merge into original</SelectItem>
                  <SelectItem value="overwrite">Overwrite original</SelectItem>
                </SelectContent>
              </Select>
            </div>
            {restoreForm.mode === "different-user" && (
              <div className="space-y-2">
                <Label>Target user</Label>
                <Input
                  placeholder="qa.restore@local.test"
                  value={restoreForm.target_user ?? ""}
                  onChange={(e) =>
                    setRestoreForm((f) => ({ ...f, target_user: e.target.value }))
                  }
                />
              </div>
            )}
            {restoreForm.mode !== "different-user" && (
              <div className="flex items-center justify-between rounded-lg border p-3">
                <div>
                  <Label>Overwrite existing files</Label>
                  <p className="text-xs text-muted-foreground">
                    Replace messages that already exist in the target
                  </p>
                </div>
                <Switch
                  checked={!!restoreForm.overwrite}
                  onCheckedChange={(c) => setRestoreForm((f) => ({ ...f, overwrite: c }))}
                />
              </div>
            )}
            {restoreForm.mode === "overwrite" && (
              <p className="text-xs text-amber-500">
                Overwrite mode replaces the original mailbox contents. Prefer a
                different-user restore to inspect data safely first.
              </p>
            )}
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={() => setRestoreTarget(null)} disabled={restoring}>
              Cancel
            </Button>
            <Button onClick={handleRestore} disabled={restoring}>
              {restoring && <Loader2 className="mr-2 h-4 w-4 animate-spin" />}
              Restore
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* Delete confirm */}
      <Dialog open={!!deleteTarget} onOpenChange={(open) => !open && setDeleteTarget(null)}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Delete backup?</DialogTitle>
            <DialogDescription className="break-all">
              This removes the backup record {deleteTarget?.filename}. This cannot be undone.
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="outline" onClick={() => setDeleteTarget(null)} disabled={deleting}>
              Cancel
            </Button>
            <Button variant="destructive" onClick={handleDelete} disabled={deleting}>
              {deleting && <Loader2 className="mr-2 h-4 w-4 animate-spin" />}
              Delete
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
}
