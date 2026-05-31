import { useState, useEffect, useCallback } from "react"
import { NavLink, useLocation, useNavigate } from "react-router-dom"
import {
  Inbox,
  Send,
  FileText,
  Trash2,
  Star,
  AlertCircle,
  Settings,
  ChevronLeft,
  ChevronRight,
  PenSquare,
  FolderOpen,
  FolderPlus,
  Pencil,
  MoreHorizontal,
  CalendarDays,
  Users,
  Search,
  Mail,
  Filter,
  MessagesSquare,
  ChevronDown,
  ChevronUp,
} from "lucide-react"
import { toast } from "sonner"
import { cn } from "@/lib/utils"
import { Button } from "@/components/ui/button"
import { Badge } from "@/components/ui/badge"
import { Input } from "@/components/ui/input"
import { Separator } from "@/components/ui/separator"
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { useAuth } from "@/contexts/AuthContext"
import { useMailbox } from "@/contexts/MailboxContext"
import api from "@/utils/api"

interface SidebarProps {
  collapsed: boolean
  onToggle: () => void
  mobileOpen?: boolean
  onMobileClose?: () => void
}

interface NavItem {
  icon: React.ElementType
  label: string
  path: string
  count?: number
  color?: string
  shortcut?: string
  badgeColor?: string
}

const mainNavItems: NavItem[] = [
  { icon: Inbox, label: "Inbox", path: "/inbox", shortcut: "gi" },
  { icon: MessagesSquare, label: "Conversations", path: "/threads" },
  { icon: Search, label: "Search", path: "/search", shortcut: "/" },
  { icon: Star, label: "Starred", path: "/starred", shortcut: "gs" },
  { icon: Send, label: "Sent", path: "/sent", shortcut: "gt" },
  { icon: FileText, label: "Drafts", path: "/drafts", shortcut: "gd" },
  { icon: Trash2, label: "Trash", path: "/trash", shortcut: "gT" },
  { icon: Users, label: "Contacts", path: "/contacts" },
  { icon: CalendarDays, label: "Calendar", path: "/calendar" },
  { icon: Filter, label: "Filters", path: "/filters" },
]

// Standard mailboxes already shown in the main nav (or as Spam below); excluded
// from the dynamic custom-folder list.
const standardMailboxes = new Set(["inbox", "sent", "drafts", "trash", "junk"])

const folderItems: NavItem[] = [
  { icon: AlertCircle, label: "Spam", path: "/spam", color: "text-red-500" },
]

// Shared mailbox item for display
interface SharedMailboxItem {
  owner: string
  mailbox: string
  rights?: string
}

const NavItemComponent = ({ item, isExpanded }: { item: NavItem; isExpanded: boolean }) => {
  const location = useLocation()
  const isActive = location.pathname === item.path

  const content = (
    <NavLink
      to={item.path}
      className={cn(
        "flex items-center gap-3 rounded-lg px-3 py-2.5 text-sm font-medium transition-all duration-200 group relative",
        isActive
          ? "bg-primary/10 text-primary shadow-sm"
          : "text-muted-foreground hover:bg-accent hover:text-accent-foreground"
      )}
    >
      <item.icon
        className={cn(
          "h-5 w-5 shrink-0 transition-colors",
          item.color || (isActive ? "text-primary" : "text-muted-foreground group-hover:text-foreground")
        )}
      />
      {isExpanded && (
        <>
          <span className="flex-1">{item.label}</span>
          {item.shortcut && (
            <kbd className="hidden group-hover:inline-flex items-center gap-0.5 rounded border px-1.5 py-0.5 text-[10px] font-mono text-muted-foreground bg-muted">
              <span>⌘</span>{item.shortcut}
            </kbd>
          )}
          {item.count !== undefined && item.count > 0 && (
            <Badge
              variant={isActive ? "default" : "secondary"}
              className="h-5 min-w-[20px] px-1.5 text-xs"
            >
              {item.count}
            </Badge>
          )}
        </>
      )}
      {!isExpanded && item.count !== undefined && item.count > 0 && (
        <Badge
          variant="default"
          className="absolute -right-1 -top-1 h-4 w-4 p-0 flex items-center justify-center text-[10px]"
        >
          {item.count}
        </Badge>
      )}
    </NavLink>
  )

  if (!isExpanded) {
    return (
      <Tooltip delayDuration={0}>
        <TooltipTrigger asChild>
          {content}
        </TooltipTrigger>
        <TooltipContent side="right" className="flex items-center gap-3">
          {item.label}
          {item.shortcut && (
            <kbd className="rounded border px-1.5 py-0.5 text-xs font-mono bg-muted">
              ⌘{item.shortcut}
            </kbd>
          )}
        </TooltipContent>
      </Tooltip>
    )
  }

  return content
}

