import { useState, useEffect } from "react";
import {
  UsersRound,
  Plus,
  MoreHorizontal,
  Edit,
  Trash2,
  Shield,
  Mail,
  CheckCircle,
  AlertCircle,
} from "lucide-react";
import { Button } from "@/components/ui/button";
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
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Skeleton } from "@/components/ui/skeleton";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { useAccounts, useDelegations } from "@/hooks/useApi";
import { useI18n } from "@/hooks/useI18n";

export function Delegation() {
  const { t } = useI18n();
  const { accounts, fetchAccounts } = useAccounts();
  const { delegations, loading, fetchDelegations, createDelegation, deleteDelegation } =
    useDelegations();
  const [activeTab, setActiveTab] = useState("shared-mailboxes");
  const [formError, setFormError] = useState<string | null>(null);
  const [isAddDialogOpen, setIsAddDialogOpen] = useState(false);
  const [selectedOwner, setSelectedOwner] = useState("");
  const [selectedGrantee, setSelectedGrantee] = useState("");
  const [grantReadAccess, setGrantReadAccess] = useState(true);
  const [grantWriteAccess, setGrantWriteAccess] = useState(false);
  const [grantSendAs, setGrantSendAs] = useState(false);
  const [grantSendOnBehalf, setGrantSendOnBehalf] = useState(false);

  useEffect(() => {
    fetchAccounts();
    fetchDelegations().catch(() => {
      /* error surfaced via hook state */
    });
  }, [fetchAccounts, fetchDelegations]);

  const handleCreateDelegation = async () => {
    if (!selectedOwner || !selectedGrantee) {
      return;
    }

    // Build rights from checkboxes
    const rights: string[] = [];
    if (grantReadAccess) rights.push("read");
    if (grantWriteAccess) rights.push("write");

    try {
      await createDelegation({
        owner: selectedOwner,
        grantee: selectedGrantee,
        rights,
        canSendAs: grantSendAs,
        canSendOnBehalf: grantSendOnBehalf,
      });
      setFormError(null);
      setIsAddDialogOpen(false);
      setSelectedOwner("");
      setSelectedGrantee("");
      setGrantReadAccess(true);
      setGrantWriteAccess(false);
      setGrantSendAs(false);
      setGrantSendOnBehalf(false);
    } catch (err) {
      setFormError((err as { message?: string }).message || t("delegation.createFailed"));
    }
  };

  const handleDeleteDelegation = async (id: string) => {
    try {
      await deleteDelegation(id);
      setFormError(null);
    } catch (err) {
      setFormError((err as { message?: string }).message || t("delegation.removeFailed"));
    }
  };

  const filteredDelegations = delegations ?? [];

  return (
    <div className="space-y-6">
      {/* Header */}
      <div className="flex flex-col gap-4 sm:flex-row sm:items-center sm:justify-between">
        <div>
          <h1 className="text-3xl font-bold tracking-tight">{t("delegation.title")}</h1>
          <p className="text-muted-foreground mt-1">
            {t("delegation.description")}
          </p>
        </div>
        <Dialog open={isAddDialogOpen} onOpenChange={setIsAddDialogOpen}>
          {/* @ts-expect-error asChild prop not typed in Base UI but works at runtime */}
          <DialogTrigger asChild>
            <Button>
              <Plus className="mr-2 h-4 w-4" />
              {t("delegation.addDelegation")}
            </Button>
          </DialogTrigger>
          <DialogContent className="sm:max-w-md">
            <DialogHeader>
              <DialogTitle>{t("delegation.addDelegateAccess")}</DialogTitle>
              <DialogDescription>
                {t("delegation.addDelegateDescription")}
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
                <Label htmlFor="owner">{t("delegation.mailboxOwner")}</Label>
                <select
                  id="owner"
                  className="w-full p-2 border rounded-md bg-background"
                  value={selectedOwner}
                  onChange={(e) => setSelectedOwner(e.target.value)}
                >
                  <option value="">{t("delegation.selectOwner")}</option>
                  {accounts?.map((acc) => (
                    <option key={acc.email} value={acc.email}>
                      {acc.email}
                    </option>
                  ))}
                </select>
              </div>
              <div className="space-y-2">
                <Label htmlFor="grantee">{t("delegation.delegateGrantee")}</Label>
                <select
                  id="grantee"
                  className="w-full p-2 border rounded-md bg-background"
                  value={selectedGrantee}
                  onChange={(e) => setSelectedGrantee(e.target.value)}
                >
                  <option value="">{t("delegation.selectDelegate")}</option>
                  {accounts?.map((acc) => (
                    <option key={acc.email} value={acc.email}>
                      {acc.email}
                    </option>
                  ))}
                </select>
              </div>
              <div className="space-y-3 pt-2 border-t">
                <Label>{t("delegation.accessRights")}</Label>
                <div className="flex items-center justify-between">
                  <div className="space-y-0.5">
                    <Label className="text-sm">{t("delegation.readAccess")}</Label>
                    <p className="text-xs text-muted-foreground">{t("delegation.readAccessDescription")}</p>
                  </div>
                  <Switch checked={grantReadAccess} onCheckedChange={setGrantReadAccess} />
                </div>
                <div className="flex items-center justify-between">
                  <div className="space-y-0.5">
                    <Label className="text-sm">{t("delegation.writeAccess")}</Label>
                    <p className="text-xs text-muted-foreground">{t("delegation.writeAccessDescription")}</p>
                  </div>
                  <Switch checked={grantWriteAccess} onCheckedChange={setGrantWriteAccess} />
                </div>
                <div className="flex items-center justify-between">
                  <div className="space-y-0.5">
                    <Label className="text-sm">{t("delegation.sendAs")}</Label>
                    <p className="text-xs text-muted-foreground">{t("delegation.sendAsDescription")}</p>
                  </div>
                  <Switch checked={grantSendAs} onCheckedChange={setGrantSendAs} />
                </div>
                <div className="flex items-center justify-between">
                  <div className="space-y-0.5">
                    <Label className="text-sm">{t("delegation.sendOnBehalf")}</Label>
                    <p className="text-xs text-muted-foreground">{t("delegation.sendOnBehalfDescription")}</p>
                  </div>
                  <Switch checked={grantSendOnBehalf} onCheckedChange={setGrantSendOnBehalf} />
                </div>
              </div>
            </div>
            <DialogFooter>
              <Button variant="outline" onClick={() => setIsAddDialogOpen(false)}>
                {t("common.cancel")}
              </Button>
              <Button onClick={handleCreateDelegation} disabled={!selectedOwner || !selectedGrantee}>
                {t("delegation.grantAccess")}
              </Button>
            </DialogFooter>
          </DialogContent>
        </Dialog>
      </div>

      {formError && !isAddDialogOpen && (
        <Alert variant="destructive">
          <AlertCircle className="h-4 w-4" />
          <AlertDescription>{formError}</AlertDescription>
        </Alert>
      )}

      {/* Tabs */}
      <Tabs value={activeTab} onValueChange={setActiveTab}>
        <TabsList>
          <TabsTrigger value="shared-mailboxes">
            <UsersRound className="h-4 w-4 mr-2" />
            {t("delegation.sharedMailboxes")}
          </TabsTrigger>
          <TabsTrigger value="delegates">
            <Shield className="h-4 w-4 mr-2" />
            {t("delegation.delegates")}
          </TabsTrigger>
          <TabsTrigger value="send-as">
            <Mail className="h-4 w-4 mr-2" />
            {t("delegation.sendAs")}
          </TabsTrigger>
        </TabsList>

        <TabsContent value="shared-mailboxes" className="space-y-4">
          <Card>
            <CardHeader>
              <CardTitle>{t("delegation.sharedMailboxAccess")}</CardTitle>
              <CardDescription>
                {t("delegation.sharedMailboxAccessDescription")}
              </CardDescription>
            </CardHeader>
            <CardContent>
              {loading ? (
                <div className="space-y-4">
                  <Skeleton className="h-16 w-full" />
                  <Skeleton className="h-16 w-full" />
                </div>
              ) : filteredDelegations.length === 0 ? (
                <div className="text-center py-8">
                  <UsersRound className="h-12 w-12 mx-auto text-muted-foreground mb-4" />
                  <h3 className="text-lg font-medium">{t("delegation.noSharedMailboxes")}</h3>
                  <p className="text-muted-foreground mt-1">
                    {t("delegation.noSharedMailboxesDescription")}
                  </p>
                </div>
              ) : (
                <div className="space-y-2">
                  {filteredDelegations.map((entry) => (
                    <div
                      key={entry.id}
                      className="flex items-center justify-between p-4 rounded-lg border hover:bg-muted/50 transition-colors"
                    >
                      <div className="flex items-center gap-3">
                        <div className="p-2 rounded-lg bg-gradient-to-br from-violet-500 to-violet-600">
                          <UsersRound className="h-4 w-4 text-white" />
                        </div>
                        <div>
                          <div className="font-medium">{entry.mailbox}</div>
                          <div className="text-sm text-muted-foreground">
                            {t("delegation.sharedWith", { grantee: entry.grantee })}
                          </div>
                        </div>
                      </div>
                      <div className="flex items-center gap-2">
                        <Badge variant="secondary">{entry.rights}</Badge>
                        {entry.canSendOnBehalf && (
                          <Badge variant="outline" className="text-xs">{t("delegation.sendOnBehalf")}</Badge>
                        )}
                        {entry.canSendAs && (
                          <Badge variant="outline" className="text-xs">{t("delegation.sendAs")}</Badge>
                        )}
                        <DropdownMenu>
                          {/* @ts-expect-error asChild prop not typed in Base UI but works at runtime */}
                          <DropdownMenuTrigger asChild>
                            <Button variant="ghost" size="icon" className="h-8 w-8">
                              <MoreHorizontal className="h-4 w-4" />
                            </Button>
                          </DropdownMenuTrigger>
                          <DropdownMenuContent align="end">
                            <DropdownMenuItem>
                              <Edit className="mr-2 h-4 w-4" />
                              {t("delegation.editRights")}
                            </DropdownMenuItem>
                            <DropdownMenuSeparator />
                            <DropdownMenuItem
                              className="text-red-600"
                              onClick={() => handleDeleteDelegation(entry.id)}
                            >
                              <Trash2 className="mr-2 h-4 w-4" />
                              {t("delegation.removeAccess")}
                            </DropdownMenuItem>
                          </DropdownMenuContent>
                        </DropdownMenu>
                      </div>
                    </div>
                  ))}
                </div>
              )}
            </CardContent>
          </Card>
        </TabsContent>

        <TabsContent value="delegates" className="space-y-4">
          <Card>
            <CardHeader>
              <CardTitle>{t("delegation.delegateRelationships")}</CardTitle>
              <CardDescription>
                {t("delegation.delegateRelationshipsDescription")}
              </CardDescription>
            </CardHeader>
            <CardContent>
              <Alert className="bg-blue-500/10 border-blue-500/20 mb-4">
                <AlertCircle className="h-4 w-4" />
                <AlertDescription>
                  {t("delegation.delegatesInfo")}
                </AlertDescription>
              </Alert>
              {filteredDelegations.filter((d) => d.rights.includes("write")).length === 0 ? (
                <div className="text-center py-8">
                  <Shield className="h-12 w-12 mx-auto text-muted-foreground mb-4" />
                  <h3 className="text-lg font-medium">{t("delegation.noDelegates")}</h3>
                  <p className="text-muted-foreground mt-1">
                    {t("delegation.noDelegatesDescription")}
                  </p>
                </div>
              ) : (
                <div className="space-y-2">
                  {filteredDelegations
                    .filter((d) => d.rights.includes("write"))
                    .map((entry) => (
                      <div
                        key={entry.id}
                        className="flex items-center justify-between p-4 rounded-lg border hover:bg-muted/50 transition-colors"
                      >
                        <div className="flex items-center gap-3">
                          <div className="p-2 rounded-lg bg-gradient-to-br from-blue-500 to-blue-600">
                            <Shield className="h-4 w-4 text-white" />
                          </div>
                          <div>
                            <div className="font-medium">{entry.grantee}</div>
                            <div className="text-sm text-muted-foreground">
                              {t("delegation.actsFor", { owner: entry.owner })}
                            </div>
                          </div>
                        </div>
                        <div className="flex items-center gap-2">
                          <Button variant="ghost" size="sm" onClick={() => handleDeleteDelegation(entry.id)}>
                            <Trash2 className="h-4 w-4 text-red-500" />
                          </Button>
                        </div>
                      </div>
                    ))}
                </div>
              )}
            </CardContent>
          </Card>
        </TabsContent>

        <TabsContent value="send-as" className="space-y-4">
          <Card>
            <CardHeader>
              <CardTitle>{t("delegation.sendAsPermissions")}</CardTitle>
              <CardDescription>
                {t("delegation.sendAsPermissionsDescription")}
              </CardDescription>
            </CardHeader>
            <CardContent>
              {filteredDelegations.filter((d) => d.canSendAs).length === 0 ? (
                <div className="text-center py-8">
                  <Mail className="h-12 w-12 mx-auto text-muted-foreground mb-4" />
                  <h3 className="text-lg font-medium">{t("delegation.noSendAs")}</h3>
                  <p className="text-muted-foreground mt-1">
                    {t("delegation.noSendAsDescription")}
                  </p>
                </div>
              ) : (
                <div className="space-y-2">
                  {filteredDelegations
                    .filter((d) => d.canSendAs)
                    .map((entry) => (
                      <div
                        key={entry.id}
                        className="flex items-center justify-between p-4 rounded-lg border hover:bg-muted/50 transition-colors"
                      >
                        <div className="flex items-center gap-3">
                          <div className="p-2 rounded-lg bg-gradient-to-br from-emerald-500 to-emerald-600">
                            <Mail className="h-4 w-4 text-white" />
                          </div>
                          <div>
                            <div className="font-medium">{entry.grantee}</div>
                            <div className="text-sm text-muted-foreground">
                              {t("delegation.canSendAs", { owner: entry.owner })}
                            </div>
                          </div>
                        </div>
                        <div className="flex items-center gap-2">
                          <CheckCircle className="h-4 w-4 text-emerald-500" />
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
