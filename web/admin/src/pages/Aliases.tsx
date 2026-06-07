import { useState, useEffect } from "react";
import { AtSign, Plus, Search, Trash2, AlertCircle, RefreshCw } from "lucide-react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Card, CardContent } from "@/components/ui/card";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Badge } from "@/components/ui/badge";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Skeleton } from "@/components/ui/skeleton";
import { useAliases } from "@/hooks/useApi";
import { useI18n } from "@/hooks/useI18n";
import type { Alias } from "@/types";

// useApi rejects with a plain { message, status } object rather than an Error,
// so unwrap that shape (and a genuine Error) to recover the server's reason.
function errorMessage(err: unknown, fallback: string): string {
  if (err instanceof Error) return err.message;
  if (typeof err === "object" && err !== null && "message" in err) {
    const msg = (err as { message?: unknown }).message;
    if (typeof msg === "string" && msg) return msg;
  }
  return fallback;
}

export function Aliases() {
  const { t } = useI18n();
  const { aliases, loading, fetchAliases, createAlias, updateAlias, deleteAlias } = useAliases();

  const [searchQuery, setSearchQuery] = useState("");
  const [isAddDialogOpen, setIsAddDialogOpen] = useState(false);
  const [newAlias, setNewAlias] = useState("");
  const [newTarget, setNewTarget] = useState("");
  const [formError, setFormError] = useState("");
  const [deleteTarget, setDeleteTarget] = useState<Alias | null>(null);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    fetchAliases().catch(() => {});
  }, [fetchAliases]);

  const filtered = aliases?.filter(
    (a) =>
      a.alias.toLowerCase().includes(searchQuery.toLowerCase()) ||
      a.target.toLowerCase().includes(searchQuery.toLowerCase())
  );

  const handleCreate = async () => {
    setFormError("");
    if (!newAlias || !newTarget) {
      setFormError(t("aliases.aliasAndTargetRequired"));
      return;
    }
    try {
      await createAlias(newAlias, newTarget);
      setIsAddDialogOpen(false);
      setNewAlias("");
      setNewTarget("");
    } catch (err) {
      setFormError(errorMessage(err, t("aliases.createFailed")));
    }
  };

  const handleToggle = async (alias: Alias) => {
    try {
      await updateAlias(alias.alias, { target: alias.target, is_active: !alias.is_active });
    } catch (err) {
      toast.error(errorMessage(err, t("aliases.updateFailed")));
    }
  };

  const handleDelete = async () => {
    if (!deleteTarget || busy) return;
    setBusy(true);
    try {
      await deleteAlias(deleteTarget.alias);
      setDeleteTarget(null);
    } catch (err) {
      toast.error(errorMessage(err, t("aliases.deleteFailed")));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="space-y-6">
      <div className="flex flex-col gap-4 sm:flex-row sm:items-center sm:justify-between">
        <div>
          <h1 className="text-3xl font-bold tracking-tight">{t("aliases.title")}</h1>
          <p className="text-muted-foreground mt-1">
            {t("aliases.subtitle")}
          </p>
        </div>
        <Button onClick={() => setIsAddDialogOpen(true)}>
          <Plus className="mr-2 h-4 w-4" />
          {t("aliases.addAlias")}
        </Button>
      </div>

      <div className="flex items-center gap-4">
        <div className="relative flex-1 max-w-sm">
          <Search className="absolute left-3 top-1/2 -translate-y-1/2 h-4 w-4 text-muted-foreground" />
          <Input
            placeholder={t("aliases.searchPlaceholder")}
            value={searchQuery}
            onChange={(e) => setSearchQuery(e.target.value)}
            className="pl-10"
          />
        </div>
        <Button variant="outline" onClick={() => fetchAliases().catch(() => {})} disabled={loading}>
          <RefreshCw className={loading ? "mr-2 h-4 w-4 animate-spin" : "mr-2 h-4 w-4"} />
          {t("common.refresh")}
        </Button>
      </div>

      {loading ? (
        <div className="space-y-3">
          <Skeleton className="h-16 w-full" />
          <Skeleton className="h-16 w-full" />
        </div>
      ) : !filtered || filtered.length === 0 ? (
        <Card>
          <CardContent className="flex flex-col items-center justify-center py-16 text-center">
            <AtSign className="h-12 w-12 text-muted-foreground mb-4" />
            <h3 className="text-lg font-medium">{t("aliases.emptyTitle")}</h3>
            <p className="text-muted-foreground mt-1">
              {t("aliases.emptyDescription")}
            </p>
            <Button className="mt-4" onClick={() => setIsAddDialogOpen(true)}>
              <Plus className="mr-2 h-4 w-4" />
              {t("aliases.addAlias")}
            </Button>
          </CardContent>
        </Card>
      ) : (
        <div className="space-y-2">
          {filtered.map((alias) => (
            <div
              key={alias.alias}
              className="flex items-center justify-between rounded-lg border bg-card p-4"
            >
              <div className="flex items-center gap-3 min-w-0">
                <div className="p-2 rounded-lg bg-violet-500/10">
                  <AtSign className="h-4 w-4 text-violet-500" />
                </div>
                <div className="min-w-0">
                  <div className="font-medium truncate">{alias.alias}</div>
                  <div className="text-sm text-muted-foreground truncate">→ {alias.target}</div>
                </div>
              </div>
              <div className="flex items-center gap-3">
                {!alias.is_active && (
                  <Badge variant="secondary" className="text-[10px]">
                    {t("common.disabled")}
                  </Badge>
                )}
                <Switch checked={alias.is_active} onCheckedChange={() => handleToggle(alias)} />
                <Button
                  variant="ghost"
                  size="icon"
                  className="h-8 w-8 text-destructive"
                  onClick={() => setDeleteTarget(alias)}
                >
                  <Trash2 className="h-4 w-4" />
                </Button>
              </div>
            </div>
          ))}
        </div>
      )}

      {/* Add Alias Dialog */}
      <Dialog open={isAddDialogOpen} onOpenChange={setIsAddDialogOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t("aliases.addAlias")}</DialogTitle>
            <DialogDescription>
              {t("aliases.addDialogDescription")}
            </DialogDescription>
          </DialogHeader>
          {formError && (
            <Alert variant="destructive">
              <AlertCircle className="h-4 w-4" />
              <AlertDescription>{formError}</AlertDescription>
            </Alert>
          )}
          <div className="space-y-4 py-4">
            <div className="space-y-2">
              <Label htmlFor="alias-address">{t("aliases.aliasAddress")}</Label>
              <Input
                id="alias-address"
                placeholder="sales@example.com"
                value={newAlias}
                onChange={(e) => setNewAlias(e.target.value)}
              />
            </div>
            <div className="space-y-2">
              <Label htmlFor="alias-target">{t("aliases.targetAccount")}</Label>
              <Input
                id="alias-target"
                placeholder="user@example.com"
                value={newTarget}
                onChange={(e) => setNewTarget(e.target.value)}
              />
            </div>
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={() => setIsAddDialogOpen(false)}>
              {t("common.cancel")}
            </Button>
            <Button onClick={handleCreate}>{t("aliases.addAlias")}</Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* Delete Confirmation Dialog */}
      <Dialog open={deleteTarget !== null} onOpenChange={(open) => { if (!open) setDeleteTarget(null); }}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t("aliases.deleteAlias")}</DialogTitle>
            <DialogDescription>
              {t("aliases.deleteConfirm", { alias: deleteTarget?.alias ?? "" })}
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="outline" onClick={() => setDeleteTarget(null)}>
              {t("common.cancel")}
            </Button>
            <Button variant="destructive" onClick={handleDelete} disabled={busy}>
              <Trash2 className="mr-2 h-4 w-4" />
              {t("common.delete")}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
}
