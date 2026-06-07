import { useState, useEffect } from "react";
import { UsersRound, Plus, Search, Trash2, AlertCircle, RefreshCw, Zap, List } from "lucide-react";
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
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Badge } from "@/components/ui/badge";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Skeleton } from "@/components/ui/skeleton";
import { useMailGroups } from "@/hooks/useApi";
import { useI18n } from "@/hooks/useI18n";
import type { MailGroup, MailGroupInput } from "@/types";

function errorMessage(err: unknown, fallback: string): string {
  if (err instanceof Error) return err.message;
  if (typeof err === "object" && err !== null && "message" in err) {
    const msg = (err as { message?: unknown }).message;
    if (typeof msg === "string" && msg) return msg;
  }
  return fallback;
}

function parseMembers(text: string): string[] {
  return text
    .split(/[\s,;]+/)
    .map((m) => m.trim())
    .filter(Boolean);
}

// summary describes a group's membership for the list row.
function membershipSummary(
  g: MailGroup,
  t: (key: string, params?: Record<string, string>) => string
): string {
  if (!g.dynamic) {
    return g.members.length === 1
      ? t("groups.memberSingular", { count: String(g.members.length) })
      : t("groups.memberPlural", { count: String(g.members.length) });
  }
  const parts: string[] = [];
  parts.push(
    g.dynamic_domain
      ? t("groups.summaryDomain", { domain: g.dynamic_domain })
      : t("groups.summaryThisDomain")
  );
  if (g.dynamic_admin_only === true) parts.push(t("groups.summaryAdminsOnly"));
  if (g.dynamic_admin_only === false) parts.push(t("groups.summaryNonAdminsOnly"));
  if (g.dynamic_local_pattern)
    parts.push(t("groups.summaryMatching", { pattern: g.dynamic_local_pattern }));
  return parts.join(", ");
}

const emptyForm = {
  email: "",
  description: "",
  dynamic: false,
  senderPolicy: "internal" as "internal" | "anyone",
  membersText: "",
  dynamicDomain: "",
  adminFilter: "any" as "any" | "admins" | "nonadmins",
  localPattern: "",
};

