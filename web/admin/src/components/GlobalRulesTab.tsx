import { useEffect, useState } from "react";
import { Globe, Plus, Trash2, X } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { Badge } from "@/components/ui/badge";
import { Skeleton } from "@/components/ui/skeleton";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { MoreHorizontal } from "lucide-react";
import { cn } from "@/lib/utils";
import { useGlobalRules } from "@/hooks/useApi";
import { useI18n } from "@/hooks/useI18n";
import type { GlobalRule, GlobalRuleCondition, GlobalRuleAction } from "@/types";

type TFunc = (key: string, params?: Record<string, string>) => string;

const conditionFields = (t: TFunc): { value: GlobalRuleCondition["field"]; label: string }[] => [
  { value: "from", label: t("globalRules.field.from") },
  { value: "to", label: t("globalRules.field.to") },
  { value: "subject", label: t("globalRules.field.subject") },
  { value: "body", label: t("globalRules.field.body") },
  { value: "header", label: t("globalRules.field.header") },
  { value: "size", label: t("globalRules.field.size") },
  { value: "flag", label: t("globalRules.field.flag") },
  { value: "address", label: t("globalRules.field.address") },
];

const conditionOperators = (t: TFunc): { value: GlobalRuleCondition["operator"]; label: string }[] => [
  { value: "contains", label: t("globalRules.operator.contains") },
  { value: "equals", label: t("globalRules.operator.equals") },
  { value: "startsWith", label: t("globalRules.operator.startsWith") },
  { value: "endsWith", label: t("globalRules.operator.endsWith") },
  { value: "matches", label: t("globalRules.operator.matches") },
];

const actionTypes = (t: TFunc): { value: GlobalRuleAction["type"]; label: string }[] => [
  { value: "moveToFolder", label: t("globalRules.action.moveToFolder") },
  { value: "copyToFolder", label: t("globalRules.action.copyToFolder") },
  { value: "markRead", label: t("globalRules.action.markRead") },
  { value: "markImportant", label: t("globalRules.action.markImportant") },
  { value: "flag", label: t("globalRules.action.flag") },
  { value: "forward", label: t("globalRules.action.forward") },
  { value: "forwardAsAttachment", label: t("globalRules.action.forwardAsAttachment") },
  { value: "redirect", label: t("globalRules.action.redirect") },
  { value: "reject", label: t("globalRules.action.reject") },
  { value: "addHeader", label: t("globalRules.action.addHeader") },
  { value: "deleteHeader", label: t("globalRules.action.deleteHeader") },
  { value: "delete", label: t("globalRules.action.delete") },
  { value: "stop", label: t("globalRules.action.stop") },
];

// Action types whose forward/redirect address lives in forwardTo.
const FORWARD_TYPES = new Set<GlobalRuleAction["type"]>(["forward", "forwardAsAttachment", "redirect"]);

function newCondition(): GlobalRuleCondition {
  return { field: "from", operator: "contains", value: "" };
}

function newAction(): GlobalRuleAction {
  return { type: "moveToFolder", target: "" };
}

function summarizeConditions(conds: GlobalRuleCondition[]): string {
  if (conds.length === 0) return "—";
  return conds.map((c) => `${c.field} ${c.operator} "${c.value}"`).join("; ");
}

function summarizeActions(actions: GlobalRuleAction[]): string {
  if (actions.length === 0) return "—";
  return actions
    .map((a) => {
      if (a.target) return `${a.type} → ${a.target}`;
      if (a.forwardTo) return `${a.type} → ${a.forwardTo}`;
      return a.type;
    })
    .join("; ");
}

