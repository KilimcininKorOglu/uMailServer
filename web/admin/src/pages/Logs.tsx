import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Badge } from "@/components/ui/badge";
import { Skeleton } from "@/components/ui/skeleton";
import { Alert, AlertDescription } from "@/components/ui/alert";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { RefreshCw, ScrollText, Search } from "lucide-react";
import { useLogs, useLogsTail, type AuditFilterQuery } from "@/hooks/useApi";
import { useI18n } from "@/hooks/useI18n";
import type { AuditEvent } from "@/types";

// EVENT_TYPE_OPTIONS is the closed set of audit event types the
// backend can emit (mirrors internal/audit/audit.go EventType). The
// "any" sentinel value is used in the Select as a non-empty marker
// that the filter must drop before building the query string.
const EVENT_TYPE_OPTIONS = [
  "login_success",
  "login_failure",
  "logout",
  "account_create",
  "account_update",
  "account_delete",
  "totp_enable",
  "totp_disable",
  "password_change",
  "tenant_create",
  "tenant_update",
  "tenant_suspend",
  "tenant_activate",
  "tenant_delete",
  "tenant_export",
  "eas_remote_wipe",
] as const;

// SERVICE_OPTIONS mirrors the audit writer's Service values. "any" is
// a UI sentinel that the filter builder drops.
const SERVICE_OPTIONS = ["api", "smtp", "imap", "pop3"] as const;

// SUCCESS_OPTIONS is the tri-state (any / yes / no) the operator sees.
// The Select uses non-empty sentinels that buildAuditQueryString maps
// to boolean or undefined.
const SUCCESS_OPTIONS = ["any", "yes", "no"] as const;
type SuccessOption = (typeof SUCCESS_OPTIONS)[number];

// DEFAULT_PAGE_LIMIT is the page size for the paged view. Matches
// auditreader.DefaultLimit so the operator's first page is the
// server's default.
const DEFAULT_PAGE_LIMIT = 100;
const TAIL_LIMIT = 50;

// AUDIT_ANY is a Select sentinel that the filter builder treats as
// "no filter applied". The actual value is otherwise unused.
const AUDIT_ANY = "__any__";

type FilterState = {
  type: string;
  user: string;
  ip: string;
  service: string;
  success: SuccessOption;
  from: string;
  to: string;
};

const EMPTY_FILTER: FilterState = {
  type: AUDIT_ANY,
  user: "",
  ip: "",
  service: AUDIT_ANY,
  success: "any",
  from: "",
  to: "",
};

// successToBool maps the tri-state Select option to a boolean for the
// query string (or undefined when the operator picks "any"). The
// backend reads this as a tri-state pointer.
function successToBool(opt: SuccessOption): boolean | undefined {
  if (opt === "yes") return true;
  if (opt === "no") return false;
  return undefined;
}

// auditQueryFromState renders the in-memory filter state as the
// wire-shape the backend expects. AUDIT_ANY and empty fields drop
// out; success is passed only when explicitly set.
function auditQueryFromState(f: FilterState): AuditFilterQuery {
  return {
    type: f.type === AUDIT_ANY ? undefined : f.type,
    user: f.user.trim() || undefined,
    ip: f.ip.trim() || undefined,
    service: f.service === AUDIT_ANY ? undefined : f.service,
    success: successToBool(f.success),
    from: f.from || undefined,
    to: f.to || undefined,
  };
}

// detailsToString flattens a details map into a short, scannable
// "k=v, k=v" rendering. Long values are truncated so a single row
// stays readable. Empty details return "" so the table cell renders
// nothing instead of "—".
function detailsToString(d: Record<string, string> | undefined): string {
  if (!d) return "";
  const parts: string[] = [];
  for (const k of Object.keys(d)) {
    const v = d[k];
    const display = v.length > 40 ? `${v.slice(0, 40)}…` : v;
    parts.push(`${k}=${display}`);
    if (parts.length >= 2) break;
  }
  return parts.join(", ");
}

