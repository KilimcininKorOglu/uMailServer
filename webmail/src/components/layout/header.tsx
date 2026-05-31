import { useState, useEffect } from "react"
import { useNavigate } from "react-router-dom"
import { Search, Bell, Sun, Moon, Menu, User, LogOut, ChevronDown, Keyboard } from "lucide-react"
import { useTheme } from "@/components/theme-provider"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { Avatar, AvatarFallback, AvatarImage } from "@/components/ui/avatar"
import { Badge } from "@/components/ui/badge"
import { cn } from "@/lib/utils"
import { useAuth } from "@/contexts/AuthContext"
import api from "@/utils/api"

interface Notification {
  id: string
  from: string
  subject: string
  date: string
}

interface HeaderProps {
  onMenuToggle: () => void
  sidebarCollapsed: boolean
}

export function Header({ onMenuToggle, sidebarCollapsed }: HeaderProps) {
  const { setTheme, resolvedTheme } = useTheme()
  const { logout, user } = useAuth()
  const navigate = useNavigate()
  const [searchQuery, setSearchQuery] = useState("")
  const [notifications, setNotifications] = useState<Notification[]>([])

  // Surface unread inbox messages as notifications (no fake data).
  useEffect(() => {
    let cancelled = false
    api.getMail("inbox")
      .then((res) => {
        if (cancelled) return
        const unread = (res.emails ?? [])
          .filter((m) => !m.read)
          .slice(0, 5)
          .map((m) => ({ id: m.id, from: m.from, subject: m.subject, date: m.date }))
        setNotifications(unread)
      })
      .catch(() => {
        if (!cancelled) setNotifications([])
      })
    return () => {
      cancelled = true
    }
  }, [])

  const email = user?.email ?? ""
  const displayName = email ? email.split("@")[0] : "Account"
  const initials = (email ? email.slice(0, 2) : "?").toUpperCase()

  const handleSignOut = async () => {
    await logout()
    navigate("/login")
  }

  const handleSearch = (e: React.FormEvent) => {
    e.preventDefault()
    if (searchQuery.trim()) {
      navigate(`/search?q=${encodeURIComponent(searchQuery.trim())}`)
      setSearchQuery("")
    }
  }

  return (
    <header
      className={cn(
        "fixed top-0 right-0 z-30 h-16 border-b bg-background/95 backdrop-blur supports-[backdrop-filter]:bg-background/60 transition-all duration-300 left-0",
        sidebarCollapsed ? "lg:left-16" : "lg:left-64"
      )}
    >
      <div className="flex h-full items-center justify-between gap-4 px-4 lg:px-6">
        {/* Left: Menu Toggle & Search */}
        <div className="flex items-center gap-4 flex-1">
          <Button
            variant="ghost"
            size="icon"
            className="lg:hidden"
            onClick={onMenuToggle}
          >
            <Menu className="h-5 w-5" />
          </Button>

          <form onSubmit={handleSearch} className="relative max-w-md flex-1 hidden md:block">
            <Search className="absolute left-3 top-1/2 h-4 w-4 -translate-y-1/2 text-muted-foreground" />
            <Input
              type="search"
              placeholder="Search emails..."
              className="pl-10 bg-muted/50 border-0 focus:bg-background focus:ring-2 focus:ring-primary/20"
              value={searchQuery}
              onChange={(e) => setSearchQuery(e.target.value)}
            />
          </form>
        </div>

        {/* Right: Actions */}
        <div className="flex items-center gap-2">
          {/* Theme Toggle */}
          <Button
            variant="ghost"
            size="icon"
            className="relative"
            onClick={() => setTheme(resolvedTheme === "dark" ? "light" : "dark")}
          >
            {resolvedTheme === "dark" ? (
              <Sun className="h-5 w-5" />
            ) : (
              <Moon className="h-5 w-5" />
            )}
          </Button>

          {/* Keyboard Shortcuts */}
          <Button
            variant="ghost"
            size="icon"
            className="relative"
            onClick={() => document.dispatchEvent(new CustomEvent("toggle-shortcuts"))}
            title="Keyboard shortcuts (?)"
          >
            <Keyboard className="h-5 w-5" />
          </Button>

          {/* Notifications */}
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <Button variant="ghost" size="icon" className="relative">
                <Bell className="h-5 w-5" />
                {notifications.length > 0 && (
                  <Badge className="absolute -right-1 -top-1 h-5 w-5 p-0 flex items-center justify-center text-[10px]">
                    {notifications.length}
                  </Badge>
                )}
              </Button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end" className="w-80">
              <DropdownMenuLabel>Notifications</DropdownMenuLabel>
              <DropdownMenuSeparator />
              <div className="max-h-80 overflow-y-auto">
                {notifications.length === 0 ? (
                  <div className="p-4 text-center text-sm text-muted-foreground">
                    No new notifications
                  </div>
                ) : (
                  notifications.map((n) => (
                    <DropdownMenuItem
                      key={n.id}
                      className="flex flex-col items-start gap-1 p-3 cursor-pointer"
                      onClick={() => navigate(`/email/${n.id}`)}
                    >
                      <span className="font-medium text-sm truncate w-full">{n.subject || "(no subject)"}</span>
                      <span className="text-xs text-muted-foreground truncate w-full">From: {n.from}</span>
                      <span className="text-xs text-muted-foreground">{n.date}</span>
                    </DropdownMenuItem>
                  ))
                )}
              </div>
            </DropdownMenuContent>
          </DropdownMenu>

          {/* User Profile */}
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <Button variant="ghost" className="relative h-9 w-9 rounded-full">
                <Avatar className="h-9 w-9 ring-2 ring-primary/20">
                  <AvatarImage src="" alt={email} />
                  <AvatarFallback className="bg-gradient-to-br from-primary to-primary/80 text-primary-foreground font-semibold">
                    {initials}
                  </AvatarFallback>
                </Avatar>
              </Button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end" className="w-56">
              <DropdownMenuLabel>
                <div className="flex flex-col">
                  <span className="font-semibold">{displayName}</span>
                  <span className="text-xs text-muted-foreground">{email}</span>
                </div>
              </DropdownMenuLabel>
              <DropdownMenuSeparator />
              <DropdownMenuItem onClick={() => navigate("/settings")}>
                <User className="mr-2 h-4 w-4" />
                Profile
              </DropdownMenuItem>
              <DropdownMenuItem onClick={() => navigate("/settings")}>
                <ChevronDown className="mr-2 h-4 w-4" />
                Account Settings
              </DropdownMenuItem>
              <DropdownMenuSeparator />
              <DropdownMenuItem className="text-destructive" onClick={handleSignOut}>
                <LogOut className="mr-2 h-4 w-4" />
                Sign Out
              </DropdownMenuItem>
            </DropdownMenuContent>
          </DropdownMenu>
        </div>
      </div>
    </header>
  )
}