export function GlobalRulesTab() {
  const { t } = useI18n();
  const { rules, loading, fetchGlobalRules, createGlobalRule, updateGlobalRule, deleteGlobalRule } = useGlobalRules();

  useEffect(() => {
    fetchGlobalRules().catch(() => {});
  }, [fetchGlobalRules]);

  const [dialogOpen, setDialogOpen] = useState(false);
  const [saving, setSaving] = useState(false);
  const [formError, setFormError] = useState<string | null>(null);

  const [name, setName] = useState("");
  const [matchAll, setMatchAll] = useState(false);
  const [conditions, setConditions] = useState<GlobalRuleCondition[]>([newCondition()]);
  const [actions, setActions] = useState<GlobalRuleAction[]>([newAction()]);

  const resetForm = () => {
    setName("");
    setMatchAll(false);
    setConditions([newCondition()]);
    setActions([newAction()]);
    setFormError(null);
  };

  const openCreate = () => {
    resetForm();
    setDialogOpen(true);
  };

  const validate = (): string | null => {
    if (!name.trim()) return t("globalRules.validation.nameRequired");
    if (conditions.length === 0) return t("globalRules.validation.conditionRequired");
    for (const c of conditions) {
      if (!c.value.trim()) return t("globalRules.validation.conditionValue");
      if (c.field === "header" && !c.headerName?.trim()) return t("globalRules.validation.headerName");
    }
    if (actions.length === 0) return t("globalRules.validation.actionRequired");
    for (const a of actions) {
      if ((a.type === "moveToFolder" || a.type === "copyToFolder") && !a.target?.trim()) {
        return t("globalRules.validation.targetFolder");
      }
      if (FORWARD_TYPES.has(a.type) && !a.forwardTo?.trim()) {
        return t("globalRules.validation.forwardTo");
      }
    }
    return null;
  };

  const handleSave = async () => {
    const err = validate();
    if (err) {
      setFormError(err);
      return;
    }
    setSaving(true);
    setFormError(null);
    try {
      await createGlobalRule({ name: name.trim(), matchAll, conditions, actions });
      setDialogOpen(false);
      resetForm();
    } catch (e) {
      setFormError((e as { message?: string }).message || t("globalRules.failedToSave"));
    } finally {
      setSaving(false);
    }
  };

  const handleToggle = async (rule: GlobalRule) => {
    try {
      await updateGlobalRule(rule.id, { enabled: !rule.enabled });
    } catch (e) {
      setFormError((e as { message?: string }).message || t("globalRules.failedToUpdate"));
    }
  };

  const handleDelete = async (id: string) => {
    try {
      await deleteGlobalRule(id);
    } catch (e) {
      setFormError((e as { message?: string }).message || t("globalRules.failedToDelete"));
    }
  };

  return (
    <Card>
      <CardHeader>
        <div className="flex items-center justify-between">
          <div>
            <CardTitle>{t("globalRules.title")}</CardTitle>
            <CardDescription>{t("globalRules.description")}</CardDescription>
          </div>
          <Button onClick={openCreate}>
            <Plus className="mr-2 h-4 w-4" />
            {t("globalRules.newRule")}
          </Button>
        </div>
      </CardHeader>
      <CardContent>
        {formError && !dialogOpen && (
          <div className="mb-4 text-sm text-red-600">{formError}</div>
        )}
        {loading ? (
          <div className="space-y-4">
            <Skeleton className="h-16 w-full" />
            <Skeleton className="h-16 w-full" />
          </div>
        ) : rules.length === 0 ? (
          <div className="text-center py-8">
            <Globe className="h-12 w-12 mx-auto text-muted-foreground mb-4" />
            <h3 className="text-lg font-medium">{t("globalRules.noRules")}</h3>
            <p className="text-muted-foreground mt-1">{t("globalRules.noRulesHelp")}</p>
          </div>
        ) : (
          <div className="space-y-2">
            {rules.map((rule) => (
              <div
                key={rule.id}
                className="flex items-center justify-between p-4 rounded-lg border hover:bg-muted/50 transition-colors"
              >
                <div className="flex items-center gap-3">
                  <div className={cn("p-2 rounded-lg", rule.enabled ? "bg-emerald-500/10" : "bg-muted")}>
                    <Globe className={cn("h-4 w-4", rule.enabled ? "text-emerald-500" : "text-muted-foreground")} />
                  </div>
                  <div>
                    <div className="font-medium">{rule.name || t("globalRules.unnamedRule")}</div>
                    <div className="text-sm text-muted-foreground">
                      {summarizeConditions(rule.conditions)} &rarr; {summarizeActions(rule.actions)}
                    </div>
                  </div>
                </div>
                <div className="flex items-center gap-2">
                  <Badge variant="secondary">{rule.priority}</Badge>
                  <Switch checked={rule.enabled} onCheckedChange={() => handleToggle(rule)} />
                  <DropdownMenu>
                    {/* @ts-expect-error asChild prop not typed in Base UI but works at runtime */}
                    <DropdownMenuTrigger asChild>
                      <Button variant="ghost" size="icon" className="h-8 w-8">
                        <MoreHorizontal className="h-4 w-4" />
                      </Button>
                    </DropdownMenuTrigger>
                    <DropdownMenuContent align="end">
                      <DropdownMenuItem className="text-red-600" onClick={() => handleDelete(rule.id)}>
                        <Trash2 className="mr-2 h-4 w-4" />
                        {t("common.delete")}
                      </DropdownMenuItem>
                    </DropdownMenuContent>
                  </DropdownMenu>
                </div>
              </div>
            ))}
          </div>
        )}
      </CardContent>

      <Dialog open={dialogOpen} onOpenChange={setDialogOpen}>
        <DialogContent className="sm:max-w-2xl max-h-[85vh] overflow-y-auto">
          <DialogHeader>
            <DialogTitle>{t("globalRules.newRule")}</DialogTitle>
            <DialogDescription>{t("globalRules.dialogDescription")}</DialogDescription>
          </DialogHeader>

          <div className="space-y-4">
            <div className="space-y-2">
              <Label htmlFor="global-rule-name">{t("globalRules.ruleName")}</Label>
              <Input
                id="global-rule-name"
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder={t("globalRules.ruleNamePlaceholder")}
              />
            </div>

            <div className="flex items-center justify-between">
              <div className="space-y-0.5">
                <Label>{t("globalRules.matchAll")}</Label>
                <p className="text-xs text-muted-foreground">{t("globalRules.matchAllHelp")}</p>
              </div>
              <Switch checked={matchAll} onCheckedChange={setMatchAll} />
            </div>

            {/* Conditions */}
            <div className="space-y-2">
              <Label>{t("globalRules.conditions")}</Label>
              {conditions.map((c, i) => (
                <div key={i} className="flex flex-wrap items-center gap-2">
                  <Select
                    value={c.field}
                    onValueChange={(v) =>
                      setConditions((prev) =>
                        prev.map((x, j) => (j === i ? { ...x, field: v as GlobalRuleCondition["field"] } : x)),
                      )
                    }
                  >
                    <SelectTrigger className="w-32">
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      {conditionFields(t).map((o) => (
                        <SelectItem key={o.value} value={o.value}>
                          {o.label}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                  {c.field === "header" && (
                    <Input
                      className="w-32"
                      placeholder={t("globalRules.headerName")}
                      value={c.headerName ?? ""}
                      onChange={(e) =>
                        setConditions((prev) =>
                          prev.map((x, j) => (j === i ? { ...x, headerName: e.target.value } : x)),
                        )
                      }
                    />
                  )}
                  <Select
                    value={c.operator}
                    onValueChange={(v) =>
                      setConditions((prev) =>
                        prev.map((x, j) => (j === i ? { ...x, operator: v as GlobalRuleCondition["operator"] } : x)),
                      )
                    }
                  >
                    <SelectTrigger className="w-32">
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      {conditionOperators(t).map((o) => (
                        <SelectItem key={o.value} value={o.value}>
                          {o.label}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                  <Input
                    className="flex-1 min-w-[8rem]"
                    placeholder={t("globalRules.value")}
                    value={c.value}
                    onChange={(e) =>
                      setConditions((prev) => prev.map((x, j) => (j === i ? { ...x, value: e.target.value } : x)))
                    }
                  />
                  <Button
                    variant="ghost"
                    size="icon"
                    className="h-8 w-8"
                    disabled={conditions.length === 1}
                    onClick={() => setConditions((prev) => prev.filter((_, j) => j !== i))}
                  >
                    <X className="h-4 w-4" />
                  </Button>
                </div>
              ))}
              <Button variant="outline" size="sm" onClick={() => setConditions((prev) => [...prev, newCondition()])}>
                <Plus className="mr-2 h-4 w-4" />
                {t("globalRules.addCondition")}
              </Button>
            </div>

            {/* Actions */}
            <div className="space-y-2">
              <Label>{t("globalRules.actions")}</Label>
              {actions.map((a, i) => (
                <div key={i} className="flex flex-wrap items-center gap-2">
                  <Select
                    value={a.type}
                    onValueChange={(v) =>
                      setActions((prev) =>
                        prev.map((x, j) => (j === i ? { ...x, type: v as GlobalRuleAction["type"] } : x)),
                      )
                    }
                  >
                    <SelectTrigger className="w-44">
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      {actionTypes(t).map((o) => (
                        <SelectItem key={o.value} value={o.value}>
                          {o.label}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                  {(a.type === "moveToFolder" || a.type === "copyToFolder") && (
                    <Input
                      className="flex-1 min-w-[8rem]"
                      placeholder={t("globalRules.targetFolder")}
                      value={a.target ?? ""}
                      onChange={(e) =>
                        setActions((prev) => prev.map((x, j) => (j === i ? { ...x, target: e.target.value } : x)))
                      }
                    />
                  )}
                  {FORWARD_TYPES.has(a.type) && (
                    <Input
                      className="flex-1 min-w-[8rem]"
                      placeholder={t("globalRules.forwardAddress")}
                      value={a.forwardTo ?? ""}
                      onChange={(e) =>
                        setActions((prev) => prev.map((x, j) => (j === i ? { ...x, forwardTo: e.target.value } : x)))
                      }
                    />
                  )}
                  {a.type === "reject" && (
                    <Input
                      className="flex-1 min-w-[8rem]"
                      placeholder={t("globalRules.rejectMessage")}
                      value={a.message ?? ""}
                      onChange={(e) =>
                        setActions((prev) => prev.map((x, j) => (j === i ? { ...x, message: e.target.value } : x)))
                      }
                    />
                  )}
                  {(a.type === "addHeader" || a.type === "deleteHeader") && (
                    <Input
                      className="w-32"
                      placeholder={t("globalRules.headerName")}
                      value={a.headerName ?? ""}
                      onChange={(e) =>
                        setActions((prev) => prev.map((x, j) => (j === i ? { ...x, headerName: e.target.value } : x)))
                      }
                    />
                  )}
                  {a.type === "addHeader" && (
                    <Input
                      className="w-32"
                      placeholder={t("globalRules.headerValue")}
                      value={a.headerValue ?? ""}
                      onChange={(e) =>
                        setActions((prev) => prev.map((x, j) => (j === i ? { ...x, headerValue: e.target.value } : x)))
                      }
                    />
                  )}
                  <Button
                    variant="ghost"
                    size="icon"
                    className="h-8 w-8"
                    disabled={actions.length === 1}
                    onClick={() => setActions((prev) => prev.filter((_, j) => j !== i))}
                  >
                    <X className="h-4 w-4" />
                  </Button>
                </div>
              ))}
              <Button variant="outline" size="sm" onClick={() => setActions((prev) => [...prev, newAction()])}>
                <Plus className="mr-2 h-4 w-4" />
                {t("globalRules.addAction")}
              </Button>
            </div>

            {formError && <div className="text-sm text-red-600">{formError}</div>}
          </div>

          <DialogFooter>
            <Button variant="outline" onClick={() => setDialogOpen(false)} disabled={saving}>
              {t("common.cancel")}
            </Button>
            <Button onClick={handleSave} disabled={saving}>
              {t("common.save")}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </Card>
  );
}