export function Groups() {
  const { t } = useI18n();
  const { groups, loading, fetchGroups, createGroup, updateGroup, deleteGroup } = useMailGroups();

  const [searchQuery, setSearchQuery] = useState("");
  const [isAddDialogOpen, setIsAddDialogOpen] = useState(false);
  const [form, setForm] = useState(emptyForm);
  const [formError, setFormError] = useState("");
  const [deleteTarget, setDeleteTarget] = useState<MailGroup | null>(null);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    fetchGroups().catch(() => {});
  }, [fetchGroups]);

  const filtered = groups?.filter((g) =>
    g.email.toLowerCase().includes(searchQuery.toLowerCase())
  );

  const handleCreate = async () => {
    setFormError("");
    if (!form.email) {
      setFormError(t("groups.addressRequired"));
      return;
    }
    const input: MailGroupInput = {
      email: form.email,
      description: form.description || undefined,
      dynamic: form.dynamic,
      sender_policy: form.senderPolicy,
    };
    if (form.dynamic) {
      input.dynamic_domain = form.dynamicDomain || undefined;
      input.dynamic_local_pattern = form.localPattern || undefined;
      if (form.adminFilter === "admins") input.dynamic_admin_only = true;
      else if (form.adminFilter === "nonadmins") input.dynamic_admin_only = false;
    } else {
      input.members = parseMembers(form.membersText);
    }
    setBusy(true);
    try {
      await createGroup(input);
      setIsAddDialogOpen(false);
      setForm(emptyForm);
    } catch (err) {
      setFormError(errorMessage(err, t("groups.createFailed")));
    } finally {
      setBusy(false);
    }
  };

  const handleToggle = async (g: MailGroup) => {
    try {
      await updateGroup(g.email, { is_active: !g.is_active });
    } catch (err) {
      toast.error(errorMessage(err, t("groups.updateFailed")));
    }
  };

  const handleDelete = async () => {
    if (!deleteTarget || busy) return;
    setBusy(true);
    try {
      await deleteGroup(deleteTarget.email);
      setDeleteTarget(null);
    } catch (err) {
      toast.error(errorMessage(err, t("groups.deleteFailed")));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="space-y-6">
      <div className="flex flex-col gap-4 sm:flex-row sm:items-center sm:justify-between">
        <div>
          <h1 className="text-3xl font-bold tracking-tight">{t("groups.title")}</h1>
          <p className="text-muted-foreground mt-1">
            {t("groups.description")}
          </p>
        </div>
        <Button onClick={() => setIsAddDialogOpen(true)}>
          <Plus className="mr-2 h-4 w-4" />
          {t("groups.addGroup")}
        </Button>
      </div>

      <div className="flex items-center gap-4">
        <div className="relative flex-1 max-w-sm">
          <Search className="absolute left-3 top-1/2 -translate-y-1/2 h-4 w-4 text-muted-foreground" />
          <Input
            placeholder={t("groups.searchPlaceholder")}
            value={searchQuery}
            onChange={(e) => setSearchQuery(e.target.value)}
            className="pl-10"
          />
        </div>
        <Button variant="outline" onClick={() => fetchGroups().catch(() => {})} disabled={loading}>
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
            <UsersRound className="h-12 w-12 text-muted-foreground mb-4" />
            <h3 className="text-lg font-medium">{t("groups.emptyTitle")}</h3>
            <p className="text-muted-foreground mt-1">
              {t("groups.emptyDescription")}
            </p>
            <Button className="mt-4" onClick={() => setIsAddDialogOpen(true)}>
              <Plus className="mr-2 h-4 w-4" />
              {t("groups.addGroup")}
            </Button>
          </CardContent>
        </Card>
      ) : (
        <div className="space-y-2">
          {filtered.map((g) => (
            <div key={g.email} className="flex items-center justify-between rounded-lg border bg-card p-4">
              <div className="flex items-center gap-3 min-w-0">
                <div className={g.dynamic ? "p-2 rounded-lg bg-amber-500/10" : "p-2 rounded-lg bg-sky-500/10"}>
                  {g.dynamic ? (
                    <Zap className="h-4 w-4 text-amber-500" />
                  ) : (
                    <List className="h-4 w-4 text-sky-500" />
                  )}
                </div>
                <div className="min-w-0">
                  <div className="flex items-center gap-2">
                    <span className="font-medium truncate">{g.email}</span>
                    <Badge variant="outline" className="text-[10px]">
                      {g.dynamic ? t("groups.dynamic") : t("groups.static")}
                    </Badge>
                  </div>
                  <div className="text-sm text-muted-foreground truncate">{membershipSummary(g, t)}</div>
                </div>
              </div>
              <div className="flex items-center gap-3">
                <Badge variant="secondary" className="text-[10px]">
                  {g.sender_policy === "anyone" ? t("groups.anyone") : t("groups.internalOnly")}
                </Badge>
                {!g.is_active && (
                  <Badge variant="secondary" className="text-[10px]">
                    {t("common.disabled")}
                  </Badge>
                )}
                <Switch checked={g.is_active} onCheckedChange={() => handleToggle(g)} />
                <Button
                  variant="ghost"
                  size="icon"
                  className="h-8 w-8 text-destructive"
                  onClick={() => setDeleteTarget(g)}
                >
                  <Trash2 className="h-4 w-4" />
                </Button>
              </div>
            </div>
          ))}
        </div>
      )}

      {/* Add Group Dialog */}
      <Dialog open={isAddDialogOpen} onOpenChange={setIsAddDialogOpen}>
        <DialogContent className="sm:max-w-lg">
          <DialogHeader>
            <DialogTitle>{t("groups.addDialogTitle")}</DialogTitle>
            <DialogDescription>
              {t("groups.addDialogDescription")}
            </DialogDescription>
          </DialogHeader>
          {formError && (
            <Alert variant="destructive">
              <AlertCircle className="h-4 w-4" />
              <AlertDescription>{formError}</AlertDescription>
            </Alert>
          )}
          <div className="space-y-4 py-2">
            <div className="space-y-2">
              <Label htmlFor="group-email">{t("groups.groupAddress")}</Label>
              <Input
                id="group-email"
                placeholder="team@example.com"
                value={form.email}
                onChange={(e) => setForm({ ...form, email: e.target.value })}
              />
            </div>
            <div className="space-y-2">
              <Label htmlFor="group-desc">{t("common.description")}</Label>
              <Input
                id="group-desc"
                placeholder={t("common.optional")}
                value={form.description}
                onChange={(e) => setForm({ ...form, description: e.target.value })}
              />
            </div>

            <div className="flex items-center justify-between rounded-lg border p-3">
              <div>
                <p className="text-sm font-medium">{t("groups.dynamicMembership")}</p>
                <p className="text-xs text-muted-foreground">
                  {t("groups.dynamicMembershipHint")}
                </p>
              </div>
              <Switch
                checked={form.dynamic}
                onCheckedChange={(checked) => setForm({ ...form, dynamic: checked })}
              />
            </div>

            {form.dynamic ? (
              <div className="space-y-4 rounded-lg border border-dashed p-3">
                <div className="space-y-2">
                  <Label htmlFor="group-dyndomain">{t("groups.scanDomain")}</Label>
                  <Input
                    id="group-dyndomain"
                    placeholder={t("groups.scanDomainPlaceholder")}
                    value={form.dynamicDomain}
                    onChange={(e) => setForm({ ...form, dynamicDomain: e.target.value })}
                  />
                </div>
                <div className="space-y-2">
                  <Label>{t("groups.adminFilter")}</Label>
                  <Select
                    value={form.adminFilter}
                    onValueChange={(v) =>
                      setForm({ ...form, adminFilter: (v as typeof form.adminFilter) ?? "any" })
                    }
                  >
                    <SelectTrigger>
                      <SelectValue placeholder={t("groups.anyAccount")} />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value="any">{t("groups.anyAccount")}</SelectItem>
                      <SelectItem value="admins">{t("groups.adminsOnly")}</SelectItem>
                      <SelectItem value="nonadmins">{t("groups.nonAdminsOnly")}</SelectItem>
                    </SelectContent>
                  </Select>
                </div>
                <div className="space-y-2">
                  <Label htmlFor="group-pattern">{t("groups.localPattern")}</Label>
                  <Input
                    id="group-pattern"
                    placeholder={t("groups.localPatternPlaceholder")}
                    value={form.localPattern}
                    onChange={(e) => setForm({ ...form, localPattern: e.target.value })}
                  />
                  <p className="text-xs text-muted-foreground">
                    {t("groups.localPatternHint")}
                  </p>
                </div>
              </div>
            ) : (
              <div className="space-y-2">
                <Label htmlFor="group-members">{t("groups.members")}</Label>
                <textarea
                  id="group-members"
                  className="flex min-h-24 w-full rounded-md border border-input bg-background px-3 py-2 text-sm ring-offset-background placeholder:text-muted-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2"
                  placeholder="one@example.com, two@example.com"
                  value={form.membersText}
                  onChange={(e) => setForm({ ...form, membersText: e.target.value })}
                />
                <p className="text-xs text-muted-foreground">
                  {t("groups.membersHint")}
                </p>
              </div>
            )}

            <div className="space-y-2">
              <Label>{t("groups.senderPolicyLabel")}</Label>
              <Select
                value={form.senderPolicy}
                onValueChange={(v) =>
                  setForm({ ...form, senderPolicy: (v as typeof form.senderPolicy) ?? "internal" })
                }
              >
                <SelectTrigger>
                  <SelectValue placeholder={t("groups.internalSendersOnly")} />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="internal">{t("groups.internalSendersOnly")}</SelectItem>
                  <SelectItem value="anyone">{t("groups.anyone")}</SelectItem>
                </SelectContent>
              </Select>
            </div>
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={() => setIsAddDialogOpen(false)} disabled={busy}>
              {t("common.cancel")}
            </Button>
            <Button onClick={handleCreate} disabled={busy}>
              {t("groups.addGroup")}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* Delete Confirmation Dialog */}
      <Dialog open={deleteTarget !== null} onOpenChange={(open) => { if (!open) setDeleteTarget(null); }}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t("groups.deleteDialogTitle")}</DialogTitle>
            <DialogDescription>
              {t("groups.deleteDialogDescription", { email: deleteTarget?.email ?? "" })}
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="outline" onClick={() => setDeleteTarget(null)} disabled={busy}>
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
