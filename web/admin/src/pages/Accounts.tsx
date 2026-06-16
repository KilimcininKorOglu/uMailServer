import { useState, useEffect } from "react";
import {
  Users,
  Plus,
  Search,
  MoreHorizontal,
  Edit,
  Trash2,
  Shield,
  Mail,
  AlertCircle,
  RefreshCw,
  Key,
  HardDrive,
  Smartphone,
  UserCog,
  X,
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
import { Badge } from "@/components/ui/badge";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Skeleton } from "@/components/ui/skeleton";
import { Progress } from "@/components/ui/progress";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { useAccounts, useAccountRoles, useRoles } from "@/hooks/useApi";
import { useI18n } from "@/hooks/useI18n";
import { EASDevicesDialog } from "@/components/EASDevicesDialog";
import { cn } from "@/lib/utils";
import type { Account, MailScopePolicy, Role } from "@/types";

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

// MailScopeFields renders the per-account send/receive scope selectors shared by
// the create and edit dialogs: "anyone" (unrestricted) vs "internal" (locally
// hosted domains only) for each direction.
function MailScopeFields({
  t,
  idPrefix,
  sendPolicy,
  receivePolicy,
  onSendChange,
  onReceiveChange,
}: {
  t: (key: string, params?: Record<string, string>) => string;
  idPrefix: string;
  sendPolicy: MailScopePolicy;
  receivePolicy: MailScopePolicy;
  onSendChange: (v: MailScopePolicy) => void;
  onReceiveChange: (v: MailScopePolicy) => void;
}) {
  return (
    <div className="space-y-3 pt-4 border-t">
      <div className="space-y-0.5">
        <Label>{t("accounts.mailScope")}</Label>
        <p className="text-xs text-muted-foreground">{t("accounts.mailScopeHint")}</p>
      </div>
      <div className="grid grid-cols-2 gap-3">
        <div className="space-y-2">
          <Label htmlFor={`${idPrefix}-send-policy`}>{t("accounts.sendScope")}</Label>
          <Select value={sendPolicy} onValueChange={(v) => onSendChange(v as MailScopePolicy)}>
            <SelectTrigger id={`${idPrefix}-send-policy`}>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="anyone">{t("accounts.scopeAnyone")}</SelectItem>
              <SelectItem value="internal">{t("accounts.scopeInternal")}</SelectItem>
            </SelectContent>
          </Select>
        </div>
        <div className="space-y-2">
          <Label htmlFor={`${idPrefix}-receive-policy`}>{t("accounts.receiveScope")}</Label>
          <Select value={receivePolicy} onValueChange={(v) => onReceiveChange(v as MailScopePolicy)}>
            <SelectTrigger id={`${idPrefix}-receive-policy`}>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="anyone">{t("accounts.scopeAnyone")}</SelectItem>
              <SelectItem value="internal">{t("accounts.scopeInternal")}</SelectItem>
            </SelectContent>
          </Select>
        </div>
      </div>
    </div>
  );
}

