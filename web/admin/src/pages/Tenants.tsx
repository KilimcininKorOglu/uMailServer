import { useState, useEffect, useCallback } from "react";
import { Building2, RefreshCw, Save, Plus, Trash2, AlertCircle } from "lucide-react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Card, CardContent } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Skeleton } from "@/components/ui/skeleton";
import { useTenants } from "@/hooks/useApi";
import { useI18n } from "@/hooks/useI18n";
import type { Tenant, TenantBranding } from "@/types";

function errorMessage(err: unknown, fallback: string): string {
  if (err instanceof Error) return err.message;
  if (typeof err === "object" && err !== null && "message" in err) {
    const msg = (err as { message?: unknown }).message;
    if (typeof msg === "string" && msg) return msg;
  }
  return fallback;
}

const emptyBranding: TenantBranding = {
  app_name: "",
  logo_url: "",
  primary_color: "",
  features: {},
};

export function Tenants() {
  const { t } = useI18n();
  const { tenants, loading, error, fetchTenants, fetchBranding, updateBranding } = useTenants();
  const [selected, setSelected] = useState<Tenant | null>(null);
  const [branding, setBranding] = useState<TenantBranding>(emptyBranding);
  const [newFeature, setNewFeature] = useState("");
  const [saving, setSaving] = useState(false);

  useEffect(() => {
    fetchTenants().catch(() => {});
  }, [fetchTenants]);

  const selectTenant = useCallback(
    async (tenant: Tenant) => {
      setSelected(tenant);
      try {
        const b = await fetchBranding(tenant.id);
        setBranding({ ...emptyBranding, ...b, features: b.features ?? {} });
      } catch (err) {
        toast.error(errorMessage(err, t("tenants.loadBrandingFailed")));
      }
    },
    [fetchBranding]
  );

  const save = useCallback(async () => {
    if (!selected) return;
    setSaving(true);
    try {
      const saved = await updateBranding(selected.id, branding);
      setBranding({ ...emptyBranding, ...saved, features: saved.features ?? {} });
      toast.success(t("tenants.brandingSaved", { id: selected.id }));
    } catch (err) {
      toast.error(errorMessage(err, t("tenants.saveBrandingFailed")));
    } finally {
      setSaving(false);
    }
  }, [selected, branding, updateBranding]);

  const addFeature = () => {
    const name = newFeature.trim();
    if (!name) return;
    setBranding((b) => ({ ...b, features: { ...b.features, [name]: true } }));
    setNewFeature("");
  };

  const featureNames = Object.keys(branding.features).sort();

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <div className="flex items-center gap-3">
          <Building2 className="h-6 w-6 text-indigo-600" />
          <h1 className="text-2xl font-semibold">{t("tenants.title")}</h1>
        </div>
        <Button variant="outline" size="sm" onClick={() => fetchTenants().catch(() => {})}>
          <RefreshCw className="h-4 w-4 mr-2" />
          {t("common.refresh")}
        </Button>
      </div>

      {error && (
        <Alert variant="destructive">
          <AlertCircle className="h-4 w-4" />
          <AlertDescription>{errorMessage(error, t("tenants.loadTenantsFailed"))}</AlertDescription>
        </Alert>
      )}

      <div className="grid grid-cols-1 lg:grid-cols-3 gap-6">
        {/* Tenant list */}
        <Card className="lg:col-span-1">
          <CardContent className="p-4 space-y-2">
            {loading && !tenants ? (
              <Skeleton className="h-24 w-full" />
            ) : (
              (tenants ?? []).map((tenant) => (
                <button
                  key={tenant.id}
                  onClick={() => selectTenant(tenant)}
                  className={`w-full text-left rounded-lg border p-3 transition-colors ${
                    selected?.id === tenant.id ? "border-indigo-500 bg-indigo-50" : "border-gray-200 hover:bg-gray-50"
                  }`}
                >
                  <div className="flex items-center justify-between">
                    <span className="font-medium">{tenant.name}</span>
                    <Badge variant={tenant.is_active ? "default" : "secondary"}>
                      {tenant.is_active ? t("common.active") : t("tenants.suspended")}
                    </Badge>
                  </div>
                  <div className="text-xs text-gray-500 mt-1">{tenant.id}</div>
                </button>
              ))
            )}
            {tenants && tenants.length === 0 && (
              <p className="text-sm text-gray-500">{t("tenants.noTenants")}</p>
            )}
          </CardContent>
        </Card>

        {/* Branding editor */}
        <Card className="lg:col-span-2">
          <CardContent className="p-6">
            {!selected ? (
              <p className="text-sm text-gray-500">{t("tenants.selectTenantPrompt")}</p>
            ) : (
              <div className="space-y-5">
                <h2 className="text-lg font-medium">{t("tenants.brandingHeading", { id: selected.id })}</h2>

                <div className="space-y-2">
                  <Label htmlFor="app_name">{t("tenants.applicationName")}</Label>
                  <Input
                    id="app_name"
                    value={branding.app_name}
                    placeholder="uMailServer"
                    onChange={(e) => setBranding((b) => ({ ...b, app_name: e.target.value }))}
                  />
                </div>

                <div className="space-y-2">
                  <Label htmlFor="logo_url">{t("tenants.logoUrl")}</Label>
                  <Input
                    id="logo_url"
                    value={branding.logo_url}
                    placeholder="https://example.com/logo.png"
                    onChange={(e) => setBranding((b) => ({ ...b, logo_url: e.target.value }))}
                  />
                </div>

                <div className="space-y-2">
                  <Label htmlFor="primary_color">{t("tenants.primaryColor")}</Label>
                  <div className="flex items-center gap-3">
                    <input
                      id="primary_color"
                      type="color"
                      value={branding.primary_color || "#4f46e5"}
                      onChange={(e) => setBranding((b) => ({ ...b, primary_color: e.target.value }))}
                      className="h-9 w-12 rounded border border-gray-300 p-0"
                    />
                    <Input
                      value={branding.primary_color}
                      placeholder="#4f46e5"
                      onChange={(e) => setBranding((b) => ({ ...b, primary_color: e.target.value }))}
                      className="max-w-[160px]"
                    />
                  </div>
                </div>

                <div className="space-y-2">
                  <Label>{t("tenants.featureFlags")}</Label>
                  <div className="space-y-2">
                    {featureNames.map((name) => (
                      <div key={name} className="flex items-center justify-between rounded border border-gray-200 px-3 py-2">
                        <span className="text-sm font-mono">{name}</span>
                        <div className="flex items-center gap-3">
                          <Switch
                            checked={branding.features[name]}
                            onCheckedChange={(on) =>
                              setBranding((b) => ({ ...b, features: { ...b.features, [name]: on } }))
                            }
                          />
                          <button
                            onClick={() =>
                              setBranding((b) => {
                                const next = { ...b.features };
                                delete next[name];
                                return { ...b, features: next };
                              })
                            }
                            className="text-gray-400 hover:text-red-600"
                            aria-label={t("tenants.removeFeature", { name })}
                          >
                            <Trash2 className="h-4 w-4" />
                          </button>
                        </div>
                      </div>
                    ))}
                    {featureNames.length === 0 && (
                      <p className="text-sm text-gray-500">{t("tenants.noFeatureFlags")}</p>
                    )}
                  </div>
                  <div className="flex items-center gap-2 pt-1">
                    <Input
                      value={newFeature}
                      placeholder={t("tenants.featureNamePlaceholder")}
                      onChange={(e) => setNewFeature(e.target.value)}
                      onKeyDown={(e) => {
                        if (e.key === "Enter") {
                          e.preventDefault();
                          addFeature();
                        }
                      }}
                      className="max-w-[260px]"
                    />
                    <Button variant="outline" size="sm" onClick={addFeature}>
                      <Plus className="h-4 w-4 mr-1" />
                      {t("common.add")}
                    </Button>
                  </div>
                </div>

                <div className="pt-2">
                  <Button onClick={save} disabled={saving}>
                    <Save className="h-4 w-4 mr-2" />
                    {saving ? t("common.saving") : t("tenants.saveBranding")}
                  </Button>
                </div>
              </div>
            )}
          </CardContent>
        </Card>
      </div>
    </div>
  );
}

export default Tenants;
