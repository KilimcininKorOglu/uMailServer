import { useState, useEffect } from "react";
import {
  Globe,
  Plus,
  Search,
  MoreHorizontal,
  Trash2,
  Copy,
  Check,
  AlertCircle,
  RefreshCw,
  Shield,
  Mail,
  Pencil,
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
import { toast } from "sonner";
import { useDomains } from "@/hooks/useApi";
import { useI18n } from "@/hooks/useI18n";
import { cn } from "@/lib/utils";
import type { Domain } from "@/types";

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

export function Domains() {
  const {
    domains,
    loading,
    error: _error,
    fetchDomains,
    createDomain,
    updateDomain,
    deleteDomain,
  } = useDomains();

  const { t } = useI18n();

  const [searchQuery, setSearchQuery] = useState("");
  const [isAddDialogOpen, setIsAddDialogOpen] = useState(false);
  const [isDeleteDialogOpen, setIsDeleteDialogOpen] = useState(false);
  const [isEditDialogOpen, setIsEditDialogOpen] = useState(false);
  const [editMaxAccounts, setEditMaxAccounts] = useState(0);
  const [editCompanyName, setEditCompanyName] = useState("");
  const [editFromInternal, setEditFromInternal] = useState("");
  const [editFromExternal, setEditFromExternal] = useState("");
  const [selectedDomain, setSelectedDomain] = useState<Domain | null>(null);
  const [newDomainName, setNewDomainName] = useState("");
  const [newDomainMaxAccounts, setNewDomainMaxAccounts] = useState(100);
  const [formError, setFormError] = useState("");
  const [copiedDNS, setCopiedDNS] = useState(false);
  const [isDeleting, setIsDeleting] = useState(false);

  useEffect(() => {
    fetchDomains();
  }, [fetchDomains]);

  const filteredDomains = domains?.filter((d: Domain) =>
    d.name.toLowerCase().includes(searchQuery.toLowerCase())
  );

  const handleCreateDomain = async () => {
    setFormError("");
    if (!newDomainName) {
      setFormError(t("domains.nameRequired"));
      return;
    }

    try {
      await createDomain(newDomainName, newDomainMaxAccounts);
      setIsAddDialogOpen(false);
      setNewDomainName("");
      setNewDomainMaxAccounts(100);
    } catch (err) {
      setFormError(errorMessage(err, t("domains.createFailed")));
    }
  };

  const handleDeleteDomain = async () => {
    if (!selectedDomain || isDeleting) return;

    // Guard against a second submission while the DELETE + refetch is still in
    // flight: the dialog stays open and the button enabled during the awaits,
    // so without this a repeat click would fire a duplicate DELETE request.
    setIsDeleting(true);
    try {
      await deleteDomain(selectedDomain.name);
      setIsDeleteDialogOpen(false);
      setSelectedDomain(null);
    } catch (err) {
      toast.error(errorMessage(err, t("domains.deleteFailed")));
    } finally {
      setIsDeleting(false);
    }
  };

  const handleToggleDomain = async (domain: Domain) => {
    try {
      // The update endpoint reads max_accounts and is_active together from the
      // body (non-pointer fields), so omitting max_accounts would reset it to 0.
      // Send the current value alongside the toggled status to preserve it.
      await updateDomain(domain.name, {
        is_active: !domain.is_active,
        max_accounts: domain.max_accounts,
      });
    } catch (err) {
      toast.error(errorMessage(err, t("domains.updateFailed")));
    }
  };

  const handleEditDomain = async () => {
    if (!selectedDomain) return;
    setFormError("");

    try {
      await updateDomain(selectedDomain.name, {
        max_accounts: editMaxAccounts,
        is_active: selectedDomain.is_active,
        company_name: editCompanyName,
        from_template_internal: editFromInternal,
        from_template_external: editFromExternal,
      });
      setIsEditDialogOpen(false);
      setSelectedDomain(null);
    } catch (err) {
      setFormError(errorMessage(err, t("domains.updateFailed")));
    }
  };

  const generateDNSRecords = (domain: Domain) => {
    return `# MX Record:
${domain.name}.    IN    MX    10    mail.${domain.name}.

# SPF Record:
${domain.name}.    IN    TXT    "v=spf1 mx ~all"

# DKIM Record:
${domain.dkim_selector || "default"}._domainkey.${domain.name}.    IN    TXT    "v=DKIM1; k=rsa; p=${domain.dkim_public_key?.replace(/\n/g, "") || "KEY"}"

# DMARC Record:
_dmarc.${domain.name}.    IN    TXT    "v=DMARC1; p=quarantine; rua=mailto:dmarc@${domain.name}"`;
  };

  const copyDNSToClipboard = (domain: Domain) => {
    navigator.clipboard.writeText(generateDNSRecords(domain));
    setCopiedDNS(true);
    setTimeout(() => setCopiedDNS(false), 2000);
  };

  return (
    <div className="space-y-6">
      {/* Header */}
      <div className="flex flex-col gap-4 sm:flex-row sm:items-center sm:justify-between">
        <div>
          <h1 className="text-3xl font-bold tracking-tight">{t("domains.title")}</h1>
          <p className="text-muted-foreground mt-1">
            {t("domains.pageDescription")}
          </p>
        </div>
        <Dialog open={isAddDialogOpen} onOpenChange={setIsAddDialogOpen}>
          {/* @ts-expect-error asChild prop not typed in Base UI but works at runtime */}
          <DialogTrigger asChild>
            <Button>
              <Plus className="mr-2 h-4 w-4" />
              {t("domains.add")}
            </Button>
          </DialogTrigger>
          <DialogContent className="sm:max-w-md">
            <DialogHeader>
              <DialogTitle>{t("domains.addNew")}</DialogTitle>
              <DialogDescription>
                {t("domains.addDescription")}
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
                <Label htmlFor="domain">{t("domains.name")}</Label>
                <Input
                  id="domain"
                  placeholder="example.com"
                  value={newDomainName}
                  onChange={(e) => setNewDomainName(e.target.value)}
                />
              </div>
              <div className="space-y-2">
                <Label htmlFor="max-accounts">{t("domains.maxAccounts")}</Label>
                <Input
                  id="max-accounts"
                  type="number"
                  value={newDomainMaxAccounts}
                  onChange={(e) => setNewDomainMaxAccounts(parseInt(e.target.value) || 100)}
                />
              </div>
            </div>
            <DialogFooter>
              <Button variant="outline" onClick={() => setIsAddDialogOpen(false)}>
                {t("common.cancel")}
              </Button>
              <Button onClick={handleCreateDomain}>{t("domains.add")}</Button>
            </DialogFooter>
          </DialogContent>
        </Dialog>
      </div>

      {/* Search and Filter */}
      <div className="flex items-center gap-4">
        <div className="relative flex-1 max-w-sm">
          <Search className="absolute left-3 top-1/2 -translate-y-1/2 h-4 w-4 text-muted-foreground" />
          <Input
            placeholder={t("domains.searchPlaceholder")}
            value={searchQuery}
            onChange={(e) => setSearchQuery(e.target.value)}
            className="pl-10"
          />
        </div>
        <Button
          variant="outline"
          size="icon"
          onClick={() => fetchDomains()}
          disabled={loading}
        >
          <RefreshCw className={cn("h-4 w-4", loading && "animate-spin")} />
        </Button>
      </div>

      {/* Domains List */}
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
      ) : filteredDomains?.length === 0 ? (
        <Card className="text-center py-12">
          <CardContent>
            <Globe className="h-12 w-12 mx-auto text-muted-foreground mb-4" />
            <h3 className="text-lg font-medium">{t("domains.noDomainsFound")}</h3>
            <p className="text-muted-foreground mt-1">
              {searchQuery
                ? t("domains.noMatch")
                : t("domains.getStarted")}
            </p>
            {!searchQuery && (
              <Button className="mt-4" onClick={() => setIsAddDialogOpen(true)}>
                <Plus className="mr-2 h-4 w-4" />
                {t("domains.add")}
              </Button>
            )}
          </CardContent>
        </Card>
      ) : (
        <div className="grid gap-4 md:grid-cols-2 lg:grid-cols-3">
          {filteredDomains?.map((domain: Domain) => (
            <DomainCard
              key={domain.name}
              domain={domain}
              onToggle={() => handleToggleDomain(domain)}
              onEdit={() => {
                setSelectedDomain(domain);
                setEditMaxAccounts(domain.max_accounts);
                setEditCompanyName(domain.company_name ?? "");
                setEditFromInternal(domain.from_template_internal ?? "");
                setEditFromExternal(domain.from_template_external ?? "");
                setFormError("");
                setIsEditDialogOpen(true);
              }}
              onDelete={() => {
                setSelectedDomain(domain);
                setIsDeleteDialogOpen(true);
              }}
              onCopyDNS={() => copyDNSToClipboard(domain)}
              copiedDNS={copiedDNS}
            />
          ))}
        </div>
      )}

      {/* Edit Dialog */}
      <Dialog open={isEditDialogOpen} onOpenChange={setIsEditDialogOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t("domains.edit")}</DialogTitle>
            <DialogDescription>
              {t("domains.editDescription", { name: selectedDomain?.name ?? "" })}
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
              <Label htmlFor="edit-max-accounts">{t("domains.maxAccounts")}</Label>
              <Input
                id="edit-max-accounts"
                type="number"
                min={0}
                value={editMaxAccounts}
                onChange={(e) => setEditMaxAccounts(Math.max(0, Number(e.target.value) || 0))}
              />
            </div>
            <div className="space-y-2">
              <Label htmlFor="edit-company-name">{t("domains.companyName")}</Label>
              <Input
                id="edit-company-name"
                value={editCompanyName}
                placeholder="Acme A.Ş."
                onChange={(e) => setEditCompanyName(e.target.value)}
              />
              <p className="text-xs text-muted-foreground">
                {t("domains.companyNameHelp")}
              </p>
            </div>
            <div className="space-y-2">
              <Label htmlFor="edit-from-internal">{t("domains.internalFromTemplate")}</Label>
              <Input
                id="edit-from-internal"
                value={editFromInternal}
                placeholder="{name} ({title})"
                onChange={(e) => setEditFromInternal(e.target.value)}
              />
            </div>
            <div className="space-y-2">
              <Label htmlFor="edit-from-external">{t("domains.externalFromTemplate")}</Label>
              <Input
                id="edit-from-external"
                value={editFromExternal}
                placeholder="{name} ({company} - {title})"
                onChange={(e) => setEditFromExternal(e.target.value)}
              />
              <p className="text-xs text-muted-foreground">
                {t("domains.fromTemplateHelp")}
              </p>
            </div>
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={() => setIsEditDialogOpen(false)}>
              {t("common.cancel")}
            </Button>
            <Button onClick={handleEditDomain}>{t("common.saveChanges")}</Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* Delete Confirmation Dialog */}
      <Dialog open={isDeleteDialogOpen} onOpenChange={setIsDeleteDialogOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t("domains.delete")}</DialogTitle>
            <DialogDescription>
              {t("domains.confirmDeleteNamed", { name: selectedDomain?.name ?? "" })}
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="outline" onClick={() => setIsDeleteDialogOpen(false)}>
              {t("common.cancel")}
            </Button>
            <Button variant="destructive" onClick={handleDeleteDomain} disabled={isDeleting}>
              <Trash2 className="mr-2 h-4 w-4" />
              {t("common.delete")}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
}

