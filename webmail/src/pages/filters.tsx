import { useState, useEffect, useCallback } from "react"
import { Filter as FilterIcon, Plus, Pencil, Trash2, X } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Switch } from "@/components/ui/switch"
import { Skeleton } from "@/components/ui/skeleton"
import { Badge } from "@/components/ui/badge"
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogFooter,
  DialogTitle,
} from "@/components/ui/dialog"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { toast } from "sonner"
import api from "@/utils/api"
import type { Filter, FilterCondition, FilterAction, FilterInput } from "@/utils/api"

const CONDITION_FIELDS: { value: FilterCondition["field"]; label: string }[] = [
  { value: "from", label: "From" },
  { value: "to", label: "To" },
  { value: "subject", label: "Subject" },
  { value: "body", label: "Body" },
  { value: "header", label: "Header" },
]

const CONDITION_OPERATORS: { value: FilterCondition["operator"]; label: string }[] = [
  { value: "contains", label: "contains" },
  { value: "equals", label: "equals" },
  { value: "startsWith", label: "starts with" },
  { value: "endsWith", label: "ends with" },
  { value: "matches", label: "matches" },
]

const ACTION_TYPES: { value: FilterAction["type"]; label: string }[] = [
  { value: "move", label: "Move to folder" },
  { value: "copy", label: "Copy to folder" },
  { value: "markRead", label: "Mark as read" },
  { value: "markSpam", label: "Mark as spam" },
  { value: "flag", label: "Flag" },
  { value: "forward", label: "Forward to" },
  { value: "delete", label: "Delete" },
]

function emptyCondition(): FilterCondition {
  return { field: "from", operator: "contains", value: "" }
}

function emptyAction(): FilterAction {
  return { type: "move", target: "" }
}

function emptyDraft(): FilterInput {
  return {
    name: "",
    enabled: true,
    matchAll: true,
    conditions: [emptyCondition()],
    actions: [emptyAction()],
  }
}