export function Accounts() {
  const { t } = useI18n();
  const {
    accounts,
    loading,
    error: _error,
    fetchAccounts,
    createAccount,
    updateAccount,
    deleteAccount,
  } = useAccounts();
  const { roles: allRoles } = useRoles();
  const { roles: accountRoles, fetchAccountRoles, assignRole, removeRole } = useAccountRoles();

  const [searchQuery, setSearchQuery] = useState("");
  const [isAddDialogOpen, setIsAddDialogOpen] = useState(false);
  const [isDeleteDialogOpen, setIsDeleteDialogOpen] = useState(false);
  const [isEditDialogOpen, setIsEditDialogOpen] = useState(false);
  const [isDevicesDialogOpen, setIsDevicesDialogOpen] = useState(false);
  const [isRolesDialogOpen, setIsRolesDialogOpen] = useState(false);
  const [devicesAccount, setDevicesAccount] = useState<Account | null>(null);
  const [selectedAccount, setSelectedAccount] = useState<Account | null>(null);
  const [rolesAccount, setRolesAccount] = useState<Account | null>(null);
  const [newAccountEmail, setNewAccountEmail] = useState("");
  const [newAccountPassword, setNewAccountPassword] = useState("");
  const [newAccountIsAdmin, setNewAccountIsAdmin] = useState(false);
  const [newAccountQuotaMB, setNewAccountQuotaMB] = useState(0);
  const [newAccountAvatar, setNewAccountAvatar] = useState("");
  const [newAccountProfile, setNewAccountProfile] = useState({ display_name: "", title: "", department: "", phone: "" });
  const [newAccountSendPolicy, setNewAccountSendPolicy] = useState<MailScopePolicy>("anyone");
  const [newAccountReceivePolicy, setNewAccountReceivePolicy] = useState<MailScopePolicy>("anyone");
  const [requirePasswordChangeOnReset, setRequirePasswordChangeOnReset] = useState(true);
  const [originalIsAdmin, setOriginalIsAdmin] = useState(false);
  const [currentAdminPassword, setCurrentAdminPassword] = useState("");
  const [formError, setFormError] = useState("");
  const [isDeleting, setIsDeleting] = useState(false);
  const [rolesLoading, setRolesLoading] = useState(false);
  const [assigningRole, setAssigningRole] = useState(false);

  useEffect(() => {
    fetchAccounts();
  }, [fetchAccounts]);

  const filteredAccounts = accounts?.filter((a: Account) =>
    a.email.toLowerCase().includes(searchQuery.toLowerCase())
  );

  const handleCreateAccount = async () => {
    setFormError("");
    if (!newAccountEmail || !newAccountPassword) {
      setFormError(t("accounts.emailPasswordRequired"));
      return;
    }

    try {
      // The backend stores the quota in bytes; the admin enters it in MB
      // (0 = unlimited, matching the server's "no limit" semantics).
      await createAccount(
        newAccountEmail,
        newAccountPassword,
        newAccountIsAdmin,
        newAccountQuotaMB * 1024 * 1024,
        newAccountAvatar || undefined,
        { ...newAccountProfile, send_policy: newAccountSendPolicy, receive_policy: newAccountReceivePolicy }
      );
      setIsAddDialogOpen(false);
      setNewAccountEmail("");
      setNewAccountPassword("");
      setNewAccountIsAdmin(false);
      setNewAccountQuotaMB(0);
      setNewAccountAvatar("");
      setNewAccountProfile({ display_name: "", title: "", department: "", phone: "" });
      setNewAccountSendPolicy("anyone");
      setNewAccountReceivePolicy("anyone");
    } catch (err) {
      setFormError(err instanceof Error ? err.message : t("accounts.createFailed"));
    }
  };

  // handleAvatarFile reads a chosen image into a data URL for the create payload,
  // enforcing the same type/size limits the server applies.
  const handleAvatarFile = (file: File) => {
    if (!file.type.startsWith("image/")) {
      setFormError(t("accounts.photoMustBeImage"));
      return;
    }
    if (file.size > 1024 * 1024) {
      setFormError(t("accounts.photoTooLarge"));
      return;
    }
    const reader = new FileReader();
    reader.onload = () => setNewAccountAvatar(String(reader.result));
    reader.onerror = () => setFormError(t("accounts.photoReadFailed"));
    reader.readAsDataURL(file);
  };

  const handleDeleteAccount = async () => {
    if (!selectedAccount || isDeleting) return;

    // Guard against a second submission while the DELETE + refetch is still in
    // flight: the dialog stays open and the button enabled during the awaits,
    // so without this a repeat click would fire a duplicate DELETE request.
    setIsDeleting(true);
    try {
      await deleteAccount(selectedAccount.email);
      setIsDeleteDialogOpen(false);
      setSelectedAccount(null);
    } catch (err) {
      console.error("Failed to delete account:", err);
    } finally {
      setIsDeleting(false);
    }
  };

  const handleUpdateAccount = async () => {
    if (!selectedAccount) return;

    const adminChanged = selectedAccount.is_admin !== originalIsAdmin;
    if (adminChanged && !currentAdminPassword) {
      setFormError(t("accounts.enterAdminPasswordPrompt"));
      return;
    }

    try {
      const updates: Partial<Account> & {
        password?: string;
        current_admin_password?: string;
      } = {
        is_admin: selectedAccount.is_admin,
        is_active: selectedAccount.is_active,
        quota_limit: selectedAccount.quota_limit,
        quota_warn: selectedAccount.quota_warn,
        quota_prohibit_send: selectedAccount.quota_prohibit_send,
        display_name: selectedAccount.display_name ?? "",
        title: selectedAccount.title ?? "",
        department: selectedAccount.department ?? "",
        phone: selectedAccount.phone ?? "",
        send_policy: selectedAccount.send_policy ?? "anyone",
        receive_policy: selectedAccount.receive_policy ?? "anyone",
      };
      if (newAccountPassword) {
        updates.password = newAccountPassword;
        updates.must_change_password = requirePasswordChangeOnReset;
      }
      // The backend requires re-authentication with the acting admin's password
      // before it will grant or revoke admin privileges.
      if (adminChanged) {
        updates.current_admin_password = currentAdminPassword;
      }
      await updateAccount(selectedAccount.email, updates);
      setIsEditDialogOpen(false);
      setSelectedAccount(null);
      setNewAccountPassword("");
      setRequirePasswordChangeOnReset(true);
      setCurrentAdminPassword("");
    } catch (err) {
      setFormError(errorMessage(err, t("accounts.updateFailed")));
    }
  };

  const handleManageRoles = async (account: Account) => {
    setRolesAccount(account);
    setRolesLoading(true);
    setFormError("");
    try {
      await fetchAccountRoles(account.email);
    } finally {
      setRolesLoading(false);
    }
    setIsRolesDialogOpen(true);
  };

  const handleAssignRole = async (roleId: string) => {
    if (!rolesAccount) return;
    setAssigningRole(true);
    try {
      await assignRole(rolesAccount.email, roleId);
    } catch (err) {
      setFormError(err instanceof Error ? err.message : String(err));
    } finally {
      setAssigningRole(false);
    }
  };

  const handleRemoveRole = async (roleId: string) => {
    if (!rolesAccount) return;
    setAssigningRole(true);
    try {
      await removeRole(rolesAccount.email, roleId);
    } catch (err) {
      setFormError(err instanceof Error ? err.message : String(err));
    } finally {
      setAssigningRole(false);
    }
  };

  const formatBytes = (bytes: number) => {
    if (bytes === 0) return "0 B";
    const k = 1024;
    const sizes = ["B", "KB", "MB", "GB", "TB"];
    const i = Math.floor(Math.log(bytes) / Math.log(k));
    return parseFloat((bytes / Math.pow(k, i)).toFixed(2)) + " " + sizes[i];
  };

  return (
    <div className="space-y-6">
      {/* Header */}
      <div className="flex flex-col gap-4 sm:flex-row sm:items-center sm:justify-between">
        <div>
          <h1 className="text-3xl font-bold tracking-tight">{t("accounts.title")}</h1>
          <p className="text-muted-foreground mt-1">
            {t("accounts.subtitle")}
          </p>
        </div>
        <Dialog open={isAddDialogOpen} onOpenChange={setIsAddDialogOpen}>
          {/* @ts-expect-error asChild prop not typed in Base UI but works at runtime */}
          <DialogTrigger asChild>
            <Button>
              <Plus className="mr-2 h-4 w-4" />
              {t("accounts.add")}
            </Button>
          </DialogTrigger>
          <DialogContent className="sm:max-w-md">
            <DialogHeader>
              <DialogTitle>{t("accounts.createTitle")}</DialogTitle>
              <DialogDescription>
                {t("accounts.createDescription")}
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
                <Label htmlFor="email">{t("accounts.emailAddress")}</Label>
                <Input
                  id="email"
                  type="email"
                  placeholder="user@example.com"
                  value={newAccountEmail}
                  onChange={(e) => setNewAccountEmail(e.target.value)}
                />
              </div>
              <div className="space-y-2">
                <Label htmlFor="password">{t("accounts.password")}</Label>
                <Input
                  id="password"
                  type="password"
                  placeholder="••••••••"
                  value={newAccountPassword}
                  onChange={(e) => setNewAccountPassword(e.target.value)}
                />
              </div>
              <div className="space-y-2">
                <Label htmlFor="quota">{t("accounts.quotaMB")}</Label>
                <Input
                  id="quota"
                  type="number"
                  min={0}
                  placeholder={t("accounts.quotaPlaceholder")}
                  value={newAccountQuotaMB}
                  onChange={(e) => setNewAccountQuotaMB(Math.max(0, Number(e.target.value) || 0))}
                />
                <p className="text-sm text-muted-foreground">{t("accounts.quotaUnlimitedHint")}</p>
              </div>
              <div className="space-y-2">
                <Label htmlFor="avatar">{t("accounts.profilePhoto")}</Label>
                <div className="flex items-center gap-3">
                  {newAccountAvatar ? (
                    <img
                      src={newAccountAvatar}
                      alt={t("accounts.preview")}
                      className="h-12 w-12 rounded-full object-cover ring-2 ring-border"
                    />
                  ) : (
                    <div className="flex h-12 w-12 items-center justify-center rounded-full bg-muted">
                      <Users className="h-5 w-5 text-muted-foreground" />
                    </div>
                  )}
                  <Input
                    id="avatar"
                    type="file"
                    accept="image/png,image/jpeg,image/gif,image/webp"
                    className="flex-1"
                    onChange={(e) => {
                      const file = e.target.files?.[0];
                      if (file) handleAvatarFile(file);
                    }}
                  />
                  {newAccountAvatar && (
                    <Button variant="ghost" size="icon" onClick={() => setNewAccountAvatar("")}>
                      <Trash2 className="h-4 w-4" />
                    </Button>
                  )}
                </div>
                <p className="text-sm text-muted-foreground">{t("accounts.photoHint")}</p>
              </div>
              <div className="grid grid-cols-2 gap-3">
                <div className="space-y-2">
                  <Label htmlFor="display-name">{t("accounts.displayName")}</Label>
                  <Input
                    id="display-name"
                    placeholder={t("accounts.displayNamePlaceholder")}
                    value={newAccountProfile.display_name}
                    onChange={(e) => setNewAccountProfile({ ...newAccountProfile, display_name: e.target.value })}
                  />
                </div>
                <div className="space-y-2">
                  <Label htmlFor="title">{t("accounts.jobTitle")}</Label>
                  <Input
                    id="title"
                    placeholder={t("accounts.titlePlaceholder")}
                    value={newAccountProfile.title}
                    onChange={(e) => setNewAccountProfile({ ...newAccountProfile, title: e.target.value })}
                  />
                </div>
                <div className="space-y-2">
                  <Label htmlFor="department">{t("accounts.department")}</Label>
                  <Input
                    id="department"
                    placeholder={t("accounts.departmentPlaceholder")}
                    value={newAccountProfile.department}
                    onChange={(e) => setNewAccountProfile({ ...newAccountProfile, department: e.target.value })}
                  />
                </div>
                <div className="space-y-2">
                  <Label htmlFor="phone">{t("accounts.phone")}</Label>
                  <Input
                    id="phone"
                    placeholder={t("accounts.phonePlaceholder")}
                    value={newAccountProfile.phone}
                    onChange={(e) => setNewAccountProfile({ ...newAccountProfile, phone: e.target.value })}
                  />
                </div>
              </div>
              <MailScopeFields
                t={t}
                idPrefix="create"
                sendPolicy={newAccountSendPolicy}
                receivePolicy={newAccountReceivePolicy}
                onSendChange={setNewAccountSendPolicy}
                onReceiveChange={setNewAccountReceivePolicy}
              />
              <div className="flex items-center justify-between pt-2">
                <Label htmlFor="is-admin">{t("accounts.adminAccount")}</Label>
                <Switch
                  id="is-admin"
                  checked={newAccountIsAdmin}
                  onCheckedChange={setNewAccountIsAdmin}
                />
              </div>
            </div>
            <DialogFooter>
              <Button variant="outline" onClick={() => setIsAddDialogOpen(false)}>
                {t("common.cancel")}
              </Button>
              <Button onClick={handleCreateAccount}>{t("accounts.createAccount")}</Button>
            </DialogFooter>
          </DialogContent>
        </Dialog>
      </div>

      {/* Search and Filter */}
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
        <Button
          variant="outline"
          size="icon"
          onClick={() => fetchAccounts()}
          disabled={loading}
        >
          <RefreshCw className={cn("h-4 w-4", loading && "animate-spin")} />
        </Button>
      </div>

      {/* Accounts List */}
      {loading ? (
        <div className="grid gap-4 md:grid-cols-2 lg:grid-cols-3">
          {[1, 2, 3].map((i) => (
            <Card key={i}>
              <CardContent className="p-6">
                <Skeleton className="h-6 w-3/4 mb-4" />
                <Skeleton className="h-4 w-1/2" />
              </CardContent>
            </Card>
          ))}
        </div>
      ) : filteredAccounts?.length === 0 ? (
        <Card className="text-center py-12">
          <CardContent>
            <Users className="h-12 w-12 mx-auto text-muted-foreground mb-4" />
            <h3 className="text-lg font-medium">{t("accounts.noAccounts")}</h3>
            <p className="text-muted-foreground mt-1">
              {searchQuery
                ? t("accounts.noMatchSearch")
                : t("accounts.getStarted")}
            </p>
            {!searchQuery && (
              <Button className="mt-4" onClick={() => setIsAddDialogOpen(true)}>
                <Plus className="mr-2 h-4 w-4" />
                {t("accounts.add")}
              </Button>
            )}
          </CardContent>
        </Card>
      ) : (
        <div className="grid gap-4 md:grid-cols-2 lg:grid-cols-3">
          {filteredAccounts?.map((account: Account) => (
            <AccountCard
              key={account.email}
              account={account}
              onEdit={() => {
                setSelectedAccount(account);
                setOriginalIsAdmin(account.is_admin);
                setCurrentAdminPassword("");
                setNewAccountPassword("");
                setRequirePasswordChangeOnReset(true);
                setFormError("");
                setIsEditDialogOpen(true);
              }}
              onDelete={() => {
                setSelectedAccount(account);
                setIsDeleteDialogOpen(true);
              }}
              onManageDevices={() => {
                setDevicesAccount(account);
                setIsDevicesDialogOpen(true);
              }}
              onManageRoles={() => handleManageRoles(account)}
              formatBytes={formatBytes}
            />
          ))}
        </div>
      )}

      {/* Edit Account Dialog */}
      <Dialog open={isEditDialogOpen} onOpenChange={setIsEditDialogOpen}>
        <DialogContent className="sm:max-w-md">
          <DialogHeader>
            <DialogTitle>{t("accounts.edit")}</DialogTitle>
            <DialogDescription>
              {t("accounts.editDescription", { email: selectedAccount?.email ?? "" })}
            </DialogDescription>
          </DialogHeader>
          {formError && (
            <Alert variant="destructive">
              <AlertCircle className="h-4 w-4" />
              <AlertDescription>{formError}</AlertDescription>
            </Alert>
          )}
          {selectedAccount && (
            <div className="space-y-4 py-4">
              <div className="flex items-center justify-between">
                <Label htmlFor="edit-is-admin">{t("accounts.adminAccount")}</Label>
                <Switch
                  id="edit-is-admin"
                  checked={selectedAccount.is_admin}
                  onCheckedChange={(checked) =>
                    setSelectedAccount({ ...selectedAccount, is_admin: checked })
                  }
                />
              </div>
              {selectedAccount.is_admin !== originalIsAdmin && (
                <div className="space-y-2">
                  <Label htmlFor="current-admin-password">{t("accounts.yourAdminPassword")}</Label>
                  <Input
                    id="current-admin-password"
                    type="password"
                    placeholder={t("accounts.confirmAdminPlaceholder")}
                    value={currentAdminPassword}
                    onChange={(e) => setCurrentAdminPassword(e.target.value)}
                  />
                  <p className="text-sm text-muted-foreground">
                    {t("accounts.adminPasswordHint")}
                  </p>
                </div>
              )}
              <div className="flex items-center justify-between">
                <Label htmlFor="edit-is-active">{t("common.active")}</Label>
                <Switch
                  id="edit-is-active"
                  checked={selectedAccount.is_active}
                  onCheckedChange={(checked) =>
                    setSelectedAccount({ ...selectedAccount, is_active: checked })
                  }
                />
              </div>
              <div className="space-y-2">
                <Label htmlFor="edit-quota">{t("accounts.quotaMB")}</Label>
                <Input
                  id="edit-quota"
                  type="number"
                  min={0}
                  placeholder={t("accounts.quotaPlaceholder")}
                  value={Math.round(selectedAccount.quota_limit / (1024 * 1024))}
                  onChange={(e) =>
                    setSelectedAccount({
                      ...selectedAccount,
                      quota_limit: Math.max(0, Number(e.target.value) || 0) * 1024 * 1024,
                    })
                  }
                />
                <p className="text-sm text-muted-foreground">{t("accounts.quotaUnlimitedHint")}</p>
              </div>
              <div className="grid grid-cols-2 gap-3">
                <div className="space-y-2">
                  <Label htmlFor="edit-quota-warn">{t("accounts.quotaWarnMB")}</Label>
                  <Input
                    id="edit-quota-warn"
                    type="number"
                    min={0}
                    value={Math.round(selectedAccount.quota_warn / (1024 * 1024))}
                    onChange={(e) =>
                      setSelectedAccount({
                        ...selectedAccount,
                        quota_warn: Math.max(0, Number(e.target.value) || 0) * 1024 * 1024,
                      })
                    }
                  />
                  <p className="text-sm text-muted-foreground">{t("accounts.quotaDisabledHint")}</p>
                </div>
                <div className="space-y-2">
                  <Label htmlFor="edit-quota-prohibit-send">{t("accounts.quotaProhibitSendMB")}</Label>
                  <Input
                    id="edit-quota-prohibit-send"
                    type="number"
                    min={0}
                    value={Math.round(selectedAccount.quota_prohibit_send / (1024 * 1024))}
                    onChange={(e) =>
                      setSelectedAccount({
                        ...selectedAccount,
                        quota_prohibit_send: Math.max(0, Number(e.target.value) || 0) * 1024 * 1024,
                      })
                    }
                  />
                  <p className="text-sm text-muted-foreground">{t("accounts.quotaDisabledHint")}</p>
                </div>
              </div>
              <div className="grid grid-cols-2 gap-3">
                <div className="space-y-2">
                  <Label htmlFor="edit-display-name">{t("accounts.displayName")}</Label>
                  <Input
                    id="edit-display-name"
                    value={selectedAccount.display_name ?? ""}
                    onChange={(e) => setSelectedAccount({ ...selectedAccount, display_name: e.target.value })}
                  />
                </div>
                <div className="space-y-2">
                  <Label htmlFor="edit-title">{t("accounts.jobTitle")}</Label>
                  <Input
                    id="edit-title"
                    value={selectedAccount.title ?? ""}
                    onChange={(e) => setSelectedAccount({ ...selectedAccount, title: e.target.value })}
                  />
                </div>
                <div className="space-y-2">
                  <Label htmlFor="edit-department">{t("accounts.department")}</Label>
                  <Input
                    id="edit-department"
                    value={selectedAccount.department ?? ""}
                    onChange={(e) => setSelectedAccount({ ...selectedAccount, department: e.target.value })}
                  />
                </div>
                <div className="space-y-2">
                  <Label htmlFor="edit-phone">{t("accounts.phone")}</Label>
                  <Input
                    id="edit-phone"
                    value={selectedAccount.phone ?? ""}
                    onChange={(e) => setSelectedAccount({ ...selectedAccount, phone: e.target.value })}
                  />
                </div>
              </div>
              <MailScopeFields
                t={t}
                idPrefix="edit"
                sendPolicy={selectedAccount.send_policy ?? "anyone"}
                receivePolicy={selectedAccount.receive_policy ?? "anyone"}
                onSendChange={(v) => setSelectedAccount({ ...selectedAccount, send_policy: v })}
                onReceiveChange={(v) => setSelectedAccount({ ...selectedAccount, receive_policy: v })}
              />
              <div className="space-y-2 pt-4 border-t">
                <Label htmlFor="new-password">{t("accounts.newPassword")}</Label>
                <Input
                  id="new-password"
                  type="password"
                  placeholder={t("accounts.newPasswordPlaceholder")}
                  value={newAccountPassword}
                  onChange={(e) => setNewAccountPassword(e.target.value)}
                />
              </div>
              {/* Only relevant when actually resetting the password; rendering it
                  unconditionally showed a checked-but-disabled switch that
                  implied it would apply even with no new password set. */}
              {newAccountPassword && (
                <div className="flex items-center justify-between">
                  <div className="space-y-0.5">
                    <Label htmlFor="require-password-change">{t("accounts.requirePasswordChange")}</Label>
                    <p className="text-sm text-muted-foreground">
                      {t("accounts.requirePasswordChangeHint")}
                    </p>
                  </div>
                  <Switch
                    id="require-password-change"
                    checked={requirePasswordChangeOnReset}
                    onCheckedChange={setRequirePasswordChangeOnReset}
                  />
                </div>
              )}
            </div>
          )}
          <DialogFooter>
            <Button
              variant="outline"
              onClick={() => {
                setIsEditDialogOpen(false);
                setNewAccountPassword("");
                setRequirePasswordChangeOnReset(true);
                setCurrentAdminPassword("");
                setFormError("");
              }}
            >
              {t("common.cancel")}
            </Button>
            <Button onClick={handleUpdateAccount}>{t("common.saveChanges")}</Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* Delete Confirmation Dialog */}
      <Dialog open={isDeleteDialogOpen} onOpenChange={setIsDeleteDialogOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t("accounts.delete")}</DialogTitle>
            <DialogDescription>
              {t("accounts.deleteConfirm", { email: selectedAccount?.email ?? "" })}
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="outline" onClick={() => setIsDeleteDialogOpen(false)}>
              {t("common.cancel")}
            </Button>
            <Button variant="destructive" onClick={handleDeleteAccount} disabled={isDeleting}>
              <Trash2 className="mr-2 h-4 w-4" />
              {t("common.delete")}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* EAS device partnerships. Reached from the per-card dropdown so a
          per-account destructive operation (remote wipe / remove) does not
          have to share state with the bulk account list or the edit dialog. */}
      <EASDevicesDialog
        email={devicesAccount?.email ?? null}
        open={isDevicesDialogOpen}
        onOpenChange={(open) => {
          setIsDevicesDialogOpen(open);
          if (!open) setDevicesAccount(null);
        }}
      />

      {/* Role assignment dialog */}
      <Dialog open={isRolesDialogOpen} onOpenChange={(open) => {
        setIsRolesDialogOpen(open);
        if (!open) setRolesAccount(null);
      }}>
        <DialogContent className="sm:max-w-md">
          <DialogHeader>
            <DialogTitle>{t("roles.assignRole")}</DialogTitle>
            <DialogDescription>
              {t("roles.assignRoleDescription", { email: rolesAccount?.email ?? "" })}
            </DialogDescription>
          </DialogHeader>
          {formError && (
            <Alert variant="destructive">
              <AlertCircle className="h-4 w-4" />
              <AlertDescription>{formError}</AlertDescription>
            </Alert>
          )}
          <div className="space-y-4 py-4">
            {rolesLoading ? (
              <Skeleton className="h-20 w-full" />
            ) : (
              <>
                {/* Current roles */}
                <div className="space-y-2">
                  <Label>{t("roles.accountRoles")}</Label>
                  {accountRoles && accountRoles.length > 0 ? (
                    <div className="space-y-2">
                      {accountRoles.map((role: Role) => (
                        <div key={role.id} className="flex items-center justify-between p-2 rounded-lg border">
                          <div className="flex items-center gap-2">
                            <Shield className="h-4 w-4 text-muted-foreground" />
                            <span className="text-sm font-medium">{role.name}</span>
                          </div>
                          <Button
                            variant="ghost"
                            size="icon"
                            onClick={() => handleRemoveRole(role.id)}
                            disabled={assigningRole}
                          >
                            <X className="h-4 w-4 text-red-500" />
                          </Button>
                        </div>
                      ))}
                    </div>
                  ) : (
                    <p className="text-sm text-muted-foreground">{t("roles.noAccountRoles")}</p>
                  )}
                </div>
                {/* Available roles to assign */}
                <div className="space-y-2">
                  <Label>{t("roles.selectRole")}</Label>
                  <div className="space-y-2">
                    {allRoles?.filter((r: Role) => !accountRoles?.some((ar: Role) => ar.id === r.id)).map((role: Role) => (
                      <Button
                        key={role.id}
                        variant="outline"
                        className="w-full justify-start"
                        onClick={() => handleAssignRole(role.id)}
                        disabled={assigningRole}
                      >
                        <Shield className="mr-2 h-4 w-4" />
                        {role.name}
                      </Button>
                    ))}
                  </div>
                </div>
              </>
            )}
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={() => setIsRolesDialogOpen(false)}>
              {t("common.cancel")}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
}

