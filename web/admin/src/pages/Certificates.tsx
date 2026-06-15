import { useCallback, useEffect } from "react";
import { ShieldCheck, RefreshCw, CheckCircle, AlertTriangle, XCircle } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Skeleton } from "@/components/ui/skeleton";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { cn } from "@/lib/utils";
import { useTLSCertificates } from "@/hooks/useApi";
import { useI18n } from "@/hooks/useI18n";
import type { TLSCertificate } from "@/types";

type TranslateFn = (key: string, params?: Record<string, string>) => string;

const dayMs = 24 * 60 * 60 * 1000;

function daysUntil(iso?: string): number | null {
  if (!iso) return null;
  const at = new Date(iso).getTime();
  if (Number.isNaN(at)) return null;
  return Math.floor((at - Date.now()) / dayMs);
}

function statusBadge(cert: TLSCertificate, t: TranslateFn) {
  if (!cert.valid) {
    return (
      <Badge className="bg-red-500/10 text-red-500 hover:bg-red-500/10">
        <XCircle className="mr-1 h-3 w-3" />
        {t("certificates.invalid")}
      </Badge>
    );
  }
  const remaining = daysUntil(cert.expires_at);
  if (remaining !== null && remaining <= 0) {
    return (
      <Badge className="bg-red-500/10 text-red-500 hover:bg-red-500/10">
        <XCircle className="mr-1 h-3 w-3" />
        {t("certificates.expired")}
      </Badge>
    );
  }
  if (cert.warning || (remaining !== null && remaining <= 14)) {
    return (
      <Badge className="bg-amber-500/10 text-amber-500 hover:bg-amber-500/10">
        <AlertTriangle className="mr-1 h-3 w-3" />
        {t("certificates.expiringSoon")}
      </Badge>
    );
  }
  return (
    <Badge className="bg-emerald-500/10 text-emerald-500 hover:bg-emerald-500/10">
      <CheckCircle className="mr-1 h-3 w-3" />
      {t("certificates.valid")}
    </Badge>
  );
}

function expiryLabel(cert: TLSCertificate, t: TranslateFn): string {
  if (!cert.expires_at) return "—";
  const remaining = daysUntil(cert.expires_at);
  const date = new Date(cert.expires_at).toLocaleDateString();
  if (remaining === null) return date;
  if (remaining <= 0) return t("certificates.expired");
  return t("certificates.expiresInDays", { days: String(remaining), date });
}

export function Certificates() {
  const { t } = useI18n();
  const { certificates, loading, fetchCertificates } = useTLSCertificates();

  const refresh = useCallback(() => {
    fetchCertificates().catch(() => {
      /* error surfaced via hook state */
    });
  }, [fetchCertificates]);

  useEffect(() => {
    refresh();
  }, [refresh]);

  return (
    <div className="space-y-6">
      <div className="flex flex-col gap-4 sm:flex-row sm:items-center sm:justify-between">
        <div>
          <h1 className="text-3xl font-bold tracking-tight">{t("certificates.title")}</h1>
          <p className="text-muted-foreground mt-1">{t("certificates.description")}</p>
        </div>
        <Button variant="outline" onClick={refresh} disabled={loading}>
          <RefreshCw className={cn("mr-2 h-4 w-4", loading && "animate-spin")} />
          {t("common.refresh")}
        </Button>
      </div>

      <Card>
        <CardHeader>
          <CardTitle className="flex items-center gap-2">
            <ShieldCheck className="h-5 w-5" />
            {t("certificates.inventory")}
          </CardTitle>
          <CardDescription>{t("certificates.inventoryDescription")}</CardDescription>
        </CardHeader>
        <CardContent>
          {loading && certificates.length === 0 ? (
            <div className="space-y-3">
              <Skeleton className="h-10 w-full" />
              <Skeleton className="h-10 w-full" />
            </div>
          ) : certificates.length === 0 ? (
            <div className="text-center py-8">
              <ShieldCheck className="h-12 w-12 mx-auto text-muted-foreground mb-4" />
              <h3 className="text-lg font-medium">{t("certificates.empty")}</h3>
              <p className="text-muted-foreground mt-1 max-w-md mx-auto">
                {t("certificates.emptyDescription")}
              </p>
            </div>
          ) : (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>{t("certificates.domain")}</TableHead>
                  <TableHead>{t("common.status")}</TableHead>
                  <TableHead>{t("certificates.expiry")}</TableHead>
                  <TableHead>{t("certificates.issuer")}</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {certificates.map((cert) => (
                  <TableRow key={cert.domain}>
                    <TableCell className="font-mono">{cert.domain}</TableCell>
                    <TableCell>{statusBadge(cert, t)}</TableCell>
                    <TableCell className="text-muted-foreground">{expiryLabel(cert, t)}</TableCell>
                    <TableCell className="text-muted-foreground">{cert.issuer || "—"}</TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          )}
        </CardContent>
      </Card>
    </div>
  );
}