// Logs is the admin log viewer page. It composes a filter bar, a
// chronological event table, "load more" cursor pagination, and a
// "refresh tail" button that swaps the table for the trailing N
// events without losing the filter context.
export default function Logs() {
  const { t } = useI18n();
  const { error, fetchLogs, reset: resetLogs } = useLogs();
  const { fetchTail } = useLogsTail();

  const [filter, setFilter] = useState<FilterState>(EMPTY_FILTER);
  const [activeFilter, setActiveFilter] = useState<FilterState>(EMPTY_FILTER);
  const [events, setEvents] = useState<AuditEvent[]>([]);
  const [cursor, setCursor] = useState<string>("");
  const [hasMore, setHasMore] = useState(false);
  const [applying, setApplying] = useState(false);
  const [loadingMore, setLoadingMore] = useState(false);
  const [tailing, setTailing] = useState(false);
  const [tailError, setTailError] = useState<string | null>(null);

  // Refs let the load-more / refresh-tail callbacks see the latest
  // filter without re-binding the effect that fired the first fetch.
  const filterRef = useRef(activeFilter);
  filterRef.current = activeFilter;

  // First-page fetch: triggered when activeFilter changes (including
  // the initial mount). We reset the cursor and replace events; the
  // load-more button is the only path that appends.
  useEffect(() => {
    let cancelled = false;
    setApplying(true);
    setEvents([]);
    setCursor("");
    setHasMore(false);
    setTailError(null);
    resetLogs();
    fetchLogs(auditQueryFromState(activeFilter), DEFAULT_PAGE_LIMIT, "")
      .then((p) => {
        if (cancelled) return;
        setEvents(p.events);
        setCursor(p.next);
        setHasMore(p.has_more);
      })
      .catch(() => {
        // error is already on the hook; UI shows the alert.
      })
      .finally(() => {
        if (!cancelled) setApplying(false);
      });
    return () => {
      cancelled = true;
    };
    // We intentionally key on activeFilter (the snapshot the user
    // applied) rather than the live draft — typing in the user input
    // would otherwise re-fire the request on every keystroke.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [activeFilter]);

  const handleApply = useCallback(() => {
    setActiveFilter(filter);
  }, [filter]);

  const handleReset = useCallback(() => {
    setFilter(EMPTY_FILTER);
    setActiveFilter(EMPTY_FILTER);
  }, []);

  const handleLoadMore = useCallback(async () => {
    if (!hasMore || loadingMore) return;
    setLoadingMore(true);
    try {
      const p = await fetchLogs(
        auditQueryFromState(filterRef.current),
        DEFAULT_PAGE_LIMIT,
        cursor
      );
      setEvents((prev) => [...prev, ...p.events]);
      setCursor(p.next);
      setHasMore(p.has_more);
    } catch {
      // error is on the hook
    } finally {
      setLoadingMore(false);
    }
  }, [hasMore, loadingMore, cursor, fetchLogs]);

  const handleRefreshTail = useCallback(async () => {
    setTailing(true);
    setTailError(null);
    try {
      const p = await fetchTail(TAIL_LIMIT);
      setEvents(p.events);
      setCursor("");
      setHasMore(false);
    } catch (err) {
      setTailError((err as { message?: string }).message || t("logs.errorTitle"));
    } finally {
      setTailing(false);
    }
    // fetchTail identity is stable; intentionally only depend on
    // t() to avoid re-creating on every filter keystroke.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [t]);

  // Empty / error / loading branches render below. We surface the
  // hook's `error` (covers paged fetch failures) AND a tail-only
  // error string (so a failed tail-refresh does not erase the
  // existing paged view).
  const showSkeleton = applying && events.length === 0;
  const showEmpty = !applying && events.length === 0 && !error;
  const filterIsActive = useMemo(
    () => JSON.stringify(activeFilter) !== JSON.stringify(EMPTY_FILTER),
    [activeFilter]
  );

  return (
    <div className="space-y-6">
      <div className="flex items-center gap-2">
        <ScrollText className="h-7 w-7 text-muted-foreground" />
        <div>
          <h1 className="text-3xl font-bold tracking-tight">{t("logs.title")}</h1>
          <p className="text-muted-foreground mt-1">{t("logs.description")}</p>
        </div>
      </div>

      <Card>
        <CardHeader>
          <CardTitle>{t("logs.filters.title")}</CardTitle>
          <CardDescription>{t("logs.filters.description")}</CardDescription>
        </CardHeader>
        <CardContent>
          <div className="grid gap-4 md:grid-cols-3 lg:grid-cols-6">
            <div className="space-y-2">
              <Label htmlFor="logs-filter-type">{t("logs.filters.type")}</Label>
              <Select
                value={filter.type}
                onValueChange={(v) =>
                  setFilter((p) => ({ ...p, type: v ?? AUDIT_ANY }))
                }
              >
                <SelectTrigger id="logs-filter-type" data-testid="logs-filter-type">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value={AUDIT_ANY}>{t("logs.filters.any")}</SelectItem>
                  {EVENT_TYPE_OPTIONS.map((et) => (
                    <SelectItem key={et} value={et}>
                      {t(`logs.eventTypes.${et}`)}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>

            <div className="space-y-2">
              <Label htmlFor="logs-filter-service">{t("logs.filters.service")}</Label>
              <Select
                value={filter.service}
                onValueChange={(v) =>
                  setFilter((p) => ({ ...p, service: v ?? AUDIT_ANY }))
                }
              >
                <SelectTrigger id="logs-filter-service" data-testid="logs-filter-service">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value={AUDIT_ANY}>{t("logs.filters.any")}</SelectItem>
                  {SERVICE_OPTIONS.map((s) => (
                    <SelectItem key={s} value={s}>
                      {s}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>

            <div className="space-y-2">
              <Label htmlFor="logs-filter-success">{t("logs.filters.success")}</Label>
              <Select
                value={filter.success}
                onValueChange={(v) =>
                  setFilter((p) => ({ ...p, success: v as SuccessOption }))
                }
              >
                <SelectTrigger id="logs-filter-success" data-testid="logs-filter-success">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="any">{t("logs.filters.any")}</SelectItem>
                  <SelectItem value="yes">{t("logs.filters.successYes")}</SelectItem>
                  <SelectItem value="no">{t("logs.filters.successNo")}</SelectItem>
                </SelectContent>
              </Select>
            </div>

            <div className="space-y-2">
              <Label htmlFor="logs-filter-user">{t("logs.filters.user")}</Label>
              <Input
                id="logs-filter-user"
                data-testid="logs-filter-user"
                value={filter.user}
                onChange={(e) => setFilter((p) => ({ ...p, user: e.target.value }))}
                placeholder={t("logs.filters.userPlaceholder")}
              />
            </div>

            <div className="space-y-2">
              <Label htmlFor="logs-filter-ip">{t("logs.filters.ip")}</Label>
              <Input
                id="logs-filter-ip"
                data-testid="logs-filter-ip"
                value={filter.ip}
                onChange={(e) => setFilter((p) => ({ ...p, ip: e.target.value }))}
                placeholder={t("logs.filters.ipPlaceholder")}
              />
            </div>

            <div className="space-y-2">
              <Label htmlFor="logs-filter-from">{t("logs.filters.from")}</Label>
              <Input
                id="logs-filter-from"
                data-testid="logs-filter-from"
                type="datetime-local"
                value={filter.from}
                onChange={(e) => setFilter((p) => ({ ...p, from: e.target.value }))}
              />
            </div>

            <div className="space-y-2">
              <Label htmlFor="logs-filter-to">{t("logs.filters.to")}</Label>
              <Input
                id="logs-filter-to"
                data-testid="logs-filter-to"
                type="datetime-local"
                value={filter.to}
                onChange={(e) => setFilter((p) => ({ ...p, to: e.target.value }))}
              />
            </div>
          </div>

          <div className="flex flex-wrap items-center gap-2 mt-4">
            <Button
              onClick={handleApply}
              disabled={applying}
              data-testid="logs-apply"
            >
              <Search className="h-4 w-4 mr-2" />
              {t("logs.filters.apply")}
            </Button>
            <Button
              variant="outline"
              onClick={handleReset}
              disabled={applying && !filterIsActive}
              data-testid="logs-reset"
            >
              {t("logs.filters.reset")}
            </Button>
            <Button
              variant="secondary"
              onClick={handleRefreshTail}
              disabled={tailing}
              data-testid="logs-refresh-tail"
            >
              <RefreshCw className={`h-4 w-4 mr-2 ${tailing ? "animate-spin" : ""}`} />
              {t("logs.refreshTail")}
            </Button>
          </div>
        </CardContent>
      </Card>

      {error && (
        <Alert variant="destructive" data-testid="logs-error">
          <AlertDescription>
            {error.status === 503
              ? t("logs.auditDisabled")
              : error.message || t("logs.errorTitle")}
          </AlertDescription>
        </Alert>
      )}
      {tailError && !error && (
        <Alert variant="destructive" data-testid="logs-tail-error">
          <AlertDescription>{tailError}</AlertDescription>
        </Alert>
      )}

      <Card>
        <CardHeader>
          <CardTitle>{t("logs.events")}</CardTitle>
          <CardDescription>
            {t("logs.count", { count: String(events.length) })}
          </CardDescription>
        </CardHeader>
        <CardContent>
          {showSkeleton ? (
            <div className="space-y-2">
              {Array.from({ length: 6 }).map((_, i) => (
                <Skeleton key={i} className="h-10 w-full" />
              ))}
            </div>
          ) : showEmpty ? (
            <div className="text-center py-12 text-muted-foreground">
              <p className="font-medium">{t("logs.emptyTitle")}</p>
              <p className="text-sm mt-1">{t("logs.emptyHint")}</p>
            </div>
          ) : (
            <>
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>{t("logs.columns.timestamp")}</TableHead>
                    <TableHead>{t("logs.columns.type")}</TableHead>
                    <TableHead>{t("logs.columns.user")}</TableHead>
                    <TableHead>{t("logs.columns.ip")}</TableHead>
                    <TableHead>{t("logs.columns.service")}</TableHead>
                    <TableHead>{t("logs.columns.success")}</TableHead>
                    <TableHead>{t("logs.columns.tenant")}</TableHead>
                    <TableHead>{t("logs.columns.details")}</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {events.map((ev, idx) => (
                    <TableRow key={`${ev.timestamp}-${idx}`} data-testid={`logs-row-${idx}`}>
                      <TableCell className="font-mono text-xs whitespace-nowrap">
                        {ev.timestamp}
                      </TableCell>
                      <TableCell>
                        <Badge variant="outline">
                          {t(`logs.eventTypes.${ev.type}`)}
                        </Badge>
                      </TableCell>
                      <TableCell className="font-mono text-xs">{ev.user || "—"}</TableCell>
                      <TableCell className="font-mono text-xs">{ev.ip || "—"}</TableCell>
                      <TableCell>
                        <Badge variant="secondary">{ev.service}</Badge>
                      </TableCell>
                      <TableCell>
                        {ev.success ? (
                          <Badge variant="default">{t("logs.filters.successYes")}</Badge>
                        ) : (
                          <Badge variant="destructive">{t("logs.filters.successNo")}</Badge>
                        )}
                      </TableCell>
                      <TableCell className="font-mono text-xs">{ev.tenant || "—"}</TableCell>
                      <TableCell className="text-xs text-muted-foreground">
                        {detailsToString(ev.details)}
                      </TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>

              <div className="flex items-center justify-center gap-2 mt-4">
                {hasMore && (
                  <Button
                    variant="outline"
                    onClick={handleLoadMore}
                    disabled={loadingMore}
                    data-testid="logs-load-more"
                  >
                    {loadingMore ? t("logs.loading") : t("logs.loadMore")}
                  </Button>
                )}
              </div>
            </>
          )}
        </CardContent>
      </Card>
    </div>
  );
}
