import { useEffect, useState } from "react";
import { Activity, AlertTriangle, CheckCircle2, Loader2, RefreshCw, XCircle } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Skeleton } from "@/components/ui/skeleton";
import { Badge } from "@/components/ui/badge";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { useDNSHealth } from "@/hooks/useApi";
import { useI18n } from "@/hooks/useI18n";
import type { DNSCheckResult } from "@/types";

interface DNSHealthDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  domain: string | null;
}

// statusIcon returns a status-appropriate icon; kept small so the table row
// stays compact while still telegraphing the result at a glance.
function statusIcon(status: DNSCheckResult["status"]) {
  switch (status) {
    case "pass":
      return <CheckCircle2 className="h-4 w-4 text-emerald-500" aria-hidden="true" />;
    case "fail":
      return <XCircle className="h-4 w-4 text-red-500" aria-hidden="true" />;
    case "warning":
      return <AlertTriangle className="h-4 w-4 text-amber-500" aria-hidden="true" />;
    default:
      return null;
  }
}

// statusBadgeVariant picks a Badge variant that visually maps to the
// severity, so the table is readable in monochrome printouts.
function statusBadgeVariant(
  status: DNSCheckResult["status"]
): "default" | "destructive" | "secondary" | "outline" {
  switch (status) {
    case "pass":
      return "default";
    case "fail":
      return "destructive";
    case "warning":
      return "secondary";
    default:
      return "outline";
  }
}

// DNSHealthDialog shows the live DNS health probe for one domain. The probe
// hits GET /api/v1/admin/domains/{domain}/dns-check and renders the per-record
// results (MX/SPF/DKIM/DMARC/PTR) with a status badge. It does NOT auto-run;
// the operator clicks "Run check" to fetch fresh results.
export function DNSHealthDialog({ open, onOpenChange, domain }: DNSHealthDialogProps) {
  const { t } = useI18n();
  const { results, loading, error, checkDNS, reset } = useDNSHealth();
  const [hasFetched, setHasFetched] = useState(false);

  // When the dialog opens for a different domain, drop the previous results
  // and flag that nothing has been fetched for the new selection. This
  // prevents leaking the previous domain's rows into a fresh dialog.
  useEffect(() => {
    if (open) {
      reset();
      setHasFetched(false);
    }
  }, [open, domain, reset]);

  const runCheck = async () => {
    if (!domain) return;
    setHasFetched(true);
    try {
      await checkDNS(domain);
    } catch {
      // useDNSHealth already populated `error`; no extra handling needed.
    }
  };

  const errorMessage = (() => {
    if (!error) return null;
    if (typeof error === "object" && "message" in error) {
      const msg = (error as { message?: unknown }).message;
      if (typeof msg === "string" && msg) return msg;
    }
    return t("domains.dnsCheck.failed");
  })();

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-3xl">
        <DialogHeader>
          <DialogTitle>
            {t("domains.dnsCheck.title", { domain: domain ?? "" })}
          </DialogTitle>
          <DialogDescription>{t("domains.dnsCheck.description")}</DialogDescription>
        </DialogHeader>

        {errorMessage && (
          <Alert variant="destructive">
            <AlertTriangle className="h-4 w-4" />
            <AlertDescription>{errorMessage}</AlertDescription>
          </Alert>
        )}

        <div className="space-y-3">
          {!hasFetched && !loading && (
            <div className="flex flex-col items-center justify-center gap-3 py-8 text-center">
              <Activity className="h-8 w-8 text-muted-foreground" />
              <p className="text-sm text-muted-foreground">
                {t("domains.dnsCheck.notRun")}
              </p>
            </div>
          )}

          {loading && (
            <div className="space-y-2">
              {[1, 2, 3, 4, 5].map((i) => (
                <Skeleton key={i} className="h-10 w-full" />
              ))}
            </div>
          )}

          {!loading && results && results.length > 0 && (
            <div className="border rounded-lg overflow-hidden">
              <table className="w-full text-sm">
                <thead className="bg-muted">
                  <tr>
                    <th className="text-left font-medium px-3 py-2">
                      {t("domains.dnsCheck.type")}
                    </th>
                    <th className="text-left font-medium px-3 py-2">
                      {t("domains.dnsCheck.status")}
                    </th>
                    <th className="text-left font-medium px-3 py-2">
                      {t("domains.dnsCheck.message")}
                    </th>
                  </tr>
                </thead>
                <tbody>
                  {results.map((r, idx) => (
                    <tr
                      key={`${r.record_type}-${r.record_name}-${idx}`}
                      className="border-t"
                      data-testid={`dns-check-row-${r.record_type.toLowerCase()}`}
                    >
                      <td className="px-3 py-2 font-mono text-xs">{r.record_type}</td>
                      <td className="px-3 py-2">
                        <Badge variant={statusBadgeVariant(r.status)} className="gap-1">
                          {statusIcon(r.status)}
                          {r.status}
                        </Badge>
                      </td>
                      <td className="px-3 py-2 text-muted-foreground">{r.message}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </div>

        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>
            {t("common.close")}
          </Button>
          <Button onClick={runCheck} disabled={loading || !domain} data-testid="dns-check-run">
            {loading ? (
              <>
                <Loader2 className="mr-2 h-4 w-4 animate-spin" />
                {t("domains.dnsCheck.running")}
              </>
            ) : (
              <>
                <RefreshCw className="mr-2 h-4 w-4" />
                {t("domains.dnsCheck.run")}
              </>
            )}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