interface AccountCardProps {
  account: Account;
  onEdit: () => void;
  onDelete: () => void;
  onManageDevices: () => void;
  onManageRoles: () => void;
  formatBytes: (bytes: number) => string;
}

function AccountCard({ account, onEdit, onDelete, onManageDevices, onManageRoles, formatBytes }: AccountCardProps) {
  const { t } = useI18n();
  // A quota_limit of 0 means unlimited storage; a percentage and progress bar
  // are meaningless in that case, so show the usage without them.
  const isUnlimited = account.quota_limit <= 0;
  const quotaPercent = isUnlimited
    ? 0
    : Math.round((account.quota_used / account.quota_limit) * 100);

  return (
    <Card className="group">
      <CardHeader className="pb-3">
        <div className="flex items-start justify-between">
          <div className="flex items-center gap-3">
            <div className={cn(
              "p-2 rounded-lg",
              account.is_admin
                ? "bg-gradient-to-br from-violet-500 to-violet-600"
                : "bg-gradient-to-br from-blue-500 to-blue-600"
            )}>
              {account.is_admin ? (
                <Shield className="h-5 w-5 text-white" />
              ) : (
                <Mail className="h-5 w-5 text-white" />
              )}
            </div>
            <div className="min-w-0">
              <CardTitle className="text-base truncate">{account.email}</CardTitle>
              <CardDescription className="flex items-center gap-1">
                {account.is_admin && (
                  <Badge variant="secondary" className="text-xs">{t("accounts.admin")}</Badge>
                )}
                {!account.is_active && (
                  <Badge variant="destructive" className="text-xs">{t("common.inactive")}</Badge>
                )}
              </CardDescription>
            </div>
          </div>
          <DropdownMenu>
            {/* @ts-expect-error asChild prop not typed in Base UI but works at runtime */}
            <DropdownMenuTrigger asChild>
              <Button variant="ghost" size="icon" className="h-8 w-8">
                <MoreHorizontal className="h-4 w-4" />
              </Button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end">
              <DropdownMenuItem onClick={onEdit}>
                <Edit className="mr-2 h-4 w-4" />
                {t("common.edit")}
              </DropdownMenuItem>
              <DropdownMenuItem
                onClick={onManageDevices}
                data-testid="account-manage-devices"
              >
                <Smartphone className="mr-2 h-4 w-4" />
                {t("accounts.devices.manage")}
              </DropdownMenuItem>
              <DropdownMenuItem onClick={onManageRoles}>
                <UserCog className="mr-2 h-4 w-4" />
                {t("roles.assignRole")}
              </DropdownMenuItem>
              {account.totp_enabled && (
                <DropdownMenuItem disabled>
                  <Key className="mr-2 h-4 w-4" />
                  {t("accounts.twoFactorEnabled")}
                </DropdownMenuItem>
              )}
              <DropdownMenuSeparator />
              <DropdownMenuItem onClick={onDelete} className="text-red-600">
                <Trash2 className="mr-2 h-4 w-4" />
                {t("common.delete")}
              </DropdownMenuItem>
            </DropdownMenuContent>
          </DropdownMenu>
        </div>
      </CardHeader>
      <CardContent>
        <div className="space-y-4">
          {/* Quota Usage */}
          <div className="space-y-2">
            <div className="flex items-center justify-between text-sm">
              <span className="text-muted-foreground flex items-center gap-1">
                <HardDrive className="h-4 w-4" />
                {t("accounts.storage")}
              </span>
              <span className="font-medium">
                {formatBytes(account.quota_used)} / {isUnlimited ? t("accounts.unlimited") : formatBytes(account.quota_limit)}
              </span>
            </div>
            {!isUnlimited && (
              <Progress
                value={quotaPercent}
                className="h-2"
              />
            )}
            <p className="text-xs text-muted-foreground text-right">
              {isUnlimited ? t("accounts.noQuotaLimit") : t("accounts.percentUsed", { percent: String(quotaPercent) })}
            </p>
          </div>

          {/* Last Login */}
          <div className="text-xs text-muted-foreground pt-2 border-t">
            {t("accounts.lastLogin")}:{" "}
            {account.last_login && new Date(account.last_login).getFullYear() > 1
              ? new Date(account.last_login).toLocaleString()
              : t("common.never")}
          </div>
        </div>
      </CardContent>
    </Card>
  );
}