// Shared mailbox item component with visual distinction
const SharedMailboxItemComponent = ({ 
  item, 
  isExpanded, 
  isActive,
  onClick 
}: { 
  item: SharedMailboxItem
  isExpanded: boolean
  isActive: boolean
  onClick: () => void
}) => {
  const content = (
    <button
      onClick={onClick}
      className={cn(
        "w-full flex items-center gap-3 rounded-lg px-3 py-2.5 text-sm font-medium transition-all duration-200 group relative",
        isActive
          ? "bg-purple-500/10 text-purple-600 dark:text-purple-400 shadow-sm"
          : "text-muted-foreground hover:bg-purple-500/5 hover:text-purple-600 dark:hover:text-purple-400"
      )}
    >
      <Mail
        className={cn(
          "h-5 w-5 shrink-0 transition-colors",
          isActive ? "text-purple-600 dark:text-purple-400" : "text-purple-400 group-hover:text-purple-500"
        )}
      />
      {isExpanded && (
        <>
          <span className="flex-1 text-left truncate">{item.mailbox}</span>
          <span className="text-xs text-muted-foreground truncate max-w-[80px]">
            {item.owner}
          </span>
        </>
      )}
    </button>
  )

  if (!isExpanded) {
    return (
      <Tooltip delayDuration={0}>
        <TooltipTrigger asChild>
          {content}
        </TooltipTrigger>
        <TooltipContent side="right" className="flex flex-col gap-1">
          <span className="font-medium">{item.mailbox}</span>
          <span className="text-xs text-muted-foreground">Shared: {item.owner}</span>
        </TooltipContent>
      </Tooltip>
    )
  }

  return content
}

