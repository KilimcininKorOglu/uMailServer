import { useCallback, useEffect, useState } from "react";
import { Smartphone, RefreshCw, AlertTriangle, Loader2, Trash2, ShieldOff } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Card, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Skeleton } from "@/components/ui/skeleton";
import { Alert, AlertDescription } from "@/components/ui/alert";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { useEASDevices } from "@/hooks/useApi";
import { useI18n } from "@/hooks/useI18n";
import type { EASDevice } from "@/types";

type TranslateFn = (key: string, params?: Record<string, string>) => string;

interface EASDevicesDialogProps {
  email: string | null;
  open: boolean;
  onOpenChange: (open: boolean) => void;
}

// EASDevicesDialog is the per-account Exchange ActiveSync device admin
// surface. It is reached from the AccountCard dropdown ("Manage devices")
// and lists every EAS partnership the account holds, with two destructive
// actions per row: remote-wipe (sets a flag the device honors on its next
// contact) and remove (drops the partnership outright, forcing a fresh
// Provision on the device's next contact). Both are audit-logged server-side.
export function EASDevicesDialog({ email, open, onOpenChange }: EASDevicesDialogProps) {
  const { t } = useI18n();
  const { devices, loading, error, fetchDevices, wipeDevice, removeDevice } =
    useEASDevices(email);

  // Pending action state — a single open confirm dialog at a time, so a fast
  // double-click cannot race two destructive actions against the same row.
  const [pending, setPending] = useState<EASDevice | null>(null);
  const [action, setAction] = useState<"wipe" | "remove" | null>(null);
  const [actionError, setActionError] = useState<string | null>(null);
  const [actionRunning, setActionRunning] = useState(false);

  useEffect(() => {
    // Fetch on every open with a non-null email. Closing the dialog leaves
    // the cache in place so re-opening the same account is instant; opening
    // a different account re-fetches because the email prop changed.
    if (open && email) {
      fetchDevices().catch(() => {
        /* error surfaced via hook state */
      });
    }
  }, [open, email, fetchDevices]);

  const refresh = useCallback(() => {
    fetchDevices().catch(() => {
      /* error surfaced via hook state */
    });
  }, [fetchDevices]);

  const onConfirm = useCallback(async () => {
    if (!pending || !action) return;
    setActionRunning(true);
    setActionError(null);
    try {
      if (action === "wipe") {
        await wipeDevice(pending.device_id);
      } else {
        await removeDevice(pending.device_id);
      }
      setPending(null);
      setAction(null);
    } catch (err) {
      const apiErr = err as { message?: string };
      setActionError(apiErr.message ?? t("accounts.devices.actionFailed"));
    } finally {
      setActionRunning(false);
    }
  }, [pending, action, wipeDevice, removeDevice, t]);

  return (
    <>
      <Dialog open={open} onOpenChange={onOpenChange}>
        <DialogContent className="sm:max-w-3xl">
          <DialogHeader>
            <DialogTitle className="flex items-center gap-2">
              <Smartphone className="h-5 w-5" />
              {t("accounts.devices.title")}
            </DialogTitle>
            <DialogDescription>
              {t("accounts.devices.subtitle", { email: email ?? "" })}
            </DialogDescription>
          </DialogHeader>

          <div className="flex items-center justify-between gap-2">
            <p className="text-sm text-muted-foreground">
              {t("accounts.devices.count", { count: String(devices.length) })}
            </p>
            <Button
              variant="ghost"
              size="sm"
              onClick={refresh}
              disabled={loading}
              data-testid="eas-devices-refresh"
            >
              {loading ? (
                <Loader2 className="mr-2 h-4 w-4 animate-spin" />
              ) : (
                <RefreshCw className="mr-2 h-4 w-4" />
              )}
              {t("common.refresh")}
            </Button>
          </div>

          {error && (
            <Alert variant="destructive">
              <AlertTriangle className="h-4 w-4" />
              <AlertDescription>{error.message}</AlertDescription>
            </Alert>
          )}

          {loading && devices.length === 0 ? (
            <div className="space-y-2">
              <Skeleton className="h-10 w-full" />
              <Skeleton className="h-10 w-full" />
              <Skeleton className="h-10 w-full" />
            </div>
          ) : devices.length === 0 ? (
            <Card>
              <CardHeader>
                <CardTitle className="text-base">
                  {t("accounts.devices.empty")}
                </CardTitle>
                <CardDescription>
                  {t("accounts.devices.emptyHint")}
                </CardDescription>
              </CardHeader>
            </Card>
          ) : (
            <div className="border rounded-md">
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>{t("accounts.devices.device")}</TableHead>
                    <TableHead>{t("accounts.devices.os")}</TableHead>
                    <TableHead>{t("accounts.devices.lastSync")}</TableHead>
                    <TableHead className="text-right">
                      {t("common.actions")}
                    </TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {devices.map((d) => (
                    <TableRow key={d.device_id}>
                      <TableCell>
                        <div className="flex flex-col gap-1">
                          <div className="flex items-center gap-2">
                            <span className="font-medium">
                              {d.friendly_name || d.model || d.device_type || d.device_id}
                            </span>
                            {d.wipe_requested && (
                              <Badge variant="destructive" className="text-xs">
                                {t("accounts.devices.wipeRequested")}
                              </Badge>
                            )}
                          </div>
                          <span className="text-xs text-muted-foreground">
                            {deviceSubtitle(d, t)}
                          </span>
                        </div>
                      </TableCell>
                      <TableCell className="text-sm">{d.os || "—"}</TableCell>
                      <TableCell className="text-sm">
                        {formatTime(d.last_sync, t)}
                      </TableCell>
                      <TableCell className="text-right">
                        <div className="flex items-center justify-end gap-1">
                          <Button
                            variant="outline"
                            size="sm"
                            disabled={d.wipe_requested}
                            onClick={() => {
                              setPending(d);
                              setAction("wipe");
                              setActionError(null);
                            }}
                            data-testid="eas-device-wipe"
                          >
                            <ShieldOff className="mr-1 h-3.5 w-3.5" />
                            {t("accounts.devices.wipe")}
                          </Button>
                          <Button
                            variant="ghost"
                            size="sm"
                            className="text-red-600 hover:text-red-600"
                            onClick={() => {
                              setPending(d);
                              setAction("remove");
                              setActionError(null);
                            }}
                            data-testid="eas-device-remove"
                          >
                            <Trash2 className="mr-1 h-3.5 w-3.5" />
                            {t("common.delete")}
                          </Button>
                        </div>
                      </TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            </div>
          )}

          <DialogFooter>
            <Button variant="outline" onClick={() => onOpenChange(false)}>
              {t("common.close")}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* Confirm a destructive action. The dialog body explains what the
          action does and that an audit entry is written; the button runs the
          action and disables itself while in flight. */}
      <Dialog
        open={pending !== null && action !== null}
        onOpenChange={(next) => {
          if (!next && !actionRunning) {
            setPending(null);
            setAction(null);
            setActionError(null);
          }
        }}
      >
        <DialogContent className="sm:max-w-md">
          <DialogHeader>
            <DialogTitle>
              {action === "wipe"
                ? t("accounts.devices.wipeConfirmTitle")
                : t("accounts.devices.removeConfirmTitle")}
            </DialogTitle>
            <DialogDescription>
              {action === "wipe"
                ? t("accounts.devices.wipeConfirmDescription", {
                    name: pending?.friendly_name || pending?.device_id || "",
                  })
                : t("accounts.devices.removeConfirmDescription", {
                    name: pending?.friendly_name || pending?.device_id || "",
                  })}
            </DialogDescription>
          </DialogHeader>
          {actionError && (
            <Alert variant="destructive">
              <AlertTriangle className="h-4 w-4" />
              <AlertDescription>{actionError}</AlertDescription>
            </Alert>
          )}
          <DialogFooter>
            <Button
              variant="outline"
              onClick={() => {
                setPending(null);
                setAction(null);
                setActionError(null);
              }}
              disabled={actionRunning}
            >
              {t("common.cancel")}
            </Button>
            <Button
              variant={action === "wipe" ? "default" : "destructive"}
              onClick={onConfirm}
              disabled={actionRunning}
              data-testid="eas-device-confirm"
            >
              {actionRunning && (
                <Loader2 className="mr-2 h-4 w-4 animate-spin" />
              )}
              {action === "wipe"
                ? t("accounts.devices.wipe")
                : t("common.delete")}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  );
}

// deviceSubtitle returns a one-line summary of the secondary identifying
// fields (model + type + protocol). The friendly_name is rendered as the
// primary line, so these are the fallback stack: model, type, device_id.
function deviceSubtitle(d: EASDevice, _t: TranslateFn): string {
  const parts: string[] = [];
  if (d.model) parts.push(d.model);
  if (d.device_type && d.device_type !== d.model) parts.push(d.device_type);
  if (d.protocol_version) parts.push(`EAS ${d.protocol_version}`);
  if (d.user_agent) parts.push(d.user_agent);
  return parts.join(" • ") || d.device_id;
}

// formatTime returns the localized last-sync string, or the i18n "never"
// key for absent / zero-value timestamps. RFC3339 strings parse with the
// built-in Date; non-conforming inputs fall through to the raw value so an
// operator can see what the server actually sent.
function formatTime(iso: string, t: TranslateFn): string {
  if (!iso) return t("common.never");
  const at = new Date(iso);
  if (Number.isNaN(at.getTime())) return iso;
  return at.toLocaleString();
}
