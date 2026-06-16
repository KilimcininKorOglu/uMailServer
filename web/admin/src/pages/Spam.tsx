import { useState, useEffect } from "react";
import {
  ShieldAlert,
  RefreshCw,
  ChevronLeft,
  ChevronRight,
  Filter,
  AlertCircle,
  XCircle,
  Inbox,
} from "lucide-react";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Input } from "@/components/ui/input";
import { Skeleton } from "@/components/ui/skeleton";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { cn } from "@/lib/utils";
import { useAntispam } from "@/hooks/useApi";
import { useI18n } from "@/hooks/useI18n";

function VerdictBadge({ verdict }: { verdict: string }) {
  const { t } = useI18n();
  switch (verdict) {
    case "reject":
      return (
        <Badge variant="destructive" className="gap-1">
          <XCircle className="h-3 w-3" />
          {t("spam.reject")}
        </Badge>
      );
    case "quarantine":
      return (
        <Badge variant="outline" className="gap-1 border-amber-500 text-amber-600">
          <AlertCircle className="h-3 w-3" />
          {t("spam.quarantine")}
        </Badge>
      );
    case "junk":
      return (
        <Badge variant="secondary" className="gap-1">
          <AlertCircle className="h-3 w-3" />
          {t("spam.junk")}
        </Badge>
      );
    case "inbox":
      return (
        <Badge variant="outline" className="gap-1 border-green-500 text-green-600">
          <Inbox className="h-3 w-3" />
          {t("spam.inbox")}
        </Badge>
      );
    default:
      return <Badge variant="outline">{verdict || "—"}</Badge>;
  }
}

const ITEMS_PER_PAGE = 20;