export function FiltersPage() {
  const [filters, setFilters] = useState<Filter[]>([])
  const [loading, setLoading] = useState(true)
  const [dialogOpen, setDialogOpen] = useState(false)
  const [editingId, setEditingId] = useState<string | null>(null)
  const [draft, setDraft] = useState<FilterInput>(emptyDraft())
  const [saving, setSaving] = useState(false)

  const loadFilters = useCallback(async () => {
    setLoading(true)
    try {
      const result = await api.getFilters()
      setFilters(result.filters ?? [])
    } catch {
      setFilters([])
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    loadFilters()
  }, [loadFilters])

  const openCreate = () => {
    setEditingId(null)
    setDraft(emptyDraft())
    setDialogOpen(true)
  }

  const openEdit = (filter: Filter) => {
    setEditingId(filter.id)
    setDraft({
      name: filter.name,
      enabled: filter.enabled,
      matchAll: filter.matchAll,
      conditions: filter.conditions.length ? filter.conditions : [emptyCondition()],
      actions: filter.actions.length ? filter.actions : [emptyAction()],
    })
    setDialogOpen(true)
  }

  const updateCondition = (index: number, patch: Partial<FilterCondition>) => {
    setDraft((d) => ({
      ...d,
      conditions: d.conditions.map((c, i) => (i === index ? { ...c, ...patch } : c)),
    }))
  }

  const updateAction = (index: number, patch: Partial<FilterAction>) => {
    setDraft((d) => ({
      ...d,
      actions: d.actions.map((a, i) => (i === index ? { ...a, ...patch } : a)),
    }))
  }

  const validate = (): string | null => {
    if (!draft.name.trim()) return "Filter name is required"
    if (draft.conditions.length === 0) return "At least one condition is required"
    for (const c of draft.conditions) {
      if (!c.value.trim()) return "Each condition needs a value"
      if (c.field === "header" && !c.headerName?.trim()) {
        return "Header conditions need a header name"
      }
    }
    if (draft.actions.length === 0) return "At least one action is required"
    for (const a of draft.actions) {
      if ((a.type === "move" || a.type === "copy") && !a.target?.trim()) {
        return "Move/Copy actions need a target folder"
      }
      if (a.type === "forward" && !a.forwardTo?.trim()) {
        return "Forward actions need a destination address"
      }
    }
    return null
  }

  const handleSave = async () => {
    const error = validate()
    if (error) {
      toast.error(error)
      return
    }
    setSaving(true)
    try {
      if (editingId) {
        await api.updateFilter(editingId, draft)
        toast.success("Filter updated")
      } else {
        await api.createFilter(draft)
        toast.success("Filter created")
      }
      setDialogOpen(false)
      await loadFilters()
    } catch {
      toast.error("Failed to save filter")
    } finally {
      setSaving(false)
    }
  }

  const handleToggle = async (filter: Filter) => {
    try {
      await api.updateFilter(filter.id, { enabled: !filter.enabled })
      await loadFilters()
    } catch {
      toast.error("Failed to update filter")
    }
  }

  const handleDelete = async (filter: Filter) => {
    try {
      await api.deleteFilter(filter.id)
      toast.success("Filter deleted")
      await loadFilters()
    } catch {
      toast.error("Failed to delete filter")
    }
  }

  return (
    <div className="space-y-6 max-w-3xl">
      <div className="flex items-center justify-between">
        <div className="flex items-center gap-3">
          <div className="rounded-full bg-muted p-2">
            <FilterIcon className="h-5 w-5" />
          </div>
          <div>
            <h2 className="text-2xl font-bold">Filters</h2>
            <p className="text-sm text-muted-foreground">
              Sort and act on incoming mail automatically
            </p>
          </div>
        </div>
        <Button onClick={openCreate}>
          <Plus className="h-4 w-4 mr-1" />
          New Filter
        </Button>
      </div>

      {loading ? (
        <div className="space-y-3">
          {[1, 2].map((i) => (
            <div key={i} className="rounded-lg border p-4">
              <Skeleton className="h-5 w-48" />
              <Skeleton className="mt-2 h-3 w-full" />
            </div>
          ))}
        </div>
      ) : filters.length === 0 ? (
        <div className="flex flex-col items-center justify-center py-16 text-center">
          <div className="rounded-full bg-muted p-4">
            <FilterIcon className="h-8 w-8 text-muted-foreground" />
          </div>
          <h3 className="mt-4 text-lg font-semibold">No filters yet</h3>
          <p className="text-sm text-muted-foreground">
            Create a filter to automatically organize incoming mail.
          </p>
        </div>
      ) : (
        <div className="space-y-3">
          {filters.map((filter) => (
            <div key={filter.id} className="rounded-lg border bg-card p-4">
              <div className="flex items-start justify-between gap-4">
                <div className="min-w-0">
                  <div className="flex items-center gap-2">
                    <span className="font-medium">{filter.name}</span>
                    {!filter.enabled && (
                      <Badge variant="secondary" className="text-[10px]">
                        Disabled
                      </Badge>
                    )}
                  </div>
                  <p className="mt-1 text-sm text-muted-foreground">
                    {filter.matchAll ? "Match all" : "Match any"} of{" "}
                    {filter.conditions.length} condition
                    {filter.conditions.length !== 1 ? "s" : ""} →{" "}
                    {filter.actions.length} action
                    {filter.actions.length !== 1 ? "s" : ""}
                  </p>
                </div>
                <div className="flex shrink-0 items-center gap-2">
                  <Switch
                    checked={filter.enabled}
                    onCheckedChange={() => handleToggle(filter)}
                  />
                  <Button
                    variant="ghost"
                    size="icon"
                    className="h-8 w-8"
                    onClick={() => openEdit(filter)}
                  >
                    <Pencil className="h-4 w-4" />
                  </Button>
                  <Button
                    variant="ghost"
                    size="icon"
                    className="h-8 w-8 text-destructive"
                    onClick={() => handleDelete(filter)}
                  >
                    <Trash2 className="h-4 w-4" />
                  </Button>
                </div>
              </div>
            </div>
          ))}
        </div>
      )}

      <Dialog open={dialogOpen} onOpenChange={setDialogOpen}>
        <DialogContent className="max-h-[85vh] overflow-y-auto sm:max-w-2xl">
          <DialogHeader>
            <DialogTitle>{editingId ? "Edit Filter" : "New Filter"}</DialogTitle>
          </DialogHeader>

          <div className="space-y-5">
            <div className="space-y-2">
              <Label htmlFor="filter-name">Name</Label>
              <Input
                id="filter-name"
                value={draft.name}
                onChange={(e) => setDraft({ ...draft, name: e.target.value })}
                placeholder="e.g. Newsletters to Archive"
              />
            </div>

            <div className="flex items-center justify-between">
              <div>
                <p className="font-medium">Match all conditions</p>
                <p className="text-sm text-muted-foreground">
                  On: every condition must match. Off: any condition matches.
                </p>
              </div>
              <Switch
                checked={draft.matchAll}
                onCheckedChange={(v) => setDraft({ ...draft, matchAll: v })}
              />
            </div>

            {/* Conditions */}
            <div className="space-y-3">
              <div className="flex items-center justify-between">
                <Label>Conditions</Label>
                <Button
                  variant="outline"
                  size="sm"
                  onClick={() =>
                    setDraft({ ...draft, conditions: [...draft.conditions, emptyCondition()] })
                  }
                >
                  <Plus className="h-4 w-4 mr-1" />
                  Add
                </Button>
              </div>
              {draft.conditions.map((cond, i) => (
                <div key={i} className="flex flex-wrap items-center gap-2">
                  <Select
                    value={cond.field}
                    onValueChange={(v) =>
                      updateCondition(i, { field: v as FilterCondition["field"] })
                    }
                  >
                    <SelectTrigger className="w-[120px]">
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      {CONDITION_FIELDS.map((f) => (
                        <SelectItem key={f.value} value={f.value}>
                          {f.label}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                  <Select
                    value={cond.operator}
                    onValueChange={(v) =>
                      updateCondition(i, { operator: v as FilterCondition["operator"] })
                    }
                  >
                    <SelectTrigger className="w-[130px]">
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      {CONDITION_OPERATORS.map((o) => (
                        <SelectItem key={o.value} value={o.value}>
                          {o.label}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                  {cond.field === "header" && (
                    <Input
                      className="w-[140px]"
                      placeholder="Header name"
                      value={cond.headerName ?? ""}
                      onChange={(e) => updateCondition(i, { headerName: e.target.value })}
                    />
                  )}
                  <Input
                    className="min-w-[140px] flex-1"
                    placeholder="Value"
                    value={cond.value}
                    onChange={(e) => updateCondition(i, { value: e.target.value })}
                  />
                  {draft.conditions.length > 1 && (
                    <Button
                      variant="ghost"
                      size="icon"
                      className="h-8 w-8"
                      onClick={() =>
                        setDraft({
                          ...draft,
                          conditions: draft.conditions.filter((_, idx) => idx !== i),
                        })
                      }
                    >
                      <X className="h-4 w-4" />
                    </Button>
                  )}
                </div>
              ))}
            </div>

            {/* Actions */}
            <div className="space-y-3">
              <div className="flex items-center justify-between">
                <Label>Actions</Label>
                <Button
                  variant="outline"
                  size="sm"
                  onClick={() =>
                    setDraft({ ...draft, actions: [...draft.actions, emptyAction()] })
                  }
                >
                  <Plus className="h-4 w-4 mr-1" />
                  Add
                </Button>
              </div>
              {draft.actions.map((action, i) => (
                <div key={i} className="flex flex-wrap items-center gap-2">
                  <Select
                    value={action.type}
                    onValueChange={(v) =>
                      updateAction(i, { type: v as FilterAction["type"] })
                    }
                  >
                    <SelectTrigger className="w-[160px]">
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      {ACTION_TYPES.map((a) => (
                        <SelectItem key={a.value} value={a.value}>
                          {a.label}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                  {(action.type === "move" || action.type === "copy") && (
                    <Input
                      className="min-w-[140px] flex-1"
                      placeholder="Target folder"
                      value={action.target ?? ""}
                      onChange={(e) => updateAction(i, { target: e.target.value })}
                    />
                  )}
                  {action.type === "forward" && (
                    <Input
                      className="min-w-[140px] flex-1"
                      placeholder="Forward to address"
                      value={action.forwardTo ?? ""}
                      onChange={(e) => updateAction(i, { forwardTo: e.target.value })}
                    />
                  )}
                  {draft.actions.length > 1 && (
                    <Button
                      variant="ghost"
                      size="icon"
                      className="h-8 w-8"
                      onClick={() =>
                        setDraft({
                          ...draft,
                          actions: draft.actions.filter((_, idx) => idx !== i),
                        })
                      }
                    >
                      <X className="h-4 w-4" />
                    </Button>
                  )}
                </div>
              ))}
            </div>
          </div>

          <DialogFooter>
            <Button variant="outline" onClick={() => setDialogOpen(false)} disabled={saving}>
              Cancel
            </Button>
            <Button onClick={handleSave} disabled={saving}>
              {editingId ? "Save" : "Create"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  )
}
