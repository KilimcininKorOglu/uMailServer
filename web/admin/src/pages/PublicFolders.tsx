import { useState, useEffect } from "react";
import { FolderLock, Plus, Trash2, X } from "lucide-react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Badge } from "@/components/ui/badge";
import {
  Card,
  CardContent,
  CardHeader,
  CardTitle,
  CardDescription,
} from "@/components/ui/card";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { useDomains, usePublicFolders } from "@/hooks/useApi";
import { useI18n } from "@/hooks/useI18n";

// RFC 4314 rights presets surfaced to the admin. "Read" lets a grantee see and
// open the folder; "Post" additionally allows appending messages (IMAP APPEND
// and webmail post both gate on the write bit).
const READ_RIGHTS = "lr";
const POST_RIGHTS = "lrsw";

/**
 * PublicFolders is the super-admin page for the per-domain public-folder tree.
 * The admin picks a domain, creates or deletes named folders under the reserved
 * public owner, and edits each folder's ACL grants (the "anyone" token for
 * org-wide access or a specific in-domain address).
 */
export function PublicFolders() {
  const { t } = useI18n();
  const { domains, fetchDomains } = useDomains();
  const {
    owner,
    folders,
    loading,
    fetchPublicFolders,
    createPublicFolder,
    deletePublicFolder,
    setPublicFolderACL,
  } = usePublicFolders();

  const [domain, setDomain] = useState("");
  const [newFolder, setNewFolder] = useState("");
  const [granteeDraft, setGranteeDraft] = useState<Record<string, string>>({});
  const [rightsDraft, setRightsDraft] = useState<Record<string, string>>({});

  useEffect(() => {
    fetchDomains();
  }, [fetchDomains]);

  useEffect(() => {
    if (domain) {
      fetchPublicFolders(domain).catch(() => undefined);
    }
  }, [domain, fetchPublicFolders]);

  const rightsLabel = (rights: string) => {
    if (rights === READ_RIGHTS) return t("publicFolders.read");
    if (rights === POST_RIGHTS) return t("publicFolders.post");
    return rights;
  };

  const handleCreate = async () => {
    const name = newFolder.trim();
    if (!domain || !name) return;
    try {
      await createPublicFolder(domain, name);
      setNewFolder("");
      toast.success(t("publicFolders.created"));
    } catch {
      toast.error(t("publicFolders.createFailed"));
    }
  };

  const handleDelete = async (name: string) => {
    try {
      await deletePublicFolder(domain, name);
      toast.success(t("publicFolders.deleted"));
    } catch {
      toast.error(t("publicFolders.deleteFailed"));
    }
  };

  const handleAddGrant = async (folder: string) => {
    const grantee = (granteeDraft[folder] ?? "").trim() || "anyone";
    const rights = rightsDraft[folder] ?? READ_RIGHTS;
    try {
      await setPublicFolderACL(domain, folder, grantee, rights);
      setGranteeDraft((d) => ({ ...d, [folder]: "" }));
      toast.success(t("publicFolders.grantSaved"));
    } catch {
      toast.error(t("publicFolders.grantFailed"));
    }
  };

  const handleRemoveGrant = async (folder: string, grantee: string) => {
    try {
      await setPublicFolderACL(domain, folder, grantee, "");
      toast.success(t("publicFolders.grantRemoved"));
    } catch {
      toast.error(t("publicFolders.grantFailed"));
    }
  };

  return (
    <div className="space-y-6">
      <header className="space-y-1">
        <h1 className="text-2xl font-semibold tracking-tight flex items-center gap-2">
          <FolderLock className="h-6 w-6" />
          {t("publicFolders.title")}
        </h1>
        <p className="text-sm text-muted-foreground">
          {t("publicFolders.description")}
        </p>
      </header>

      <Card>
        <CardHeader>
          <CardTitle>{t("publicFolders.domain")}</CardTitle>
          <CardDescription>{t("publicFolders.selectDomainHelp")}</CardDescription>
        </CardHeader>
        <CardContent className="flex flex-col gap-2 sm:flex-row sm:items-center">
          <Select value={domain} onValueChange={(v) => setDomain(v ?? "")}>
            <SelectTrigger className="w-72">
              <SelectValue placeholder={t("publicFolders.selectDomain")} />
            </SelectTrigger>
            <SelectContent>
              {(domains ?? []).map((d) => (
                <SelectItem key={d.name} value={d.name}>
                  {d.name}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          {owner && (
            <Badge variant="secondary" className="font-mono">
              {owner}
            </Badge>
          )}
        </CardContent>
      </Card>

      {domain && (
        <>
          <Card>
            <CardHeader>
              <CardTitle>{t("publicFolders.add")}</CardTitle>
            </CardHeader>
            <CardContent className="flex flex-col gap-2 sm:flex-row sm:items-center">
              <Input
                value={newFolder}
                placeholder={t("publicFolders.namePlaceholder")}
                onChange={(e) => setNewFolder(e.target.value)}
                onKeyDown={(e) => {
                  if (e.key === "Enter") handleCreate();
                }}
                className="sm:max-w-xs"
              />
              <Button onClick={handleCreate} disabled={!newFolder.trim()}>
                <Plus className="mr-1 h-4 w-4" />
                {t("publicFolders.add")}
              </Button>
            </CardContent>
          </Card>

          {!loading && folders.length === 0 && (
            <p className="text-sm text-muted-foreground">
              {t("publicFolders.empty")}
            </p>
          )}

          {folders.map((folder) => (
            <Card key={folder.name}>
              <CardHeader className="flex flex-row items-center justify-between space-y-0">
                <CardTitle className="flex items-center gap-2">
                  <FolderLock className="h-4 w-4" />
                  {folder.name}
                </CardTitle>
                <Button
                  variant="ghost"
                  size="sm"
                  onClick={() => handleDelete(folder.name)}
                >
                  <Trash2 className="h-4 w-4" />
                </Button>
              </CardHeader>
              <CardContent className="space-y-4">
                <div className="space-y-2">
                  <Label className="text-xs uppercase text-muted-foreground">
                    {t("publicFolders.grants")}
                  </Label>
                  {folder.grants.length === 0 ? (
                    <p className="text-sm text-muted-foreground">
                      {t("publicFolders.noGrants")}
                    </p>
                  ) : (
                    <ul className="space-y-1">
                      {folder.grants.map((g) => (
                        <li
                          key={g.grantee}
                          className="flex items-center justify-between rounded-md border px-3 py-1.5 text-sm"
                        >
                          <span className="flex items-center gap-2">
                            <span className="font-medium">{g.grantee}</span>
                            <Badge variant="outline">{rightsLabel(g.rights)}</Badge>
                          </span>
                          <Button
                            variant="ghost"
                            size="sm"
                            onClick={() => handleRemoveGrant(folder.name, g.grantee)}
                          >
                            <X className="h-4 w-4" />
                          </Button>
                        </li>
                      ))}
                    </ul>
                  )}
                </div>

                <div className="flex flex-col gap-2 sm:flex-row sm:items-end">
                  <div className="space-y-1">
                    <Label className="text-xs">{t("publicFolders.grantee")}</Label>
                    <Input
                      value={granteeDraft[folder.name] ?? ""}
                      placeholder={t("publicFolders.granteePlaceholder")}
                      onChange={(e) =>
                        setGranteeDraft((d) => ({ ...d, [folder.name]: e.target.value }))
                      }
                      className="sm:w-64"
                    />
                  </div>
                  <div className="space-y-1">
                    <Label className="text-xs">{t("publicFolders.permission")}</Label>
                    <Select
                      value={rightsDraft[folder.name] ?? READ_RIGHTS}
                      onValueChange={(v) =>
                        setRightsDraft((d) => ({ ...d, [folder.name]: v ?? READ_RIGHTS }))
                      }
                    >
                      <SelectTrigger className="w-40">
                        <SelectValue />
                      </SelectTrigger>
                      <SelectContent>
                        <SelectItem value={READ_RIGHTS}>
                          {t("publicFolders.read")}
                        </SelectItem>
                        <SelectItem value={POST_RIGHTS}>
                          {t("publicFolders.post")}
                        </SelectItem>
                      </SelectContent>
                    </Select>
                  </div>
                  <Button variant="secondary" onClick={() => handleAddGrant(folder.name)}>
                    {t("publicFolders.saveGrant")}
                  </Button>
                </div>
              </CardContent>
            </Card>
          ))}
        </>
      )}
    </div>
  );
}
