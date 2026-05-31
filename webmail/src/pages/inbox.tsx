import { useState, useEffect, useCallback } from "react"
import { useNavigate } from "react-router-dom"
import {
  Star,
  Archive,
  Trash2,
  MailOpen,
  Paperclip,
  RefreshCw,
  ChevronLeft,
  ChevronRight,
  Filter,
  MoreHorizontal,
  List,
  LayoutGrid,
  ArrowUpDown,
} from "lucide-react"
import { WelcomeBanner } from "@/components/welcome-banner"
import { cn } from "@/lib/utils"
import { Button } from "@/components/ui/button"
import { Checkbox } from "@/components/ui/checkbox"
import { Badge } from "@/components/ui/badge"
import { Skeleton } from "@/components/ui/skeleton"
import { Separator } from "@/components/ui/separator"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs"
import { toast } from "sonner"
import api from "@/utils/api"
import type { Mail } from "@/utils/api"

interface Email {
  id: string
  from: string
  fromEmail: string
  subject: string
  preview: string
  date: string
  read: boolean
  starred: boolean
  hasAttachments: boolean
  folder: string
  labels: string[]
}

type ViewMode = "list" | "compact"
type SortOption = "date" | "from" | "subject"
type SortDir = "asc" | "desc"

interface InboxPageProps {
  folder?: string
}

