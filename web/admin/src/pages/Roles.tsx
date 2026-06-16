import { useState, useEffect } from "react";
import {
  Shield,
  Plus,
  Search,
  MoreHorizontal,
  Edit,
  Trash2,
  AlertCircle,
  RefreshCw,
  Check,
} from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from "@/components/ui/dialog";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { Label } from "@/components/ui/label";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Skeleton } from "@/components/ui/skeleton";
import { cn } from "@/lib/utils";
import { useRoles } from "@/hooks/useApi";
import { useI18n } from "@/hooks/useI18n";
import type { Role, RolePermission, RoleWithPermissions } from "@/types";

// builtInPermissions maps each permission constant to its i18n key.
const builtInPermissionMeta: Record<string, { labelKey: string; descKey: string }> = {
  SystemAdmin: { labelKey: "roles.systemAdmin", descKey: "roles.systemAdminDesc" },
  SystemAdminRO: { labelKey: "roles.systemAdminRO", descKey: "roles.systemAdminRODesc" },
  DomainAdmin: { labelKey: "roles.domainAdmin", descKey: "roles.domainAdminDesc" },
  DomainAdminRO: { labelKey: "roles.domainAdminRO", descKey: "roles.domainAdminRODesc" },
  OrgAdmin: { labelKey: "roles.orgAdmin", descKey: "roles.orgAdminDesc" },
  DomainPurge: { labelKey: "roles.domainPurge", descKey: "roles.domainPurgeDesc" },
  ResetPasswd: { labelKey: "roles.resetPasswd", descKey: "roles.resetPasswdDesc" },
};

const BUILT_IN_PERMISSIONS = [
  "SystemAdmin",
  "SystemAdminRO",
  "DomainAdmin",
  "DomainAdminRO",
  "OrgAdmin",
  "DomainPurge",
  "ResetPasswd",
];

// BuiltInPermissionsTab renders the list of available built-in permissions.
function BuiltInPermissionsTab() {
  const { t } = useI18n();
  return (
    <div className="space-y-3">
      <p className="text-sm text-muted-foreground">{t("roles.builtInPermissions")}</p>
      <div className="grid gap-3">
        {BUILT_IN_PERMISSIONS.map((perm) => {
          const meta = builtInPermissionMeta[perm];
          return (
            <div key={perm} className="flex items-start gap-3 p-3 rounded-lg border">
              <div className="mt-0.5">
                <Check className="h-4 w-4 text-muted-foreground" />
              </div>
              <div>
                <p className="font-medium text-sm">{meta ? t(meta.labelKey) : perm}</p>
                <p className="text-xs text-muted-foreground">{meta ? t(meta.descKey) : ""}</p>
              </div>
            </div>
          );
        })}
      </div>
    </div>
  );
}