interface DomainCardProps {
  domain: Domain;
  onToggle: () => void;
  onEdit: () => void;
  onDelete: () => void;
  onCopyDNS: () => void;
  copiedDNS: boolean;
}

function DomainCard({ domain, onToggle, onEdit, onDelete, onCopyDNS, copiedDNS }: DomainCardProps) {
  const { t } = useI18n();
  const [showDNS, setShowDNS] = useState(false);

  return (
    <Card className="group">
      <CardHeader className="pb-3">
        <div className="flex items-start justify-between">
          <div className="flex items-center gap-3">
            <div className="p-2 rounded-lg bg-gradient-to-br from-blue-500 to-blue-600">
              <Globe className="h-5 w-5 text-white" />
            </div>
            <div>
              <CardTitle className="text-lg">{domain.name}</CardTitle>
              <CardDescription>
                {domain.is_active ? (
                  <span className="flex items-center gap-1 text-emerald-500">
                    <Shield className="h-3 w-3" />
                    {t("common.active")}
                  </span>
                ) : (
                  <span className="text-muted-foreground">{t("common.inactive")}</span>
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
                <Pencil className="mr-2 h-4 w-4" />
                {t("common.edit")}
              </DropdownMenuItem>
              <DropdownMenuItem onClick={() => setShowDNS(true)}>
                <Mail className="mr-2 h-4 w-4" />
                {t("domains.viewDnsRecords")}
              </DropdownMenuItem>
              <DropdownMenuItem onClick={onCopyDNS}>
                {copiedDNS ? (
                  <Check className="mr-2 h-4 w-4" />
                ) : (
                  <Copy className="mr-2 h-4 w-4" />
                )}
                {copiedDNS ? t("common.copied") : t("domains.copyDnsRecords")}
              </DropdownMenuItem>
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
          <div className="flex items-center justify-between">
            <span className="text-sm text-muted-foreground">{t("domains.maxAccounts")}</span>
            <Badge variant="secondary">{domain.max_accounts}</Badge>
          </div>
          <div className="flex items-center justify-between">
            <span className="text-sm text-muted-foreground">{t("common.status")}</span>
            <div className="flex items-center gap-2">
              <Switch checked={domain.is_active} onCheckedChange={onToggle} />
            </div>
          </div>
          {domain.dkim_selector && (
            <div
              className="flex items-center gap-2 pt-2"
              title={t("domains.dkimKeyTooltip")}
            >
              <Shield className="h-4 w-4 text-emerald-500" />
              <span className="text-sm text-muted-foreground">{t("domains.dkimKeyConfigured")}</span>
            </div>
          )}
        </div>
      </CardContent>

      {/* DNS Records Dialog */}
      <Dialog open={showDNS} onOpenChange={setShowDNS}>
        <DialogContent className="max-w-2xl">
          <DialogHeader>
            <DialogTitle>{t("domains.dnsRecordsFor", { name: domain.name })}</DialogTitle>
            <DialogDescription>
              {t("domains.dnsRecordsDescription")}
            </DialogDescription>
          </DialogHeader>
          <div className="bg-muted rounded-lg p-4 font-mono text-sm overflow-x-auto">
            <pre>{`# MX Record:
${domain.name}.    IN    MX    10    mail.${domain.name}.

# SPF Record:
${domain.name}.    IN    TXT    "v=spf1 mx ~all"

# DKIM Record:
${domain.dkim_selector || "default"}._domainkey.${domain.name}.    IN    TXT    "v=DKIM1; k=rsa; p=${domain.dkim_public_key?.replace(/\n/g, "") || "KEY"}"

# DMARC Record:
_dmarc.${domain.name}.    IN    TXT    "v=DMARC1; p=quarantine; rua=mailto:dmarc@${domain.name}"`}</pre>
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={() => onCopyDNS()}>
              {copiedDNS ? (
                <>
                  <Check className="mr-2 h-4 w-4" />
                  {t("common.copied")}
                </>
              ) : (
                <>
                  <Copy className="mr-2 h-4 w-4" />
                  {t("domains.copyRecords")}
                </>
              )}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </Card>
  );
}