export function Spam() {
  const { t } = useI18n();
  const { entries, total, loading, error: _error, fetchHistory } = useAntispam();
  const [verdictFilter, setVerdictFilter] = useState<string>("all");
  const [domainFilter, setDomainFilter] = useState("");
  const [currentPage, setCurrentPage] = useState(1);

  const load = (offset = 0) => {
    const params: { verdict?: string; domain?: string; limit: number; offset?: number } = {
      limit: ITEMS_PER_PAGE,
    };
    if (verdictFilter !== "all") {
      params.verdict = verdictFilter;
    }
    if (domainFilter.trim()) {
      params.domain = domainFilter.trim();
    }
    if (offset > 0) {
      params.offset = offset;
    }
    fetchHistory(params).catch(() => {
      /* error via hook state */
    });
  };

  useEffect(() => {
    load();
  }, []);

  const handlePageChange = (page: number) => {
    setCurrentPage(page);
    load((page - 1) * ITEMS_PER_PAGE);
  };

  const totalPages = Math.ceil(total / ITEMS_PER_PAGE);

  return (
    <div className="space-y-6">
      {/* Header */}
      <div className="flex flex-col gap-4 sm:flex-row sm:items-center sm:justify-between">
        <div>
          <h1 className="text-3xl font-bold tracking-tight">{t("spam.title")}</h1>
          <p className="text-muted-foreground mt-1">{t("spam.description")}</p>
        </div>
        <Button
          variant="outline"
          onClick={() => load(0)}
          disabled={loading}
        >
          <RefreshCw className={cn("mr-2 h-4 w-4", loading && "animate-spin")} />
          {t("common.refresh")}
        </Button>
      </div>

      {/* Filters */}
      <Card>
        <CardContent className="pt-6">
          <div className="flex flex-col gap-4 sm:flex-row sm:items-center">
            <div className="flex items-center gap-2 flex-1">
              <Filter className="h-4 w-4 text-muted-foreground" />
              <span className="text-sm font-medium">{t("spam.filter")}:</span>
            </div>
            <Select
              value={verdictFilter}
              onValueChange={(v) => {
                if (!v) return;
                setVerdictFilter(v);
                setCurrentPage(1);
                load(0);
              }}
            >
              <SelectTrigger className="w-40">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="all">{t("spam.allVerdicts")}</SelectItem>
                <SelectItem value="reject">{t("spam.reject")}</SelectItem>
                <SelectItem value="quarantine">{t("spam.quarantine")}</SelectItem>
                <SelectItem value="junk">{t("spam.junk")}</SelectItem>
                <SelectItem value="inbox">{t("spam.inbox")}</SelectItem>
              </SelectContent>
            </Select>
            <Input
              placeholder={t("spam.domainPlaceholder")}
              value={domainFilter}
              onChange={(e) => setDomainFilter(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === "Enter") {
                  setCurrentPage(1);
                  const params: Parameters<typeof fetchHistory>[0] = { limit: ITEMS_PER_PAGE };
                  if (verdictFilter !== "all") params.verdict = verdictFilter;
                  if (domainFilter.trim()) params.domain = domainFilter.trim();
                  fetchHistory(params).catch(() => {});
                }
              }}
              className="max-w-xs"
            />
          </div>
        </CardContent>
      </Card>

      {/* Summary stats */}
      <div className="grid gap-4 md:grid-cols-4">
        <Card>
          <CardHeader className="flex flex-row items-center justify-between pb-2">
            <CardTitle className="text-sm font-medium">{t("spam.total")}</CardTitle>
            <ShieldAlert className="h-4 w-4 text-muted-foreground" />
          </CardHeader>
          <CardContent>
            <div className="text-2xl font-bold">{total}</div>
            <p className="text-xs text-muted-foreground">{t("spam.totalDesc")}</p>
          </CardContent>
        </Card>
        <Card>
          <CardHeader className="flex flex-row items-center justify-between pb-2">
            <CardTitle className="text-sm font-medium">{t("spam.rejected")}</CardTitle>
            <XCircle className="h-4 w-4 text-red-500" />
          </CardHeader>
          <CardContent>
            <div className="text-2xl font-bold">
              {entries?.filter((e) => e.verdict === "reject").length ?? "—"}
            </div>
            <p className="text-xs text-muted-foreground">{t("spam.rejectedDesc")}</p>
          </CardContent>
        </Card>
        <Card>
          <CardHeader className="flex flex-row items-center justify-between pb-2">
            <CardTitle className="text-sm font-medium">{t("spam.quarantined")}</CardTitle>
            <AlertCircle className="h-4 w-4 text-amber-500" />
          </CardHeader>
          <CardContent>
            <div className="text-2xl font-bold">
              {entries?.filter((e) => e.verdict === "quarantine").length ?? "—"}
            </div>
            <p className="text-xs text-muted-foreground">{t("spam.quarantinedDesc")}</p>
          </CardContent>
        </Card>
        <Card>
          <CardHeader className="flex flex-row items-center justify-between pb-2">
            <CardTitle className="text-sm font-medium">{t("spam.junk")}</CardTitle>
            <AlertCircle className="h-4 w-4 text-muted-foreground" />
          </CardHeader>
          <CardContent>
            <div className="text-2xl font-bold">
              {entries?.filter((e) => e.verdict === "junk").length ?? "—"}
            </div>
            <p className="text-xs text-muted-foreground">{t("spam.junkDesc")}</p>
          </CardContent>
        </Card>
      </div>

      {/* History table */}
      <Card>
        <CardHeader>
          <CardTitle>{t("spam.history")}</CardTitle>
          <CardDescription>{t("spam.historyDescription")}</CardDescription>
        </CardHeader>
        <CardContent>
          {loading && !entries ? (
            <div className="space-y-3">
              {[1, 2, 3, 4, 5].map((i) => (
                <Skeleton key={i} className="h-12 w-full" />
              ))}
            </div>
          ) : entries?.length === 0 ? (
            <div className="text-center py-12">
              <ShieldAlert className="h-12 w-12 mx-auto text-muted-foreground mb-4" />
              <h3 className="text-lg font-medium">{t("spam.noEvents")}</h3>
              <p className="text-muted-foreground mt-1">{t("spam.noEventsDesc")}</p>
            </div>
          ) : (
            <>
              <div className="overflow-x-auto">
                <table className="w-full text-sm">
                  <thead>
                    <tr className="border-b">
                      <th className="text-left py-2 pr-4 font-medium">{t("spam.colTime")}</th>
                      <th className="text-left py-2 pr-4 font-medium">{t("spam.colFrom")}</th>
                      <th className="text-left py-2 pr-4 font-medium">{t("spam.colTo")}</th>
                      <th className="text-left py-2 pr-4 font-medium">{t("spam.colSubject")}</th>
                      <th className="text-left py-2 pr-4 font-medium">{t("spam.colScore")}</th>
                      <th className="text-left py-2 pr-4 font-medium">{t("spam.colVerdict")}</th>
                      <th className="text-left py-2 font-medium">{t("spam.colReasons")}</th>
                    </tr>
                  </thead>
                  <tbody>
                    {entries?.map((entry) => (
                      <tr key={entry.id} className="border-b last:border-0 hover:bg-muted/50">
                        <td className="py-2 pr-4 text-muted-foreground">
                          {entry.timestamp
                            ? new Date(entry.timestamp).toLocaleString()
                            : "—"}
                        </td>
                        <td className="py-2 pr-4">
                          {entry.mail_from || entry.from_header || "—"}
                        </td>
                        <td className="py-2 pr-4">{entry.rcpt_to || "—"}</td>
                        <td className="py-2 pr-4 max-w-xs truncate" title={entry.subject}>
                          {entry.subject || "—"}
                        </td>
                        <td className="py-2 pr-4">
                          <span
                            className={cn(
                              "font-mono",
                              entry.score >= 8 && "text-red-600 font-bold",
                              entry.score >= 4 && entry.score < 8 && "text-amber-600",
                            )}
                          >
                            {entry.score.toFixed(1)}
                          </span>
                        </td>
                        <td className="py-2 pr-4">
                          <VerdictBadge verdict={entry.verdict} />
                        </td>
                        <td className="py-2 text-muted-foreground text-xs max-w-xs truncate">
                          {entry.reasons?.join(", ") || "—"}
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>

              {/* Pagination */}
              {totalPages > 1 && (
                <div className="flex items-center justify-between mt-4">
                  <p className="text-sm text-muted-foreground">
                    {t("spam.showing", {
                      from: String((currentPage - 1) * ITEMS_PER_PAGE + 1),
                      to: String(Math.min(currentPage * ITEMS_PER_PAGE, total)),
                      total: String(total),
                    })}
                  </p>
                  <div className="flex items-center gap-2">
                    <Button
                      variant="outline"
                      size="icon"
                      onClick={() => handlePageChange(currentPage - 1)}
                      disabled={currentPage === 1}
                    >
                      <ChevronLeft className="h-4 w-4" />
                    </Button>
                    <span className="text-sm">
                      {currentPage} / {totalPages}
                    </span>
                    <Button
                      variant="outline"
                      size="icon"
                      onClick={() => handlePageChange(currentPage + 1)}
                      disabled={currentPage >= totalPages}
                    >
                      <ChevronRight className="h-4 w-4" />
                    </Button>
                  </div>
                </div>
              )}
            </>
          )}
        </CardContent>
      </Card>
    </div>
  );
}