// RoleFormDialog is used for both create and edit.
function RoleFormDialog({
  mode,
  role,
  open,
  onOpenChange,
  onSave,
}: {
  mode: "create" | "edit";
  role?: RoleWithPermissions;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onSave: (name: string, description: string, permissions: string[]) => Promise<void>;
}) {
  const { t } = useI18n();
  const [name, setName] = useState(role?.role.name ?? "");
  const [description, setDescription] = useState(role?.role.description ?? "");
  const [selectedPerms, setSelectedPerms] = useState<string[]>(
    role?.permissions.map((p) => p.permission) ?? []
  );
  const [saving, setSaving] = useState(false);
  const [formError, setFormError] = useState("");

  useEffect(() => {
    if (open) {
      setName(role?.role.name ?? "");
      setDescription(role?.role.description ?? "");
      setSelectedPerms(role?.permissions.map((p) => p.permission) ?? []);
      setFormError("");
    }
  }, [open, role]);

  const togglePerm = (perm: string) => {
    setSelectedPerms((prev) =>
      prev.includes(perm) ? prev.filter((p) => p !== perm) : [...prev, perm]
    );
  };

  const handleSave = async () => {
    if (!name.trim()) {
      setFormError(t("accounts.emailPasswordRequired"));
      return;
    }
    setSaving(true);
    try {
      await onSave(name.trim(), description.trim(), selectedPerms);
      onOpenChange(false);
    } catch (err) {
      setFormError(err instanceof Error ? err.message : String(err));
    } finally {
      setSaving(false);
    }
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>
            {mode === "create" ? t("roles.createTitle") : t("roles.editTitle")}
          </DialogTitle>
          <DialogDescription>
            {mode === "create" ? t("roles.createDescription") : t("roles.editDescription")}
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
            <Label htmlFor="role-name">{t("roles.name")}</Label>
            <Input
              id="role-name"
              placeholder={t("roles.namePlaceholder")}
              value={name}
              onChange={(e) => setName(e.target.value)}
            />
          </div>
          <div className="space-y-2">
            <Label htmlFor="role-description">{t("roles.descriptionLabel")}</Label>
            <Input
              id="role-description"
              placeholder={t("roles.descriptionPlaceholder")}
              value={description}
              onChange={(e) => setDescription(e.target.value)}
            />
          </div>
          <div className="space-y-2">
            <Label>{t("roles.permissions")}</Label>
            <p className="text-xs text-muted-foreground">{t("roles.permissionsDescription")}</p>
            <div className="grid gap-2 pt-2">
              {BUILT_IN_PERMISSIONS.map((perm) => {
                const meta = builtInPermissionMeta[perm];
                const checked = selectedPerms.includes(perm);
                return (
                  <label
                    key={perm}
                    className={cn(
                      "flex items-start gap-3 p-3 rounded-lg border cursor-pointer transition-colors",
                      checked ? "border-primary bg-primary/5" : "hover:bg-muted/50"
                    )}
                  >
                    <input
                      type="checkbox"
                      className="mt-0.5 accent-primary"
                      checked={checked}
                      onChange={() => togglePerm(perm)}
                    />
                    <div>
                      <p className="font-medium text-sm">{meta ? t(meta.labelKey) : perm}</p>
                      <p className="text-xs text-muted-foreground">{meta ? t(meta.descKey) : ""}</p>
                    </div>
                  </label>
                );
              })}
            </div>
          </div>
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>
            {t("common.cancel")}
          </Button>
          <Button onClick={handleSave} disabled={saving}>
            {saving ? t("common.loading") : t("common.saveChanges")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

export function Roles() {
  const { t } = useI18n();
  const { roles, loading, error: _error, fetchRoles, createRole, updateRole, deleteRole, getRolePermissions, setRolePermissions } =
    useRoles();

  const [searchQuery, setSearchQuery] = useState("");
  const [isCreateOpen, setIsCreateOpen] = useState(false);
  const [isEditOpen, setIsEditOpen] = useState(false);
  const [isDeleteOpen, setIsDeleteOpen] = useState(false);
  const [selectedRole, setSelectedRole] = useState<Role | null>(null);
  const [selectedRoleDetail, setSelectedRoleDetail] = useState<RoleWithPermissions | null>(null);
  const [loadingDetail, setLoadingDetail] = useState(false);
  const [formError, setFormError] = useState("");
  const [deleting, setDeleting] = useState(false);

  useEffect(() => {
    fetchRoles();
  }, [fetchRoles]);

  const filteredRoles = roles?.filter((r: Role) =>
    r.name.toLowerCase().includes(searchQuery.toLowerCase())
  );

  const handleEdit = async (role: Role) => {
    setSelectedRole(role);
    setLoadingDetail(true);
    setFormError("");
    try {
      const detail = await getRolePermissions(role.id);
      setSelectedRoleDetail(detail);
      setIsEditOpen(true);
    } catch (err) {
      setFormError(err instanceof Error ? err.message : String(err));
    } finally {
      setLoadingDetail(false);
    }
  };

  const handleDelete = (role: Role) => {
    setSelectedRole(role);
    setIsDeleteOpen(true);
  };

  const handleCreate = async (name: string, description: string, permissions: string[]) => {
    setFormError("");
    try {
      const newRole = await createRole(name, description);
      const perms: RolePermission[] = permissions.map((p) => ({
        id: crypto.randomUUID(),
        role_id: newRole.id,
        permission: p,
      }));
      await setRolePermissions(newRole.id, perms);
      await fetchRoles();
    } catch (err) {
      setFormError(err instanceof Error ? err.message : String(err));
      throw err;
    }
  };

  const handleUpdate = async (name: string, description: string, permissions: string[]) => {
    if (!selectedRole) return;
    setFormError("");
    try {
      await updateRole(selectedRole.id, name, description);
      const perms: RolePermission[] = permissions.map((p) => ({
        id: crypto.randomUUID(),
        role_id: selectedRole.id,
        permission: p,
      }));
      await setRolePermissions(selectedRole.id, perms);
      await fetchRoles();
    } catch (err) {
      setFormError(err instanceof Error ? err.message : String(err));
      throw err;
    }
  };

  const handleDeleteConfirm = async () => {
    if (!selectedRole || deleting) return;
    setDeleting(true);
    try {
      await deleteRole(selectedRole.id);
      setIsDeleteOpen(false);
      setSelectedRole(null);
    } catch (err) {
      setFormError(err instanceof Error ? err.message : String(err));
    } finally {
      setDeleting(false);
    }
  };

  return (
    <div className="space-y-6">
      {/* Header */}
      <div className="flex flex-col gap-4 sm:flex-row sm:items-center sm:justify-between">
        <div>
          <h1 className="text-3xl font-bold tracking-tight">{t("roles.title")}</h1>
          <p className="text-muted-foreground mt-1">{t("roles.description")}</p>
        </div>
        <div className="flex gap-2">
          <Button variant="outline" onClick={() => fetchRoles()} disabled={loading}>
            <RefreshCw className={cn("mr-2 h-4 w-4", loading && "animate-spin")} />
            {t("common.refresh")}
          </Button>
          <Dialog open={isCreateOpen} onOpenChange={setIsCreateOpen}>
            {/* @ts-expect-error asChild prop not typed in Base UI but works at runtime */}
            <DialogTrigger asChild>
              <Button>
                <Plus className="mr-2 h-4 w-4" />
                {t("roles.add")}
              </Button>
            </DialogTrigger>
            <RoleFormDialog
              mode="create"
              open={isCreateOpen}
              onOpenChange={setIsCreateOpen}
              onSave={handleCreate}
            />
          </Dialog>
        </div>
      </div>

      {formError && (
        <Alert variant="destructive">
          <AlertCircle className="h-4 w-4" />
          <AlertDescription>{formError}</AlertDescription>
        </Alert>
      )}

      {/* Search */}
      <div className="flex items-center gap-4">
        <div className="relative flex-1 max-w-sm">
          <Search className="absolute left-3 top-1/2 -translate-y-1/2 h-4 w-4 text-muted-foreground" />
          <Input
            placeholder={t("accounts.searchPlaceholder")}
            value={searchQuery}
            onChange={(e) => setSearchQuery(e.target.value)}
            className="pl-10"
          />
        </div>
      </div>

      {/* Built-in permissions reference */}
      <BuiltInPermissionsTab />

      {/* Roles List */}
      {loading ? (
        <div className="grid gap-4 md:grid-cols-2 lg:grid-cols-3">
          {[1, 2, 3].map((i) => (
            <Card key={i}>
              <CardContent className="p-6">
                <Skeleton className="h-6 w-3/4 mb-4" />
                <Skeleton className="h-4 w-1/2 mb-4" />
                <Skeleton className="h-4 w-3/4" />
              </CardContent>
            </Card>
          ))}
        </div>
      ) : filteredRoles?.length === 0 ? (
        <Card className="text-center py-12">
          <CardContent>
            <Shield className="h-12 w-12 mx-auto text-muted-foreground mb-4" />
            <h3 className="text-lg font-medium">{t("roles.noRoles")}</h3>
            <p className="text-muted-foreground mt-1">{t("roles.noRolesDescription")}</p>
            <Button className="mt-4" onClick={() => setIsCreateOpen(true)}>
              <Plus className="mr-2 h-4 w-4" />
              {t("roles.add")}
            </Button>
          </CardContent>
        </Card>
      ) : (
        <div className="grid gap-4 md:grid-cols-2 lg:grid-cols-3">
          {filteredRoles?.map((role) => (
            <Card key={role.id} className="group">
              <CardHeader className="pb-3">
                <div className="flex items-start justify-between">
                  <div className="flex items-center gap-3">
                    <div className="p-2 rounded-lg bg-gradient-to-br from-violet-500 to-violet-600">
                      <Shield className="h-5 w-5 text-white" />
                    </div>
                    <div className="min-w-0">
                      <CardTitle className="text-base truncate">{role.name}</CardTitle>
                      <CardDescription className="truncate">
                        {role.description || t("roles.descriptionPlaceholder")}
                      </CardDescription>
                    </div>
                  </div>
                  <DropdownMenu>
                    {/* @ts-expect-error asChild prop not typed in Base UI but works at runtime */}
                    <DropdownMenuTrigger asChild>
                      <Button variant="ghost" size="icon" className="h-8 w-8 opacity-0 group-hover:opacity-100 transition-opacity">
                        <MoreHorizontal className="h-4 w-4" />
                      </Button>
                    </DropdownMenuTrigger>
                    <DropdownMenuContent align="end">
                      <DropdownMenuItem
                        onClick={() => handleEdit(role)}
                        disabled={loadingDetail}
                      >
                        <Edit className="mr-2 h-4 w-4" />
                        {t("common.edit")}
                      </DropdownMenuItem>
                      <DropdownMenuSeparator />
                      <DropdownMenuItem
                        onClick={() => handleDelete(role)}
                        className="text-red-600"
                      >
                        <Trash2 className="mr-2 h-4 w-4" />
                        {t("common.delete")}
                      </DropdownMenuItem>
                    </DropdownMenuContent>
                  </DropdownMenu>
                </div>
              </CardHeader>
              <CardContent>
                <p className="text-xs text-muted-foreground">
                  {role.created_at
                    ? new Date(role.created_at).toLocaleDateString()
                    : ""}
                </p>
              </CardContent>
            </Card>
          ))}
        </div>
      )}

      {/* Edit Role Dialog */}
      {selectedRoleDetail && (
        <RoleFormDialog
          mode="edit"
          role={selectedRoleDetail}
          open={isEditOpen}
          onOpenChange={setIsEditOpen}
          onSave={handleUpdate}
        />
      )}

      {/* Delete Confirmation Dialog */}
      <Dialog open={isDeleteOpen} onOpenChange={setIsDeleteOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t("roles.deleteTitle")}</DialogTitle>
            <DialogDescription>
              {t("roles.deleteConfirm", { name: selectedRole?.name ?? "" })}
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="outline" onClick={() => setIsDeleteOpen(false)}>
              {t("common.cancel")}
            </Button>
            <Button variant="destructive" onClick={handleDeleteConfirm} disabled={deleting}>
              <Trash2 className="mr-2 h-4 w-4" />
              {t("common.delete")}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
}
