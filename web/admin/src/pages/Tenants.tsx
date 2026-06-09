import { useState, useEffect, useCallback } from "react";
import { Building2, RefreshCw, Save, Plus, Trash2, AlertCircle, Image, Upload } from "lucide-react";
import { toast } from "sonner";
import { Button, buttonVariants } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { cn } from "@/lib/utils";
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
  tagline: "",
  footer_text: "",
  features: {},
};

// Cap an uploaded logo so the public, pre-auth /api/v1/branding response (which
// inlines the data URL) stays small.
const MAX_LOGO_BYTES = 512 * 1024;

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

  // handleLogoFile reads a chosen image into a data URL and stores it as the
  // logo_url, so the login screen renders it without an external host.
  const handleLogoFile = (file: File) => {
    if (!file.type.startsWith("image/")) {
      toast.error(t("tenants.logoMustBeImage"));
      return;
    }
    if (file.size > MAX_LOGO_BYTES) {
      toast.error(t("tenants.logoTooLarge"));
      return;
    }
    const reader = new FileReader();
    reader.onload = () => setBranding((b) => ({ ...b, logo_url: String(reader.result) }));
    reader.onerror = () => toast.error(t("tenants.logoReadFailed"));
    reader.readAsDataURL(file);
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
                  <Label htmlFor="logo_url">{t("tenants.logo")}</Label>
                  <div className="flex items-center gap-3">
                    {branding.logo_url ? (
                      <img
                        src={branding.logo_url}
                        alt={t("tenants.logo")}
                        className="h-12 w-12 rounded-lg object-contain border border-gray-200 bg-white"
                      />
                    ) : (
                      <div className="h-12 w-12 rounded-lg border border-dashed border-gray-300 flex items-center justify-center text-gray-300">
                        <Image className="h-5 w-5" />
                      </div>
                    )}
                    <div className="flex items-center gap-2">
                      <label className={cn(buttonVariants({ variant: "outline", size: "sm" }), "cursor-pointer")}>
                        <Upload className="mr-2 h-4 w-4" />
                        {t("tenants.uploadLogo")}
                        <input
                          type="file"
                          accept="image/*"
                          className="hidden"
                          onChange={(e) => {
                            const f = e.target.files?.[0];
                            if (f) handleLogoFile(f);
                            e.target.value = "";
                          }}
                        />
                      </label>
                      {branding.logo_url && (
                        <Button
                          variant="ghost"
                          size="sm"
                          onClick={() => setBranding((b) => ({ ...b, logo_url: "" }))}
                        >
                          {t("common.remove")}
                        </Button>
                      )}
                    </div>
                  </div>
                  <Input
                    id="logo_url"
                    value={branding.logo_url.startsWith("data:") ? "" : branding.logo_url}
                    placeholder={t("tenants.logoUrlPlaceholder")}
                    onChange={(e) => setBranding((b) => ({ ...b, logo_url: e.target.value }))}
                  />
                  <p className="text-xs text-gray-500">{t("tenants.logoHint")}</p>
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
                  <Label htmlFor="tagline">{t("tenants.loginTagline")}</Label>
                  <Input
                    id="tagline"
                    value={branding.tagline}
                    placeholder={t("tenants.loginTaglinePlaceholder")}
                    onChange={(e) => setBranding((b) => ({ ...b, tagline: e.target.value }))}
                  />
                  <p className="text-xs text-gray-500">{t("tenants.loginTaglineHint")}</p>
                </div>

                <div className="space-y-2">
                  <Label htmlFor="footer_text">{t("tenants.loginFooter")}</Label>
                  <Input
                    id="footer_text"
                    value={branding.footer_text}
                    placeholder={t("tenants.loginFooterPlaceholder")}
                    onChange={(e) => setBranding((b) => ({ ...b, footer_text: e.target.value }))}
                  />
                  <p className="text-xs text-gray-500">{t("tenants.loginFooterHint")}</p>
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