export function InboxPage({ folder = "inbox" }: InboxPageProps) {
  const navigate = useNavigate()
  const [emails, setEmails] = useState<Email[]>([])
  const [selectedEmails, setSelectedEmails] = useState<Set<string>>(new Set())
  const [activeFilter, setActiveFilter] = useState("all")
  const [loading, setLoading] = useState(true)
  const [viewMode, setViewMode] = useState<ViewMode>("list")
  const [sortBy, setSortBy] = useState<SortOption>("date")
  const [sortDir, setSortDir] = useState<SortDir>("desc")
  const [page, setPage] = useState(0)
  const PAGE_SIZE = 25

  // Reset to the first page when the folder or filter changes.
  useEffect(() => {
    setPage(0)
  }, [folder, activeFilter])
  const [showWelcome, setShowWelcome] = useState(true)

  // Load emails from API (reused by the initial load and the Refresh button)
  const loadEmails = useCallback(async () => {
    setLoading(true)
    try {
      // Map folder to API folder name
      const apiFolder = folder === "starred" ? "inbox" : folder
      const result = await api.get<{ emails?: Mail[] }>(`/mail/${apiFolder}`)

      if (result && result.emails) {
        // Convert API Mail to Email format
        const loadedEmails: Email[] = result.emails.map((mail: Mail) => {
          // Parse from field to extract name and email
          const fromParts = mail.from.split('<')
          const fromEmail = fromParts.length > 1 ? fromParts[1].replace('>', '') : mail.from
          const fromName = fromParts.length > 1 ? fromParts[0].trim() : mail.from

          return {
            id: mail.id,
            from: fromName,
            fromEmail: fromEmail,
            subject: mail.subject,
            preview: mail.preview,
            date: mail.date,
            read: mail.read,
            starred: mail.starred,
            hasAttachments: mail.hasAttachments,
            folder: mail.folder.toLowerCase(),
            labels: [], // Labels from API if available
          }
        })

        // Filter for starred if needed
        const filteredEmails = folder === "starred"
          ? loadedEmails.filter(e => e.starred)
          : loadedEmails

        setEmails(filteredEmails)
      } else {
        setEmails([])
      }
    } catch (err) {
      console.error('Failed to load emails:', err)
      setEmails([])
    } finally {
      setLoading(false)
    }
  }, [folder])

  useEffect(() => {
    loadEmails()
  }, [loadEmails])

  const toggleSelectAll = () => {
    if (selectedEmails.size === emails.length) {
      setSelectedEmails(new Set())
    } else {
      setSelectedEmails(new Set(emails.map((e) => e.id)))
    }
  }

  const toggleSelect = (id: string) => {
    const newSelected = new Set(selectedEmails)
    if (newSelected.has(id)) {
      newSelected.delete(id)
    } else {
      newSelected.add(id)
    }
    setSelectedEmails(newSelected)
  }

  const toggleStar = async (id: string, e: React.MouseEvent) => {
    e.stopPropagation()
    const email = emails.find((em) => em.id === id)
    if (!email) return
    const next = !email.starred
    try {
      await api.setFlag(id, "\\Flagged", next)
      setEmails((prev) => prev.map((em) =>
        em.id === id ? { ...em, starred: next } : em
      ))
    } catch (err) {
      console.error("Failed to update star:", err)
      toast.error("Failed to update star")
    }
  }

  const markAsRead = async (id: string, e: React.MouseEvent) => {
    e.stopPropagation()
    try {
      await api.setFlag(id, "\\Seen", true)
      setEmails((prev) => prev.map((email) =>
        email.id === id ? { ...email, read: true } : email
      ))
    } catch (err) {
      console.error("Failed to mark message as read:", err)
      toast.error("Failed to mark as read")
    }
  }

  const handleRefresh = async () => {
    await loadEmails()
    toast.success("Inbox refreshed")
  }

  const archiveEmails = async (ids: string[]) => {
    if (ids.length === 0) return
    try {
      await Promise.all(ids.map((id) => api.moveMail(id, "archive")))
      setEmails((prev) => prev.filter((e) => !ids.includes(e.id)))
      setSelectedEmails(new Set())
      toast.success(`${ids.length} message${ids.length !== 1 ? "s" : ""} archived`)
    } catch (err) {
      console.error("Failed to archive messages:", err)
      toast.error("Failed to archive messages")
    }
  }

  const handleArchive = () => archiveEmails([...selectedEmails])

  const deleteEmails = async (ids: string[]) => {
    if (ids.length === 0) return
    try {
      await Promise.all(ids.map((id) => api.deleteMail(id)))
      setEmails((prev) => prev.filter((e) => !ids.includes(e.id)))
      setSelectedEmails(new Set())
      toast.success(`${ids.length} message${ids.length !== 1 ? "s" : ""} moved to trash`)
    } catch (err) {
      console.error("Failed to delete messages:", err)
      toast.error("Failed to delete messages")
    }
  }

  const handleDelete = () => deleteEmails([...selectedEmails])

  const handleMarkRead = async () => {
    const ids = [...selectedEmails]
    if (ids.length === 0) return
    try {
      await Promise.all(ids.map((id) => api.setFlag(id, "\\Seen", true)))
      setEmails((prev) => prev.map((e) =>
        ids.includes(e.id) ? { ...e, read: true } : e
      ))
      setSelectedEmails(new Set())
      toast.success(`${ids.length} message${ids.length !== 1 ? "s" : ""} marked as read`)
    } catch (err) {
      console.error("Failed to mark messages as read:", err)
      toast.error("Failed to mark as read")
    }
  }

  const filteredEmails = emails
    .filter((email) => {
      if (activeFilter === "unread") return !email.read
      if (activeFilter === "starred") return email.starred
      return true
    })
    .sort((a, b) => {
      const ts = (d: string) => {
        const t = Date.parse(d)
        return isNaN(t) ? 0 : t
      }
      let cmp = 0
      if (sortBy === "date") cmp = ts(a.date) - ts(b.date)
      else if (sortBy === "from") cmp = a.from.localeCompare(b.from)
      else if (sortBy === "subject") cmp = a.subject.localeCompare(b.subject)
      return sortDir === "asc" ? cmp : -cmp
    })

  const unreadCount = emails.filter((e) => !e.read).length

  const totalPages = Math.max(1, Math.ceil(filteredEmails.length / PAGE_SIZE))
  const currentPage = Math.min(page, totalPages - 1)
  const pageEmails = filteredEmails.slice(currentPage * PAGE_SIZE, currentPage * PAGE_SIZE + PAGE_SIZE)

  const EmailRow = ({ email }: { email: Email }) => (
    <div
      className={cn(
        "group flex cursor-pointer items-center gap-3 transition-all duration-200",
        viewMode === "list" ? "p-4 hover:bg-accent/50" : "p-2 hover:bg-accent/50",
        !email.read && viewMode === "list" && "bg-accent/5",
        selectedEmails.has(email.id) && "bg-primary/5"
      )}
      onClick={() => navigate(`/email/${email.id}`)}
    >
      <Checkbox
        checked={selectedEmails.has(email.id)}
        onCheckedChange={() => toggleSelect(email.id)}
        onClick={(e) => e.stopPropagation()}
      />

      <Button
        variant="ghost"
        size="icon"
        className={cn(
          "h-8 w-8 shrink-0 transition-colors",
          email.starred ? "text-amber-500" : "text-muted-foreground hover:text-foreground"
        )}
        onClick={(e) => toggleStar(email.id, e)}
      >
        <Star className={cn("h-4 w-4", email.starred && "fill-current")} />
      </Button>

      <div className={cn("flex-1 min-w-0", viewMode === "compact" && "flex items-center gap-4")}>
        <div className="flex items-center gap-2">
          <span className={cn("text-sm", !email.read ? "font-semibold" : "font-normal")}>
            {viewMode === "list" ? email.from : email.from.split(" ")[0]}
          </span>
          {email.labels.slice(0, viewMode === "compact" ? 0 : 1).map((label) => (
            <Badge key={label} variant="secondary" className="text-[10px] px-1.5 py-0">
              {label}
            </Badge>
          ))}
        </div>
        {viewMode === "list" && (
          <div className="flex items-center gap-2 text-sm text-muted-foreground">
            <span className={cn(!email.read && "text-foreground font-medium")}>
              {email.subject}
            </span>
            <span className="truncate">— {email.preview}</span>
          </div>
        )}
      </div>

      <div className={cn("flex items-center gap-2 shrink-0", viewMode === "compact" && "flex-row-reverse")}>
        {email.hasAttachments && (
          <Paperclip className="h-4 w-4 text-muted-foreground" />
        )}
        {!email.read && viewMode === "list" && (
          <span className="h-2 w-2 rounded-full bg-primary" />
        )}
        <span className={cn(
          "text-xs text-muted-foreground whitespace-nowrap",
          viewMode === "compact" && "w-12 text-right"
        )}>
          {email.date}
        </span>
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <Button
              variant="ghost"
              size="icon"
              className="h-8 w-8 opacity-0 group-hover:opacity-100"
              onClick={(e) => e.stopPropagation()}
            >
              <MoreHorizontal className="h-4 w-4" />
            </Button>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end">
            <DropdownMenuItem onClick={(e) => markAsRead(email.id, e)}>
              <MailOpen className="mr-2 h-4 w-4" />
              Mark as read
            </DropdownMenuItem>
            <DropdownMenuItem onClick={(e) => toggleStar(email.id, e)}>
              <Star className={cn("mr-2 h-4 w-4", email.starred && "fill-current")} />
              {email.starred ? "Remove star" : "Add star"}
            </DropdownMenuItem>
            <DropdownMenuItem
              onClick={(e) => {
                e.stopPropagation()
                archiveEmails([email.id])
              }}
            >
              <Archive className="mr-2 h-4 w-4" />
              Archive
            </DropdownMenuItem>
            <DropdownMenuSeparator />
            <DropdownMenuItem
              className="text-destructive"
              onClick={(e) => {
                e.stopPropagation()
                deleteEmails([email.id])
              }}
            >
              <Trash2 className="mr-2 h-4 w-4" />
              Delete
            </DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>
      </div>
    </div>
  )

  return (
    <div className="space-y-4">
      {showWelcome && folder === "inbox" && (
        <WelcomeBanner onDismiss={() => setShowWelcome(false)} />
      )}
      <div className="flex flex-col gap-4 sm:flex-row sm:items-center sm:justify-between">
        <div className="flex items-center gap-2">
          <Checkbox
            checked={selectedEmails.size === emails.length && emails.length > 0}
            onCheckedChange={toggleSelectAll}
          />

          {selectedEmails.size > 0 ? (
            <div className="flex items-center gap-2 animate-in fade-in slide-in-from-left-2">
              <span className="text-sm text-muted-foreground">
                {selectedEmails.size} selected
              </span>
              <Separator orientation="vertical" className="h-4" />
              <Button variant="ghost" size="icon" className="h-8 w-8" onClick={handleArchive} title="Archive">
                <Archive className="h-4 w-4" />
              </Button>
              <Button variant="ghost" size="icon" className="h-8 w-8 text-destructive" onClick={handleDelete} title="Delete">
                <Trash2 className="h-4 w-4" />
              </Button>
              <Button variant="ghost" size="icon" className="h-8 w-8" onClick={handleMarkRead} title="Mark as read">
                <MailOpen className="h-4 w-4" />
              </Button>
            </div>
          ) : (
            <Button variant="ghost" size="icon" className="h-8 w-8" onClick={handleRefresh}>
              <RefreshCw className={cn("h-4 w-4", loading && "animate-spin")} />
            </Button>
          )}

          {unreadCount > 0 && activeFilter === "all" && (
            <Badge variant="secondary" className="ml-2">
              {unreadCount} unread
            </Badge>
          )}
        </div>

        <div className="flex items-center gap-2">
          <Tabs value={activeFilter} onValueChange={setActiveFilter}>
            <TabsList>
              <TabsTrigger value="all">All</TabsTrigger>
              <TabsTrigger value="unread">Unread</TabsTrigger>
              <TabsTrigger value="starred">Starred</TabsTrigger>
            </TabsList>
          </Tabs>

          <Separator orientation="vertical" className="h-6" />

          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <Button variant="ghost" size="icon" className="h-8 w-8" title="Sort">
                <ArrowUpDown className="h-4 w-4" />
              </Button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end">
              <DropdownMenuItem onClick={() => setSortBy("date")}>
                Date {sortBy === "date" && "✓"}
              </DropdownMenuItem>
              <DropdownMenuItem onClick={() => setSortBy("from")}>
                Sender {sortBy === "from" && "✓"}
              </DropdownMenuItem>
              <DropdownMenuItem onClick={() => setSortBy("subject")}>
                Subject {sortBy === "subject" && "✓"}
              </DropdownMenuItem>
              <DropdownMenuSeparator />
              <DropdownMenuItem onClick={() => setSortDir((d) => (d === "asc" ? "desc" : "asc"))}>
                {sortDir === "asc" ? "Ascending" : "Descending"}
              </DropdownMenuItem>
            </DropdownMenuContent>
          </DropdownMenu>

          <div className="flex border rounded-md">
            <Button
              variant={viewMode === "list" ? "secondary" : "ghost"}
              size="icon"
              className="h-8 w-8 rounded-r-none"
              onClick={() => setViewMode("list")}
            >
              <List className="h-4 w-4" />
            </Button>
            <Button
              variant={viewMode === "compact" ? "secondary" : "ghost"}
              size="icon"
              className="h-8 w-8 rounded-l-none"
              onClick={() => setViewMode("compact")}
            >
              <LayoutGrid className="h-4 w-4" />
            </Button>
          </div>
        </div>
      </div>

      <div className={cn(
        "rounded-lg border bg-card",
        viewMode === "compact" && "divide-y"
      )}>
        {loading ? (
          <div className={cn(viewMode === "list" ? "divide-y" : "")}>
            {[1, 2, 3, 4, 5].map((i) => (
              <div key={i} className={cn("flex items-start gap-4", viewMode === "list" ? "p-4" : "p-2")}>
                <Skeleton className="h-4 w-4" />
                <Skeleton className="h-4 w-4" />
                <div className="flex-1 space-y-2">
                  <Skeleton className="h-4 w-32" />
                  {viewMode === "list" && <Skeleton className="h-3 w-full" />}
                </div>
              </div>
            ))}
          </div>
        ) : filteredEmails.length === 0 ? (
          <div className="flex flex-col items-center justify-center py-16 text-center">
            <div className="rounded-full bg-muted p-4">
              <Filter className="h-8 w-8 text-muted-foreground" />
            </div>
            <h3 className="mt-4 text-lg font-semibold">No emails</h3>
            <p className="text-sm text-muted-foreground">
              {folder === "starred" || activeFilter === "starred"
                ? "No starred messages."
                : activeFilter === "unread"
                ? "No unread messages."
                : "Your inbox is empty."}
            </p>
          </div>
        ) : (
          <div className={cn(viewMode === "list" ? "divide-y" : "")}>
            {pageEmails.map((email) => (
              <EmailRow key={email.id} email={email} />
            ))}
          </div>
        )}
      </div>

      <div className="flex items-center justify-between">
        <span className="text-sm text-muted-foreground">
          {filteredEmails.length} message{filteredEmails.length !== 1 ? "s" : ""}
          {totalPages > 1 && ` · Page ${currentPage + 1} of ${totalPages}`}
        </span>
        <div className="flex items-center gap-2">
          <Button
            variant="outline"
            size="icon"
            disabled={currentPage <= 0}
            onClick={() => setPage((p) => Math.max(0, p - 1))}
          >
            <ChevronLeft className="h-4 w-4" />
          </Button>
          <Button
            variant="outline"
            size="icon"
            disabled={currentPage >= totalPages - 1}
            onClick={() => setPage((p) => Math.min(totalPages - 1, p + 1))}
          >
            <ChevronRight className="h-4 w-4" />
          </Button>
        </div>
      </div>
    </div>
  )
}