export function Sidebar({ collapsed, onToggle, mobileOpen = false, onMobileClose }: SidebarProps) {
  const navigate = useNavigate()
  const location = useLocation()
  const [hovered, setHovered] = useState(false)
  const { user } = useAuth()
  const { currentMailbox, switchMailbox, loadSharedMailboxes, sharedMailboxes } = useMailbox()

  // Track expanded state for shared mailboxes section
  const [sharedExpanded, setSharedExpanded] = useState(true)

  // Real folder counts (no fake numbers): inbox unread + spam total.
  const [inboxUnread, setInboxUnread] = useState(0)
  const [spamCount, setSpamCount] = useState(0)
  // Real custom mailboxes (beyond the standard ones shown in the main nav).
  const [customFolders, setCustomFolders] = useState<string[]>([])

  // Load shared mailboxes on mount
  useEffect(() => {
    loadSharedMailboxes()
  }, [loadSharedMailboxes])

  // Folder management dialog state.
  const [folderDialogOpen, setFolderDialogOpen] = useState(false)
  const [folderDialogMode, setFolderDialogMode] = useState<"create" | "rename">("create")
  const [folderDialogCurrent, setFolderDialogCurrent] = useState("")
  const [folderDialogValue, setFolderDialogValue] = useState("")
  const [folderBusy, setFolderBusy] = useState(false)
  const [folderDeleteTarget, setFolderDeleteTarget] = useState<string | null>(null)

  // loadCustomFolders refreshes the dynamic folder list (also re-run after a
  // create/rename/delete so the sidebar reflects the change immediately).
  const loadCustomFolders = useCallback(async () => {
    try {
      const result = await api.getMailboxes()
      const extra = (result.mailboxes ?? []).filter(
        (m) => !standardMailboxes.has(m.toLowerCase())
      )
      setCustomFolders(extra)
    } catch {
      setCustomFolders([])
    }
  }, [])

  // Load real inbox/spam counts on mount
  useEffect(() => {
    let cancelled = false
    const loadCounts = async () => {
      try {
        const inbox = await api.getMail("inbox")
        if (!cancelled) {
          setInboxUnread((inbox.emails ?? []).filter((m) => !m.read).length)
        }
      } catch {
        if (!cancelled) setInboxUnread(0)
      }
      try {
        const spam = await api.getMail("spam")
        if (!cancelled) setSpamCount((spam.emails ?? []).length)
      } catch {
        if (!cancelled) setSpamCount(0)
      }
      await loadCustomFolders()
    }
    loadCounts()
    return () => {
      cancelled = true
    }
  }, [loadCustomFolders])

  const openCreateFolder = () => {
    setFolderDialogMode("create")
    setFolderDialogCurrent("")
    setFolderDialogValue("")
    setFolderDialogOpen(true)
  }

  const openRenameFolder = (name: string) => {
    setFolderDialogMode("rename")
    setFolderDialogCurrent(name)
    setFolderDialogValue(name)
    setFolderDialogOpen(true)
  }

  const submitFolderDialog = async () => {
    const value = folderDialogValue.trim()
    if (!value) {
      toast.error("Folder name is required")
      return
    }
    setFolderBusy(true)
    try {
      if (folderDialogMode === "create") {
        await api.createFolder(value)
        toast.success("Folder created")
      } else {
        await api.renameFolder(folderDialogCurrent, value)
        toast.success("Folder renamed")
      }
      setFolderDialogOpen(false)
      await loadCustomFolders()
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "Failed to save folder")
    } finally {
      setFolderBusy(false)
    }
  }

  const confirmDeleteFolder = async () => {
    if (!folderDeleteTarget || folderBusy) return
    setFolderBusy(true)
    try {
      await api.deleteFolder(folderDeleteTarget)
      toast.success("Folder deleted")
      if (location.pathname === `/folder/${encodeURIComponent(folderDeleteTarget)}`) {
        navigate("/inbox")
      }
      setFolderDeleteTarget(null)
      await loadCustomFolders()
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "Failed to delete folder")
    } finally {
      setFolderBusy(false)
    }
  }

  // Inject real counts into the nav items (badges only render when > 0).
  const mainNav = mainNavItems.map((item) =>
    item.path === "/inbox" ? { ...item, count: inboxUnread } : item
  )
  const folders: NavItem[] = folderItems.map((item) =>
    item.path === "/spam" ? { ...item, count: spamCount } : item
  )

  const isExpanded = !collapsed || hovered

  // Check if we're in a shared mailbox context
  const isInSharedContext = currentMailbox.type === 'shared'

  // Handle switching to a shared mailbox
  const handleSharedMailboxClick = (mb: SharedMailboxItem) => {
    // Navigate to the shared mailbox inbox with the context
    navigate(`/shared/${encodeURIComponent(mb.owner)}/inbox`)
    // Also update the mailbox context
    switchMailbox(mb.mailbox, mb.owner)
  }

  // Handle switching back to personal mailbox
  const handlePersonalMailboxClick = () => {
    if (user?.email) {
      navigate('/inbox')
      switchMailbox(user.email)
    }
  }

  return (
    <TooltipProvider>
    <aside
      className={cn(
        "fixed left-0 top-0 z-40 h-screen border-r bg-card transition-all duration-300 ease-in-out",
        isExpanded ? "w-64" : "w-16",
        // Hidden off-canvas on small screens unless toggled open; always shown on lg+.
        mobileOpen ? "translate-x-0" : "-translate-x-full lg:translate-x-0"
      )}
      onMouseEnter={() => collapsed && setHovered(true)}
      onMouseLeave={() => setHovered(false)}
    >
      {/* Logo Area */}
      <div className="flex h-16 items-center justify-between border-b px-4">
        <div className={cn("flex items-center gap-3", !isExpanded && "justify-center w-full")}>
          <div className="flex h-9 w-9 shrink-0 items-center justify-center rounded-lg bg-gradient-to-br from-primary to-primary/80 shadow-lg shadow-primary/25">
            <svg
              viewBox="0 0 24 24"
              className="h-5 w-5 text-primary-foreground"
              fill="none"
              stroke="currentColor"
              strokeWidth="2"
            >
              <path d="M3 8l7.89 5.26a2 2 0 002.22 0L21 8M5 19h14a2 2 0 002-2V7a2 2 0 00-2-2H5a2 2 0 00-2 2v10a2 2 0 002 2z" />
            </svg>
          </div>
          {isExpanded && (
            <span className="font-semibold text-lg tracking-tight">uMail</span>
          )}
        </div>
        {isExpanded && (
          <Button
            variant="ghost"
            size="icon"
            className="h-8 w-8"
            onClick={onToggle}
          >
            <ChevronLeft className="h-4 w-4" />
          </Button>
        )}
      </div>

      {/* Compose Button */}
      <div className="p-3">
        <Button
          className={cn(
            "w-full bg-gradient-to-r from-primary to-primary/90 hover:from-primary/90 hover:to-primary shadow-lg shadow-primary/25 transition-all",
            !isExpanded && "px-0 justify-center"
          )}
          size={isExpanded ? "default" : "icon"}
          onClick={() => navigate("/compose")}
        >
          <PenSquare className="h-4 w-4" />
          {isExpanded && <span className="ml-2">Compose</span>}
        </Button>
      </div>

      {/* Main Navigation */}
      <nav className="flex-1 space-y-1 px-2 py-2 overflow-y-auto" onClick={() => onMobileClose?.()}>
        {mainNav.map((item) => (
          <NavItemComponent key={item.path} item={item} isExpanded={isExpanded} />
        ))}

        {/* Shared Mailboxes Section - only show when user has shared mailboxes */}
        {sharedMailboxes.length > 0 && isExpanded && (
          <>
            <Separator className="my-3" />
            <button
              onClick={() => setSharedExpanded(!sharedExpanded)}
              className="flex items-center justify-between w-full px-3 py-2 text-xs font-semibold text-muted-foreground uppercase tracking-wider hover:text-foreground transition-colors"
            >
              <span className="flex items-center gap-2">
                <Mail className="h-4 w-4 text-purple-500" />
                Shared Mailboxes
              </span>
              {sharedExpanded ? (
                <ChevronUp className="h-4 w-4" />
              ) : (
                <ChevronDown className="h-4 w-4" />
              )}
            </button>
            
            {sharedExpanded && (
              <div className="space-y-1">
                {/* Personal mailbox entry when in shared context */}
                {isInSharedContext && (
                  <button
                    onClick={handlePersonalMailboxClick}
                    className="w-full flex items-center gap-3 rounded-lg px-3 py-2.5 text-sm font-medium transition-all duration-200 group bg-primary/5 hover:bg-primary/10 text-primary"
                  >
                    <Users className="h-5 w-5 shrink-0 text-primary" />
                    <span className="flex-1 text-left">My Mailbox</span>
                    <Badge variant="secondary" className="text-xs">Personal</Badge>
                  </button>
                )}
                
                {sharedMailboxes.map((mb) => (
                  <SharedMailboxItemComponent
                    key={`${mb.owner}:${mb.mailbox}`}
                    item={mb}
                    isExpanded={isExpanded}
                    isActive={isInSharedContext && currentMailbox.owner === mb.owner}
                    onClick={() => handleSharedMailboxClick(mb)}
                  />
                ))}
              </div>
            )}
          </>
        )}

        {/* Shared mailboxes in collapsed mode */}
        {sharedMailboxes.length > 0 && !isExpanded && (
          <div className="space-y-1 px-1">
            <Tooltip delayDuration={0}>
              <TooltipTrigger asChild>
                <button
                  onClick={() => setSharedExpanded(!sharedExpanded)}
                  className="w-full flex items-center justify-center rounded-lg p-2 text-purple-500 hover:bg-purple-500/10 transition-colors"
                >
                  <Mail className="h-5 w-5" />
                </button>
              </TooltipTrigger>
              <TooltipContent side="right">
                <span>{sharedMailboxes.length} shared mailbox{sharedMailboxes.length > 1 ? 'es' : ''}</span>
              </TooltipContent>
            </Tooltip>
          </div>
        )}

        <Separator className="my-3" />

        {isExpanded && (
          <div className="flex items-center justify-between px-3 pb-2">
            <p className="text-xs font-semibold text-muted-foreground uppercase tracking-wider">
              Folders
            </p>
            <Tooltip delayDuration={0}>
              <TooltipTrigger asChild>
                <button
                  onClick={openCreateFolder}
                  className="text-muted-foreground hover:text-foreground"
                  aria-label="New folder"
                >
                  <FolderPlus className="h-4 w-4" />
                </button>
              </TooltipTrigger>
              <TooltipContent side="right">New folder</TooltipContent>
            </Tooltip>
          </div>
        )}

        {folders.map((item) => (
          <NavItemComponent key={item.path} item={item} isExpanded={isExpanded} />
        ))}

        {customFolders.map((name) => {
          const path = `/folder/${encodeURIComponent(name)}`
          const isActive = location.pathname === path
          if (!isExpanded) {
            return (
              <NavItemComponent
                key={path}
                item={{ icon: FolderOpen, label: name, path }}
                isExpanded={isExpanded}
              />
            )
          }
          return (
            <div
              key={path}
              className={cn(
                "group flex items-center gap-1 rounded-lg pr-1 transition-all",
                isActive ? "bg-primary/10" : "hover:bg-accent"
              )}
            >
              <NavLink
                to={path}
                className={cn(
                  "flex flex-1 items-center gap-3 rounded-lg px-3 py-2.5 text-sm font-medium min-w-0",
                  isActive ? "text-primary" : "text-muted-foreground group-hover:text-accent-foreground"
                )}
              >
                <FolderOpen className="h-5 w-5 shrink-0" />
                <span className="flex-1 truncate">{name}</span>
              </NavLink>
              <DropdownMenu>
                <DropdownMenuTrigger asChild>
                  <button
                    className="opacity-0 group-hover:opacity-100 text-muted-foreground hover:text-foreground px-1"
                    aria-label={`Folder actions for ${name}`}
                  >
                    <MoreHorizontal className="h-4 w-4" />
                  </button>
                </DropdownMenuTrigger>
                <DropdownMenuContent align="end">
                  <DropdownMenuItem onClick={() => openRenameFolder(name)}>
                    <Pencil className="mr-2 h-4 w-4" />
                    Rename
                  </DropdownMenuItem>
                  <DropdownMenuItem
                    className="text-destructive"
                    onClick={() => setFolderDeleteTarget(name)}
                  >
                    <Trash2 className="mr-2 h-4 w-4" />
                    Delete
                  </DropdownMenuItem>
                </DropdownMenuContent>
              </DropdownMenu>
            </div>
          )
        })}
      </nav>

      {/* Create / rename folder dialog */}
      <Dialog open={folderDialogOpen} onOpenChange={setFolderDialogOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{folderDialogMode === "create" ? "New Folder" : "Rename Folder"}</DialogTitle>
            <DialogDescription>
              {folderDialogMode === "create"
                ? "Create a folder to organize your mail."
                : `Rename "${folderDialogCurrent}".`}
            </DialogDescription>
          </DialogHeader>
          <Input
            autoFocus
            value={folderDialogValue}
            onChange={(e) => setFolderDialogValue(e.target.value)}
            placeholder="Folder name"
            onKeyDown={(e) => {
              if (e.key === "Enter") void submitFolderDialog()
            }}
          />
          <DialogFooter>
            <Button variant="outline" onClick={() => setFolderDialogOpen(false)} disabled={folderBusy}>
              Cancel
            </Button>
            <Button onClick={submitFolderDialog} disabled={folderBusy}>
              {folderDialogMode === "create" ? "Create" : "Rename"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* Delete folder confirmation */}
      <Dialog open={folderDeleteTarget !== null} onOpenChange={(open) => { if (!open) setFolderDeleteTarget(null) }}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Delete Folder</DialogTitle>
            <DialogDescription>
              Delete "{folderDeleteTarget}"? Messages in this folder will be removed.
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="outline" onClick={() => setFolderDeleteTarget(null)} disabled={folderBusy}>
              Cancel
            </Button>
            <Button variant="destructive" onClick={confirmDeleteFolder} disabled={folderBusy}>
              <Trash2 className="mr-2 h-4 w-4" />
              Delete
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* Bottom Actions */}
      <div className="border-t p-2">
        <NavLink
          to="/settings"
          className={({ isActive }) =>
            cn(
              "flex items-center gap-3 rounded-lg px-3 py-2.5 text-sm font-medium transition-all duration-200 group",
              isActive
                ? "bg-primary/10 text-primary"
                : "text-muted-foreground hover:bg-accent hover:text-accent-foreground"
            )
          }
        >
          <Settings className="h-5 w-5 shrink-0" />
          {isExpanded && <span>Settings</span>}
        </NavLink>
      </div>

      {/* Collapse Toggle (when collapsed) */}
      {!isExpanded && (
        <Button
          variant="ghost"
          size="icon"
          className="absolute -right-3 top-20 h-6 w-6 rounded-full border bg-background shadow-md hover:bg-accent"
          onClick={onToggle}
        >
          <ChevronRight className="h-3 w-3" />
        </Button>
      )}
    </aside>
    </TooltipProvider>
  )
}
