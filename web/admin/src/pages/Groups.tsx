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
function membershipSummary(g: MailGroup): string {
  if (!g.dynamic) {
    return `${g.members.length} member${g.members.length === 1 ? "" : "s"}`;
  }
  const parts: string[] = [];
  parts.push(g.dynamic_domain ? `domain ${g.dynamic_domain}` : "this domain");
  if (g.dynamic_admin_only === true) parts.push("admins only");
  if (g.dynamic_admin_only === false) parts.push("non-admins only");
  if (g.dynamic_local_pattern) parts.push(`matching "${g.dynamic_local_pattern}"`);
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
      setFormError("Group address is required");
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
      setFormError(errorMessage(err, "Failed to create group"));
    } finally {
      setBusy(false);
    }
  };

  const handleToggle = async (g: MailGroup) => {
    try {
      await updateGroup(g.email, { is_active: !g.is_active });
    } catch (err) {
      toast.error(errorMessage(err, "Failed to update group"));
    }
  };

  const handleDelete = async () => {
    if (!deleteTarget || busy) return;
    setBusy(true);
    try {
      await deleteGroup(deleteTarget.email);
      setDeleteTarget(null);
    } catch (err) {
      toast.error(errorMessage(err, "Failed to delete group"));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="space-y-6">
      <div className="flex flex-col gap-4 sm:flex-row sm:items-center sm:justify-between">
        <div>
          <h1 className="text-3xl font-bold tracking-tight">Mail Groups</h1>
          <p className="text-muted-foreground mt-1">
            Distribution lists that fan mail out to a static member list or a dynamic rule
          </p>
        </div>
        <Button onClick={() => setIsAddDialogOpen(true)}>
          <Plus className="mr-2 h-4 w-4" />
          Add Group
        </Button>
      </div>

      <div className="flex items-center gap-4">
        <div className="relative flex-1 max-w-sm">
          <Search className="absolute left-3 top-1/2 -translate-y-1/2 h-4 w-4 text-muted-foreground" />
          <Input
            placeholder="Search groups..."
            value={searchQuery}
            onChange={(e) => setSearchQuery(e.target.value)}
            className="pl-10"
          />
        </div>
        <Button variant="outline" onClick={() => fetchGroups().catch(() => {})} disabled={loading}>
          <RefreshCw className={loading ? "mr-2 h-4 w-4 animate-spin" : "mr-2 h-4 w-4"} />
          Refresh
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
            <h3 className="text-lg font-medium">No mail groups</h3>
            <p className="text-muted-foreground mt-1">
              Create a group to deliver one address to many recipients.
            </p>
            <Button className="mt-4" onClick={() => setIsAddDialogOpen(true)}>
              <Plus className="mr-2 h-4 w-4" />
              Add Group
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
                      {g.dynamic ? "Dynamic" : "Static"}
                    </Badge>
                  </div>
                  <div className="text-sm text-muted-foreground truncate">{membershipSummary(g)}</div>
                </div>
              </div>
              <div className="flex items-center gap-3">
                <Badge variant="secondary" className="text-[10px]">
                  {g.sender_policy === "anyone" ? "Anyone" : "Internal only"}
                </Badge>
                {!g.is_active && (
                  <Badge variant="secondary" className="text-[10px]">
                    Disabled
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
            <DialogTitle>Add Mail Group</DialogTitle>
            <DialogDescription>
              Mail sent to the group address is delivered to every member.
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
              <Label htmlFor="group-email">Group Address</Label>
              <Input
                id="group-email"
                placeholder="team@example.com"
                value={form.email}
                onChange={(e) => setForm({ ...form, email: e.target.value })}
              />
            </div>
            <div className="space-y-2">
              <Label htmlFor="group-desc">Description</Label>
              <Input
                id="group-desc"
                placeholder="Optional"
                value={form.description}
                onChange={(e) => setForm({ ...form, description: e.target.value })}
              />
            </div>

            <div className="flex items-center justify-between rounded-lg border p-3">
              <div>
                <p className="text-sm font-medium">Dynamic membership</p>
                <p className="text-xs text-muted-foreground">
                  Resolve members from a rule instead of a fixed list
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
                  <Label htmlFor="group-dyndomain">Scan domain</Label>
                  <Input
                    id="group-dyndomain"
                    placeholder="Defaults to the group's domain"
                    value={form.dynamicDomain}
                    onChange={(e) => setForm({ ...form, dynamicDomain: e.target.value })}
                  />
                </div>
                <div className="space-y-2">
                  <Label>Admin filter</Label>
                  <Select
                    value={form.adminFilter}
                    onValueChange={(v) =>
                      setForm({ ...form, adminFilter: (v as typeof form.adminFilter) ?? "any" })
                    }
                  >
                    <SelectTrigger>
                      <SelectValue placeholder="Any account" />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value="any">Any account</SelectItem>
                      <SelectItem value="admins">Admins only</SelectItem>
                      <SelectItem value="nonadmins">Non-admins only</SelectItem>
                    </SelectContent>
                  </Select>
                </div>
                <div className="space-y-2">
                  <Label htmlFor="group-pattern">Local-part pattern</Label>
                  <Input
                    id="group-pattern"
                    placeholder="e.g. sales-*"
                    value={form.localPattern}
                    onChange={(e) => setForm({ ...form, localPattern: e.target.value })}
                  />
                  <p className="text-xs text-muted-foreground">
                    Glob match on the part before @ (leave blank to match all).
                  </p>
                </div>
              </div>
            ) : (
              <div className="space-y-2">
                <Label htmlFor="group-members">Members</Label>
                <textarea
                  id="group-members"
                  className="flex min-h-24 w-full rounded-md border border-input bg-background px-3 py-2 text-sm ring-offset-background placeholder:text-muted-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2"
                  placeholder="one@example.com, two@example.com"
                  value={form.membersText}
                  onChange={(e) => setForm({ ...form, membersText: e.target.value })}
                />
                <p className="text-xs text-muted-foreground">
                  Separate addresses with commas, spaces or new lines.
                </p>
              </div>
            )}

            <div className="space-y-2">
              <Label>Who can send to this group</Label>
              <Select
                value={form.senderPolicy}
                onValueChange={(v) =>
                  setForm({ ...form, senderPolicy: (v as typeof form.senderPolicy) ?? "internal" })
                }
              >
                <SelectTrigger>
                  <SelectValue placeholder="Internal senders only" />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="internal">Internal senders only</SelectItem>
                  <SelectItem value="anyone">Anyone</SelectItem>
                </SelectContent>
              </Select>
            </div>
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={() => setIsAddDialogOpen(false)} disabled={busy}>
              Cancel
            </Button>
            <Button onClick={handleCreate} disabled={busy}>
              Add Group
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* Delete Confirmation Dialog */}
      <Dialog open={deleteTarget !== null} onOpenChange={(open) => { if (!open) setDeleteTarget(null); }}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Delete Mail Group</DialogTitle>
            <DialogDescription>
              Are you sure you want to delete {deleteTarget?.email}? Mail to this address
              will no longer be delivered to its members.
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="outline" onClick={() => setDeleteTarget(null)} disabled={busy}>
              Cancel
            </Button>
            <Button variant="destructive" onClick={handleDelete} disabled={busy}>
              <Trash2 className="mr-2 h-4 w-4" />
              Delete
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
}
