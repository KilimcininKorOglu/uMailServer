import { useState, useEffect, useCallback } from "react"
import { CalendarDays, Plus, MapPin, Clock, Edit, Trash2, MoreHorizontal, Users } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Switch } from "@/components/ui/switch"
import { Textarea } from "@/components/ui/textarea"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { toast } from "sonner"
import api, { type CalendarEvent, type UserFreeBusy } from "@/utils/api"

// rfc3339ToLocalInput converts an RFC3339 instant to the value a
// datetime-local input expects ("YYYY-MM-DDTHH:mm" in local time).
function rfc3339ToLocalInput(value: string): string {
  const d = new Date(value)
  if (isNaN(d.getTime())) return ""
  const pad = (n: number) => String(n).padStart(2, "0")
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`
}

// localInputToRFC3339 converts a datetime-local value to an RFC3339 instant.
function localInputToRFC3339(value: string): string {
  const d = new Date(value)
  return isNaN(d.getTime()) ? "" : d.toISOString()
}

interface EventForm {
  summary: string
  start: string
  end: string
  allDay: boolean
  location: string
  description: string
  attendees: string
}

const emptyForm: EventForm = { summary: "", start: "", end: "", allDay: false, location: "", description: "", attendees: "" }

function dayKey(value: string): string {
  const d = new Date(value)
  return isNaN(d.getTime()) ? value : d.toLocaleDateString(undefined, { weekday: "long", year: "numeric", month: "long", day: "numeric" })
}

function timeLabel(ev: CalendarEvent): string {
  if (ev.allDay) return "All day"
  const start = new Date(ev.start)
  const opts: Intl.DateTimeFormatOptions = { hour: "2-digit", minute: "2-digit" }
  const s = isNaN(start.getTime()) ? "" : start.toLocaleTimeString(undefined, opts)
  if (!ev.end) return s
  const end = new Date(ev.end)
  return isNaN(end.getTime()) ? s : `${s} – ${end.toLocaleTimeString(undefined, opts)}`
}

export function CalendarPage() {
  const [events, setEvents] = useState<CalendarEvent[]>([])
  const [loading, setLoading] = useState(true)
  const [dialogOpen, setDialogOpen] = useState(false)
  const [editingUID, setEditingUID] = useState<string | null>(null)
  const [form, setForm] = useState<EventForm>(emptyForm)
  const [busy, setBusy] = useState(false)
  const [deleteTarget, setDeleteTarget] = useState<CalendarEvent | null>(null)

  // Availability (free/busy) lookup.
  const [fbOpen, setFbOpen] = useState(false)
  const [fbEmails, setFbEmails] = useState("")
  const [fbDate, setFbDate] = useState(() => {
    const d = new Date()
    const pad = (n: number) => String(n).padStart(2, "0")
    return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`
  })
  const [fbLoading, setFbLoading] = useState(false)
  const [fbResults, setFbResults] = useState<UserFreeBusy[] | null>(null)

  const load = useCallback(async () => {
    setLoading(true)
    try {
      const res = await api.getCalendarEvents()
      const list = (res.events ?? []).slice().sort((a, b) => a.start.localeCompare(b.start))
      setEvents(list)
    } catch {
      setEvents([])
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    load()
  }, [load])

  const openCreate = () => {
    setEditingUID(null)
    setForm(emptyForm)
    setDialogOpen(true)
  }

  const openEdit = (ev: CalendarEvent) => {
    setEditingUID(ev.uid)
    setForm({
      summary: ev.summary,
      start: ev.allDay ? ev.start.slice(0, 10) : rfc3339ToLocalInput(ev.start),
      end: ev.end ? (ev.allDay ? ev.end.slice(0, 10) : rfc3339ToLocalInput(ev.end)) : "",
      allDay: !!ev.allDay,
      location: ev.location ?? "",
      description: ev.description ?? "",
      attendees: (ev.attendees ?? []).join(", "),
    })
    setDialogOpen(true)
  }

  const submit = async () => {
    if (!form.summary.trim()) {
      toast.error("Title is required")
      return
    }
    if (!form.start) {
      toast.error("Start is required")
      return
    }
    const attendees = form.attendees
      .split(/[\s,;]+/)
      .map((a) => a.trim())
      .filter(Boolean)
    const payload = {
      summary: form.summary.trim(),
      start: form.allDay ? form.start : localInputToRFC3339(form.start),
      end: form.end ? (form.allDay ? form.end : localInputToRFC3339(form.end)) : undefined,
      allDay: form.allDay || undefined,
      location: form.location || undefined,
      description: form.description || undefined,
      attendees: attendees.length > 0 ? attendees : undefined,
    }
    setBusy(true)
    try {
      if (editingUID) {
        await api.updateCalendarEvent(editingUID, payload)
        toast.success("Event updated")
      } else {
        await api.createCalendarEvent(payload)
        toast.success("Event created")
      }
      setDialogOpen(false)
      await load()
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "Failed to save event")
    } finally {
      setBusy(false)
    }
  }

  const confirmDelete = async () => {
    if (!deleteTarget || busy) return
    setBusy(true)
    try {
      await api.deleteCalendarEvent(deleteTarget.uid)
      toast.success("Event deleted")
      setDeleteTarget(null)
      await load()
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "Failed to delete event")
    } finally {
      setBusy(false)
    }
  }

  const checkAvailability = async () => {
    const emails = fbEmails
      .split(/[\s,;]+/)
      .map((e) => e.trim())
      .filter(Boolean)
    if (emails.length === 0) {
      toast.error("Enter at least one email address")
      return
    }
    if (!fbDate) {
      toast.error("Pick a date")
      return
    }
    // Query the whole local day.
    const dayStart = new Date(`${fbDate}T00:00:00`)
    const dayEnd = new Date(dayStart.getTime() + 24 * 60 * 60 * 1000)
    setFbLoading(true)
    setFbResults(null)
    try {
      const res = await api.getFreeBusy(emails, dayStart.toISOString(), dayEnd.toISOString())
      setFbResults(res.freeBusy ?? [])
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "Failed to check availability")
    } finally {
      setFbLoading(false)
    }
  }

  // Group sorted events by day for the agenda view.
  const groups: { day: string; items: CalendarEvent[] }[] = []
  for (const ev of events) {
    const key = dayKey(ev.start)
    const last = groups[groups.length - 1]
    if (last && last.day === key) last.items.push(ev)
    else groups.push({ day: key, items: [ev] })
  }

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <div className="flex items-center gap-2">
          <CalendarDays className="h-6 w-6 text-primary" />
          <h1 className="text-2xl font-bold">Calendar</h1>
        </div>
        <div className="flex items-center gap-2">
          <Button variant="outline" onClick={() => { setFbResults(null); setFbOpen(true) }}>
            <Users className="mr-2 h-4 w-4" />
            Availability
          </Button>
          <Button onClick={openCreate}>
            <Plus className="mr-2 h-4 w-4" />
            New Event
          </Button>
        </div>
      </div>

      {loading ? (
        <p className="text-sm text-muted-foreground py-8 text-center">Loading…</p>
      ) : events.length === 0 ? (
        <div className="flex flex-col items-center justify-center py-16 text-center">
          <div className="rounded-full bg-muted p-4">
            <CalendarDays className="h-8 w-8 text-muted-foreground" />
          </div>
          <h3 className="mt-4 text-lg font-medium">No events</h3>
          <p className="text-muted-foreground mt-1">Create an event to get started.</p>
          <Button className="mt-4" onClick={openCreate}>
            <Plus className="mr-2 h-4 w-4" />
            New Event
          </Button>
        </div>
      ) : (
        <div className="space-y-6">
          {groups.map((group) => (
            <div key={group.day}>
              <h2 className="mb-2 text-sm font-semibold text-muted-foreground">{group.day}</h2>
              <div className="rounded-lg border bg-card divide-y">
                {group.items.map((ev) => (
                  <div key={ev.uid} className="flex items-start gap-4 p-4 hover:bg-accent/50 transition-colors">
                    <div className="flex w-24 shrink-0 items-center gap-1 text-sm text-muted-foreground">
                      <Clock className="h-3.5 w-3.5" />
                      {timeLabel(ev)}
                    </div>
                    <div className="flex-1 min-w-0">
                      <p className="font-medium truncate">{ev.summary}</p>
                      {ev.location && (
                        <p className="flex items-center gap-1 text-sm text-muted-foreground">
                          <MapPin className="h-3.5 w-3.5" />
                          {ev.location}
                        </p>
                      )}
                      {ev.description && (
                        <p className="text-sm text-muted-foreground truncate">{ev.description}</p>
                      )}
                    </div>
                    <DropdownMenu>
                      <DropdownMenuTrigger asChild>
                        <Button variant="ghost" size="icon" className="h-8 w-8">
                          <MoreHorizontal className="h-4 w-4" />
                        </Button>
                      </DropdownMenuTrigger>
                      <DropdownMenuContent align="end">
                        <DropdownMenuItem onClick={() => openEdit(ev)}>
                          <Edit className="mr-2 h-4 w-4" />
                          Edit
                        </DropdownMenuItem>
                        <DropdownMenuItem className="text-destructive" onClick={() => setDeleteTarget(ev)}>
                          <Trash2 className="mr-2 h-4 w-4" />
                          Delete
                        </DropdownMenuItem>
                      </DropdownMenuContent>
                    </DropdownMenu>
                  </div>
                ))}
              </div>
            </div>
          ))}
        </div>
      )}

      {/* Create / edit dialog */}
      <Dialog open={dialogOpen} onOpenChange={setDialogOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{editingUID ? "Edit Event" : "New Event"}</DialogTitle>
            <DialogDescription>Events are shared with CalDAV clients.</DialogDescription>
          </DialogHeader>
          <div className="space-y-4 py-2">
            <div className="space-y-2">
              <Label htmlFor="ev-summary">Title</Label>
              <Input
                id="ev-summary"
                value={form.summary}
                onChange={(e) => setForm({ ...form, summary: e.target.value })}
                placeholder="Event title"
              />
            </div>
            <div className="flex items-center justify-between">
              <Label htmlFor="ev-allday">All day</Label>
              <Switch
                id="ev-allday"
                checked={form.allDay}
                onCheckedChange={(checked) => setForm({ ...form, allDay: checked })}
              />
            </div>
            <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
              <div className="space-y-2">
                <Label htmlFor="ev-start">Start</Label>
                <Input
                  id="ev-start"
                  type={form.allDay ? "date" : "datetime-local"}
                  value={form.start}
                  onChange={(e) => setForm({ ...form, start: e.target.value })}
                />
              </div>
              <div className="space-y-2">
                <Label htmlFor="ev-end">End</Label>
                <Input
                  id="ev-end"
                  type={form.allDay ? "date" : "datetime-local"}
                  value={form.end}
                  onChange={(e) => setForm({ ...form, end: e.target.value })}
                />
              </div>
            </div>
            <div className="space-y-2">
              <Label htmlFor="ev-location">Location</Label>
              <Input
                id="ev-location"
                value={form.location}
                onChange={(e) => setForm({ ...form, location: e.target.value })}
                placeholder="Optional"
              />
            </div>
            <div className="space-y-2">
              <Label htmlFor="ev-desc">Description</Label>
              <Textarea
                id="ev-desc"
                value={form.description}
                onChange={(e) => setForm({ ...form, description: e.target.value })}
                rows={3}
                placeholder="Optional"
              />
            </div>
            <div className="space-y-2">
              <Label htmlFor="ev-attendees">Attendees</Label>
              <Input
                id="ev-attendees"
                value={form.attendees}
                onChange={(e) => setForm({ ...form, attendees: e.target.value })}
                placeholder="email1@example.com, email2@example.com"
              />
              <p className="text-xs text-muted-foreground">
                Attendees receive an email invitation they can accept or decline.
              </p>
            </div>
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={() => setDialogOpen(false)} disabled={busy}>
              Cancel
            </Button>
            <Button onClick={submit} disabled={busy}>
              {editingUID ? "Save" : "Create"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* Availability (free/busy) lookup */}
      <Dialog open={fbOpen} onOpenChange={setFbOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Check Availability</DialogTitle>
            <DialogDescription>
              See when people are busy on a given day. Only busy time ranges are shown, never event details.
            </DialogDescription>
          </DialogHeader>
          <div className="space-y-4 py-2">
            <div className="space-y-2">
              <Label htmlFor="fb-emails">People</Label>
              <Textarea
                id="fb-emails"
                value={fbEmails}
                onChange={(e) => setFbEmails(e.target.value)}
                rows={2}
                placeholder="email1@example.com, email2@example.com"
              />
            </div>
            <div className="space-y-2">
              <Label htmlFor="fb-date">Date</Label>
              <Input
                id="fb-date"
                type="date"
                value={fbDate}
                onChange={(e) => setFbDate(e.target.value)}
              />
            </div>
            {fbResults && (
              <div className="space-y-3 rounded-lg border bg-muted/30 p-3">
                {fbResults.length === 0 ? (
                  <p className="text-sm text-muted-foreground">No results.</p>
                ) : (
                  fbResults.map((r) => (
                    <div key={r.user}>
                      <p className="text-sm font-medium">{r.user}</p>
                      {r.busy.length === 0 ? (
                        <p className="text-sm text-muted-foreground">Free all day</p>
                      ) : (
                        <ul className="mt-1 space-y-0.5">
                          {r.busy.map((b, i) => (
                            <li key={i} className="flex items-center gap-1.5 text-sm text-muted-foreground">
                              <Clock className="h-3.5 w-3.5" />
                              {new Date(b.start).toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" })}
                              {" – "}
                              {new Date(b.end).toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" })}
                            </li>
                          ))}
                        </ul>
                      )}
                    </div>
                  ))
                )}
              </div>
            )}
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={() => setFbOpen(false)} disabled={fbLoading}>
              Close
            </Button>
            <Button onClick={checkAvailability} disabled={fbLoading}>
              {fbLoading ? "Checking…" : "Check"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* Delete confirmation */}
      <Dialog open={deleteTarget !== null} onOpenChange={(open) => { if (!open) setDeleteTarget(null) }}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Delete Event</DialogTitle>
            <DialogDescription>Delete "{deleteTarget?.summary}"? This cannot be undone.</DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="outline" onClick={() => setDeleteTarget(null)} disabled={busy}>
              Cancel
            </Button>
            <Button variant="destructive" onClick={confirmDelete} disabled={busy}>
              <Trash2 className="mr-2 h-4 w-4" />
              Delete
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  )
}
