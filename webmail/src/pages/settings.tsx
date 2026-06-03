import { useState, useEffect, useCallback, useRef } from "react"
import { Moon, Sun, Bell, Shield, Palette, Keyboard, Mail, Globe, Lock, Plane, Monitor, UserCog, Trash2, Plus, Tag, X, Camera } from "lucide-react"
import { useTheme } from "@/components/theme-provider"
import { useAuth } from "@/contexts/AuthContext"
import { Avatar, AvatarFallback, AvatarImage } from "@/components/ui/avatar"
import { Button } from "@/components/ui/button"
import { Separator } from "@/components/ui/separator"
import { Switch } from "@/components/ui/switch"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Textarea } from "@/components/ui/textarea"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { toast } from "sonner"
import api from "@/utils/api"
import type { VacationAutoReply, ClientSession, Delegation, Category } from "@/utils/api"
import { enablePushNotifications, disablePushNotifications, pushSupported } from "@/utils/push"

// rfc3339ToDate extracts the YYYY-MM-DD part from an RFC3339 string for <input type="date">.
function rfc3339ToDate(value?: string): string {
  if (!value) return ""
  return value.slice(0, 10)
}

// dateToRFC3339 turns a YYYY-MM-DD value into an RFC3339 UTC timestamp, or undefined when empty.
function dateToRFC3339(value: string): string | undefined {
  if (!value) return undefined
  return `${value}T00:00:00Z`
}

const emptyVacation: VacationAutoReply = {
  enabled: false,
  subject: "Out of Office",
  message: "",
}

export function SettingsPage() {
  const { theme, setTheme, resolvedTheme } = useTheme()
  const { user } = useAuth()

  // Profile photo (self-service avatar). avatarVersion cache-busts the <img>
  // after an upload/removal so the new photo shows immediately.
  const email = user?.email ?? ""
  const initials = (email ? email.slice(0, 2) : "?").toUpperCase()
  const fileInputRef = useRef<HTMLInputElement>(null)
  const [avatarVersion, setAvatarVersion] = useState(1)
  const [avatarBusy, setAvatarBusy] = useState(false)
  // Only request the avatar endpoint when the user actually has a photo,
  // otherwise the <img> 404s and spams the console. Tracks upload/removal.
  const [hasAvatar, setHasAvatar] = useState(!!user?.hasAvatar)

  const handlePickAvatar = async (file: File) => {
    if (!file.type.startsWith("image/")) {
      toast.error("Please choose an image file")
      return
    }
    if (file.size > 1024 * 1024) {
      toast.error("Image must be 1 MB or smaller")
      return
    }
    const dataURL = await new Promise<string>((resolve, reject) => {
      const reader = new FileReader()
      reader.onload = () => resolve(String(reader.result))
      reader.onerror = () => reject(reader.error)
      reader.readAsDataURL(file)
    })
    setAvatarBusy(true)
    try {
      await api.updateAvatar(dataURL)
      setHasAvatar(true)
      setAvatarVersion((v) => v + 1)
      toast.success("Profile photo updated")
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "Failed to update photo")
    } finally {
      setAvatarBusy(false)
    }
  }

  const handleRemoveAvatar = async () => {
    setAvatarBusy(true)
    try {
      await api.removeAvatar()
      setHasAvatar(false)
      setAvatarVersion((v) => v + 1)
      toast.success("Profile photo removed")
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "Failed to remove photo")
    } finally {
      setAvatarBusy(false)
    }
  }

  // Directory profile (display name, title, department, phone) — shown in the
  // GAL and on Outlook contact cards. Self-service via /api/v1/profile.
  const [profile, setProfile] = useState({ display_name: "", title: "", department: "", phone: "" })
  const [profileBusy, setProfileBusy] = useState(false)

  useEffect(() => {
    api.getProfile()
      .then((p) =>
        setProfile({
          display_name: p.display_name ?? "",
          title: p.title ?? "",
          department: p.department ?? "",
          phone: p.phone ?? "",
        })
      )
      .catch(() => undefined)
  }, [])

  const handleSaveProfile = async () => {
    setProfileBusy(true)
    try {
      await api.updateProfile(profile)
      toast.success("Profile updated")
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "Failed to update profile")
    } finally {
      setProfileBusy(false)
    }
  }

  // Password change dialog (Manage Account)
  const [pwOpen, setPwOpen] = useState(false)
  const [pwCurrent, setPwCurrent] = useState("")
  const [pwNew, setPwNew] = useState("")
  const [pwConfirm, setPwConfirm] = useState("")
  const [pwSaving, setPwSaving] = useState(false)

  // Active sessions
  const [sessions, setSessions] = useState<ClientSession[]>([])

  const loadSessions = useCallback(async () => {
    try {
      const res = await api.getSessions()
      setSessions(res.sessions ?? [])
    } catch (err) {
      console.error("Failed to load sessions:", err)
      setSessions([])
    }
  }, [])

  useEffect(() => {
    loadSessions()
  }, [loadSessions])

  const handleRevokeSession = async (id: string) => {
    try {
      await api.revokeSession(id)
      toast.success("Session revoked")
      setSessions((prev) => prev.filter((s) => s.id !== id))
    } catch (err) {
      console.error("Failed to revoke session:", err)
      toast.error("Failed to revoke session")
    }
  }

  const [pushBusy, setPushBusy] = useState(false)

  const handleEnablePush = async () => {
    setPushBusy(true)
    try {
      await enablePushNotifications()
      toast.success("Push notifications enabled")
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "Failed to enable push notifications")
    } finally {
      setPushBusy(false)
    }
  }

  const handleDisablePush = async () => {
    setPushBusy(true)
    try {
      await disablePushNotifications()
      toast.success("Push notifications disabled")
    } catch {
      toast.error("Failed to disable push notifications")
    } finally {
      setPushBusy(false)
    }
  }

  const handleChangePassword = async () => {
    if (pwNew.length < 8) {
      toast.error("New password must be at least 8 characters")
      return
    }
    if (pwNew !== pwConfirm) {
      toast.error("Passwords do not match")
      return
    }
    setPwSaving(true)
    try {
      await api.changePassword(pwCurrent, pwNew)
      toast.success("Password updated")
      setPwOpen(false)
      setPwCurrent("")
      setPwNew("")
      setPwConfirm("")
    } catch (err) {
      console.error("Failed to change password:", err)
      toast.error("Failed to change password. Check your current password.")
    } finally {
      setPwSaving(false)
    }
  }
  const [settings, setSettings] = useState({
    // Notifications
    emailNotifications: true,
    browserNotifications: false,
    soundNotifications: true,
    desktopNotifications: true,
    // Email
    autoSaveDraft: true,
    readReceipts: false,
    deliveryReceipts: true,
    // Privacy
    showOnlineStatus: false,
    allowReadReceipts: true,
    // Composition
    richTextMode: true,
    autoCorrect: true,
    spellCheck: true,
  })

  // Load persisted preferences on mount and merge over the defaults.
  useEffect(() => {
    let cancelled = false
    api.getPreferences()
      .then((res) => {
        if (cancelled || !res.preferences) return
        setSettings((prev) => ({ ...prev, ...res.preferences }))
      })
      .catch(() => {
        // keep defaults
      })
    return () => {
      cancelled = true
    }
  }, [])

  const handleToggle = async (key: keyof typeof settings) => {
    const next = { ...settings, [key]: !settings[key] }
    setSettings(next)
    try {
      await api.setPreferences(next)
      toast.success("Setting updated")
    } catch (err) {
      console.error("Failed to save setting:", err)
      toast.error("Failed to save setting")
      setSettings(settings) // revert
    }
  }

  // Vacation / Out-of-Office auto-reply (backed by /api/v1/vacation).
  const [vacation, setVacation] = useState<VacationAutoReply>(emptyVacation)
  const [vacationLoading, setVacationLoading] = useState(true)
  const [vacationSaving, setVacationSaving] = useState(false)

  const loadVacation = useCallback(async () => {
    setVacationLoading(true)
    try {
      const cfg = await api.getVacation()
      setVacation({ ...emptyVacation, ...cfg })
    } catch {
      setVacation(emptyVacation)
    } finally {
      setVacationLoading(false)
    }
  }, [])

  useEffect(() => {
    loadVacation()
  }, [loadVacation])

  const handleVacationSave = async () => {
    if (vacation.enabled && !vacation.subject.trim()) {
      toast.error("Subject is required when auto-reply is enabled")
      return
    }
    if (vacation.enabled && !vacation.message.trim()) {
      toast.error("Message is required when auto-reply is enabled")
      return
    }
    setVacationSaving(true)
    try {
      await api.setVacation(vacation)
      toast.success("Auto-reply saved")
      await loadVacation()
    } catch {
      toast.error("Failed to save auto-reply")
    } finally {
      setVacationSaving(false)
    }
  }

  const handleVacationDisable = async () => {
    setVacationSaving(true)
    try {
      await api.deleteVacation()
      toast.success("Auto-reply disabled")
      await loadVacation()
    } catch {
      toast.error("Failed to disable auto-reply")
    } finally {
      setVacationSaving(false)
    }
  }

  // Outgoing-mail signature (backed by /api/v1/signature).
  const [signature, setSignature] = useState("")
  const [signatureSaving, setSignatureSaving] = useState(false)

  useEffect(() => {
    let cancelled = false
    api.getSignature()
      .then((res) => {
        if (!cancelled) setSignature(res.signature ?? "")
      })
      .catch(() => {
        // keep empty
      })
    return () => {
      cancelled = true
    }
  }, [])

  // Categories: named, colored labels the user can apply to messages.
  const [categories, setCategories] = useState<Category[]>([])
  const [catName, setCatName] = useState("")
  const [catColor, setCatColor] = useState("#ef4444")
  const [catBusy, setCatBusy] = useState(false)

  useEffect(() => {
    let cancelled = false
    api.getCategories()
      .then((res) => { if (!cancelled) setCategories(res.categories ?? []) })
      .catch(() => {})
    return () => { cancelled = true }
  }, [])

  const saveCategories = async (next: Category[]) => {
    const prev = categories
    setCategories(next)
    setCatBusy(true)
    try {
      const res = await api.setCategories(next)
      setCategories(res.categories ?? next)
    } catch {
      setCategories(prev)
      toast.error("Failed to save categories")
    } finally {
      setCatBusy(false)
    }
  }

  const addCategory = () => {
    const name = catName.trim()
    if (!name) return
    if (categories.some((c) => c.name.toLowerCase() === name.toLowerCase())) {
      toast.error("That category already exists")
      return
    }
    setCatName("")
    void saveCategories([...categories, { name, color: catColor }])
  }

  const removeCategory = (name: string) => void saveCategories(categories.filter((c) => c.name !== name))

  const handleSignatureSave = async () => {
    setSignatureSaving(true)
    try {
      await api.setSignature(signature)
      toast.success("Signature saved")
    } catch {
      toast.error("Failed to save signature")
    } finally {
      setSignatureSaving(false)
    }
  }

  // Delegates: people the user grants access to their own mailbox.
  const [delegations, setDelegations] = useState<Delegation[]>([])
  const [delEmail, setDelEmail] = useState("")
  const [delWrite, setDelWrite] = useState(false)
  const [delSendOnBehalf, setDelSendOnBehalf] = useState(false)
  const [delBusy, setDelBusy] = useState(false)

  const loadDelegations = useCallback(async () => {
    try {
      const res = await api.getDelegations()
      setDelegations(res.delegations ?? [])
    } catch {
      setDelegations([])
    }
  }, [])

  useEffect(() => {
    loadDelegations()
  }, [loadDelegations])

  const handleAddDelegate = async () => {
    const grantee = delEmail.trim().toLowerCase()
    if (!grantee) {
      toast.error("Delegate email is required")
      return
    }
    setDelBusy(true)
    try {
      await api.createDelegation({
        grantee,
        rights: delWrite ? ["read", "write"] : ["read"],
        canSendOnBehalf: delSendOnBehalf,
      })
      toast.success("Delegate added")
      setDelEmail("")
      setDelWrite(false)
      setDelSendOnBehalf(false)
      await loadDelegations()
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "Failed to add delegate")
    } finally {
      setDelBusy(false)
    }
  }

  const handleRemoveDelegate = async (id: string) => {
    setDelBusy(true)
    try {
      await api.deleteDelegation(id)
      toast.success("Delegate removed")
      await loadDelegations()
    } catch {
      toast.error("Failed to remove delegate")
    } finally {
      setDelBusy(false)
    }
  }

  const SettingSection = ({
    icon: Icon,
    title,
    description,
    children
  }: {
    icon: React.ElementType
    title: string
    description: string
    children: React.ReactNode
  }) => (
    <div className="rounded-lg border bg-card">
      <div className="flex items-center gap-4 p-6 pb-4">
        <div className="rounded-full bg-muted p-2">
          <Icon className="h-5 w-5" />
        </div>
        <div>
          <h3 className="font-semibold">{title}</h3>
          <p className="text-sm text-muted-foreground">{description}</p>
        </div>
      </div>
      <div className="px-6 pb-6">
        {children}
      </div>
    </div>
  )

  const SettingRow = ({
    title,
    description,
    checked,
    onChange
  }: {
    title: string
    description: string
    checked: boolean
    onChange: () => void
  }) => (
    <div className="flex items-center justify-between py-3">
      <div>
        <p className="font-medium">{title}</p>
        <p className="text-sm text-muted-foreground">{description}</p>
      </div>
      <Switch checked={checked} onCheckedChange={onChange} />
    </div>
  )

  return (
    <div className="space-y-6 max-w-3xl">
      <div>
        <h2 className="text-2xl font-bold">Settings</h2>
        <p className="text-muted-foreground">
          Manage your email preferences and account settings.
        </p>
      </div>

      {/* Profile photo */}
      <SettingSection
        icon={Camera}
        title="Profile photo"
        description="Set the photo shown across uMail and the directory"
      >
        <div className="flex items-center gap-4">
          <Avatar className="h-16 w-16 ring-2 ring-primary/20">
            <AvatarImage src={hasAvatar && email ? api.avatarUrl(email, avatarVersion) : ""} alt={email} />
            <AvatarFallback className="bg-gradient-to-br from-primary to-primary/80 text-primary-foreground text-lg font-semibold">
              {initials}
            </AvatarFallback>
          </Avatar>
          <div className="flex flex-col gap-2 sm:flex-row sm:items-center">
            <input
              ref={fileInputRef}
              type="file"
              accept="image/png,image/jpeg,image/gif,image/webp"
              className="hidden"
              onChange={(e) => {
                const file = e.target.files?.[0]
                if (file) void handlePickAvatar(file)
                e.target.value = ""
              }}
            />
            <Button variant="outline" disabled={avatarBusy} onClick={() => fileInputRef.current?.click()}>
              <Camera className="mr-2 h-4 w-4" />
              {avatarBusy ? "Saving…" : "Upload photo"}
            </Button>
            <Button variant="ghost" disabled={avatarBusy} onClick={handleRemoveAvatar}>
              <Trash2 className="mr-2 h-4 w-4" />
              Remove
            </Button>
          </div>
        </div>
        <p className="mt-3 text-xs text-muted-foreground">PNG, JPG, GIF or WebP up to 1 MB.</p>
      </SettingSection>

      {/* Directory profile */}
      <SettingSection
        icon={UserCog}
        title="Profile"
        description="Your name and contact details shown in the directory and to Outlook"
      >
        <div className="space-y-4">
          <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
            <div className="space-y-2">
              <Label htmlFor="profile-display-name">Display name</Label>
              <Input
                id="profile-display-name"
                value={profile.display_name}
                onChange={(e) => setProfile({ ...profile, display_name: e.target.value })}
                placeholder="Jane Doe"
              />
            </div>
            <div className="space-y-2">
              <Label htmlFor="profile-title">Title</Label>
              <Input
                id="profile-title"
                value={profile.title}
                onChange={(e) => setProfile({ ...profile, title: e.target.value })}
                placeholder="Engineer"
              />
            </div>
            <div className="space-y-2">
              <Label htmlFor="profile-department">Department</Label>
              <Input
                id="profile-department"
                value={profile.department}
                onChange={(e) => setProfile({ ...profile, department: e.target.value })}
                placeholder="Sales"
              />
            </div>
            <div className="space-y-2">
              <Label htmlFor="profile-phone">Phone</Label>
              <Input
                id="profile-phone"
                value={profile.phone}
                onChange={(e) => setProfile({ ...profile, phone: e.target.value })}
                placeholder="+1 555 0100"
              />
            </div>
          </div>
          <Button onClick={handleSaveProfile} disabled={profileBusy}>
            {profileBusy ? "Saving…" : "Save profile"}
          </Button>
        </div>
      </SettingSection>

      {/* Appearance */}
      <SettingSection
        icon={Palette}
        title="Appearance"
        description="Customize how uMail looks on your device"
      >
        <div className="space-y-4">
          <div className="flex items-center justify-between">
            <div>
              <p className="font-medium">Theme</p>
              <p className="text-sm text-muted-foreground">Choose your preferred color scheme</p>
            </div>
            <div className="flex gap-2">
              <Button
                variant={theme === "light" ? "default" : "outline"}
                size="icon"
                onClick={() => setTheme("light")}
                title="Light mode"
              >
                <Sun className="h-4 w-4" />
              </Button>
              <Button
                variant={theme === "dark" ? "default" : "outline"}
                size="icon"
                onClick={() => setTheme("dark")}
                title="Dark mode"
              >
                <Moon className="h-4 w-4" />
              </Button>
              <Button
                variant={theme === "system" ? "default" : "outline"}
                size="icon"
                onClick={() => setTheme("system")}
                title="System default"
              >
                <Globe className="h-4 w-4" />
              </Button>
            </div>
          </div>
          <Separator />
          <SettingRow
            title="Dark mode"
            description="Use dark theme"
            checked={theme === "dark"}
            onChange={() => setTheme(resolvedTheme === "dark" ? "light" : "dark")}
          />
        </div>
      </SettingSection>

      {/* Notifications */}
      <SettingSection
        icon={Bell}
        title="Notifications"
        description="Configure how you receive alerts for new messages"
      >
        <div className="space-y-1">
          <SettingRow
            title="Email notifications"
            description="Receive notifications for new emails"
            checked={settings.emailNotifications}
            onChange={() => handleToggle("emailNotifications")}
          />
          <Separator />
          <SettingRow
            title="Browser notifications"
            description="Show notifications in your browser"
            checked={settings.browserNotifications}
            onChange={() => handleToggle("browserNotifications")}
          />
          <Separator />
          <SettingRow
            title="Sound notifications"
            description="Play a sound for new messages"
            checked={settings.soundNotifications}
            onChange={() => handleToggle("soundNotifications")}
          />
          <Separator />
          <SettingRow
            title="Desktop notifications"
            description="Show desktop notifications when app is in background"
            checked={settings.desktopNotifications}
            onChange={() => handleToggle("desktopNotifications")}
          />
        </div>
      </SettingSection>

      {/* Email Settings */}
      <SettingSection
        icon={Mail}
        title="Email Composition"
        description="Settings for composing and sending emails"
      >
        <div className="space-y-1">
          <SettingRow
            title="Auto-save drafts"
            description="Automatically save drafts while composing"
            checked={settings.autoSaveDraft}
            onChange={() => handleToggle("autoSaveDraft")}
          />
          <Separator />
          <SettingRow
            title="Rich text mode"
            description="Use rich text editor with formatting"
            checked={settings.richTextMode}
            onChange={() => handleToggle("richTextMode")}
          />
          <Separator />
          <SettingRow
            title="Auto-correct"
            description="Automatically correct spelling"
            checked={settings.autoCorrect}
            onChange={() => handleToggle("autoCorrect")}
          />
          <Separator />
          <SettingRow
            title="Spell check"
            description="Check spelling while typing"
            checked={settings.spellCheck}
            onChange={() => handleToggle("spellCheck")}
          />
        </div>
      </SettingSection>

      {/* Auto-Reply (Out of Office) */}
      <SettingSection
        icon={Plane}
        title="Auto-Reply (Out of Office)"
        description="Automatically reply to incoming mail while you are away"
      >
        {vacationLoading ? (
          <p className="text-sm text-muted-foreground py-3">Loading…</p>
        ) : (
          <div className="space-y-4">
            <SettingRow
              title="Enable auto-reply"
              description="Send an automatic reply to people who email you"
              checked={vacation.enabled}
              onChange={() => setVacation({ ...vacation, enabled: !vacation.enabled })}
            />
            <Separator />
            <div className="space-y-2">
              <Label htmlFor="vacation-subject">Subject</Label>
              <Input
                id="vacation-subject"
                value={vacation.subject}
                onChange={(e) => setVacation({ ...vacation, subject: e.target.value })}
                placeholder="Out of Office"
                disabled={!vacation.enabled}
              />
            </div>
            <div className="space-y-2">
              <Label htmlFor="vacation-message">Message</Label>
              <Textarea
                id="vacation-message"
                value={vacation.message}
                onChange={(e) => setVacation({ ...vacation, message: e.target.value })}
                placeholder="I am currently out of office and will respond when I return."
                rows={4}
                disabled={!vacation.enabled}
              />
            </div>
            <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
              <div className="space-y-2">
                <Label htmlFor="vacation-start">Start date (optional)</Label>
                <Input
                  id="vacation-start"
                  type="date"
                  value={rfc3339ToDate(vacation.start_date)}
                  onChange={(e) =>
                    setVacation({ ...vacation, start_date: dateToRFC3339(e.target.value) })
                  }
                  disabled={!vacation.enabled}
                />
              </div>
              <div className="space-y-2">
                <Label htmlFor="vacation-end">End date (optional)</Label>
                <Input
                  id="vacation-end"
                  type="date"
                  value={rfc3339ToDate(vacation.end_date)}
                  onChange={(e) =>
                    setVacation({ ...vacation, end_date: dateToRFC3339(e.target.value) })
                  }
                  disabled={!vacation.enabled}
                />
              </div>
            </div>
            <div className="flex items-center gap-2">
              <Button onClick={handleVacationSave} disabled={vacationSaving}>
                Save
              </Button>
              <Button
                variant="outline"
                onClick={handleVacationDisable}
                disabled={vacationSaving}
              >
                Disable
              </Button>
            </div>
          </div>
        )}
      </SettingSection>

      {/* Signature */}
      <SettingSection
        icon={Mail}
        title="Signature"
        description="Appended to new messages you compose"
      >
        <div className="space-y-3">
          <Textarea
            id="signature"
            value={signature}
            onChange={(e) => setSignature(e.target.value)}
            placeholder={"Best regards,\nYour Name"}
            rows={4}
          />
          <Button onClick={handleSignatureSave} disabled={signatureSaving}>
            Save
          </Button>
        </div>
      </SettingSection>

      {/* Categories */}
      <SettingSection
        icon={Tag}
        title="Categories"
        description="Color-coded labels you can apply to messages"
      >
        <div className="space-y-3">
          <div className="flex flex-wrap items-center gap-2">
            {categories.length === 0 ? (
              <p className="text-sm text-muted-foreground">No categories yet.</p>
            ) : (
              categories.map((c) => (
                <span
                  key={c.name}
                  className="inline-flex items-center gap-1 rounded-full px-2.5 py-0.5 text-xs font-medium text-white"
                  style={{ backgroundColor: c.color || "#64748b" }}
                >
                  {c.name}
                  <button
                    onClick={() => removeCategory(c.name)}
                    aria-label={`Remove ${c.name}`}
                    className="opacity-80 hover:opacity-100"
                  >
                    <X className="h-3 w-3" />
                  </button>
                </span>
              ))
            )}
          </div>
          <div className="flex items-center gap-2">
            <input
              type="color"
              value={catColor}
              onChange={(e) => setCatColor(e.target.value)}
              className="h-9 w-12 cursor-pointer rounded border bg-transparent"
              aria-label="Category color"
            />
            <Input
              value={catName}
              onChange={(e) => setCatName(e.target.value)}
              onKeyDown={(e) => { if (e.key === "Enter") addCategory() }}
              placeholder="Category name"
            />
            <Button onClick={addCategory} disabled={catBusy || !catName.trim()}>
              <Plus className="mr-1 h-4 w-4" />
              Add
            </Button>
          </div>
        </div>
      </SettingSection>

      {/* Delegates */}
      <SettingSection
        icon={UserCog}
        title="Delegates"
        description="Let other people access your mailbox and send on your behalf"
      >
        <div className="space-y-4">
          {delegations.length > 0 && (
            <div className="space-y-2">
              {delegations.map((d) => (
                <div key={d.id} className="flex items-center justify-between rounded-lg border p-3">
                  <div className="min-w-0">
                    <p className="font-medium truncate">{d.grantee}</p>
                    <p className="text-xs text-muted-foreground">
                      {d.rights || "no access"}
                      {d.canSendOnBehalf ? " · send on behalf" : ""}
                      {d.canSendAs ? " · send as" : ""}
                    </p>
                  </div>
                  <Button
                    variant="ghost"
                    size="icon"
                    className="h-8 w-8 text-destructive"
                    onClick={() => handleRemoveDelegate(d.id)}
                    disabled={delBusy}
                  >
                    <Trash2 className="h-4 w-4" />
                  </Button>
                </div>
              ))}
            </div>
          )}
          <div className="space-y-3 rounded-lg border p-3">
            <div className="space-y-2">
              <Label htmlFor="delegate-email">Add a delegate</Label>
              <Input
                id="delegate-email"
                type="email"
                value={delEmail}
                onChange={(e) => setDelEmail(e.target.value)}
                placeholder="colleague@example.com"
              />
            </div>
            <SettingRow
              title="Allow editing"
              description="Delegate can change items, not just read them"
              checked={delWrite}
              onChange={() => setDelWrite((v) => !v)}
            />
            <SettingRow
              title="Send on behalf"
              description="Delegate can send mail on your behalf"
              checked={delSendOnBehalf}
              onChange={() => setDelSendOnBehalf((v) => !v)}
            />
            <Button onClick={handleAddDelegate} disabled={delBusy || !delEmail.trim()}>
              <Plus className="mr-2 h-4 w-4" />
              Add delegate
            </Button>
          </div>
        </div>
      </SettingSection>

      {/* Privacy & Security */}
      <SettingSection
        icon={Shield}
        title="Privacy & Security"
        description="Control your privacy and security settings"
      >
        <div className="space-y-1">
          <SettingRow
            title="Read receipts"
            description="Request read receipts for sent emails"
            checked={settings.readReceipts}
            onChange={() => handleToggle("readReceipts")}
          />
          <Separator />
          <SettingRow
            title="Delivery receipts"
            description="Request delivery confirmations for sent emails"
            checked={settings.deliveryReceipts}
            onChange={() => handleToggle("deliveryReceipts")}
          />
          <Separator />
          <SettingRow
            title="Show online status"
            description="Let others see when you're online"
            checked={settings.showOnlineStatus}
            onChange={() => handleToggle("showOnlineStatus")}
          />
          <Separator />
          <SettingRow
            title="Allow read receipts"
            description="Send read receipts when others request them"
            checked={settings.allowReadReceipts}
            onChange={() => handleToggle("allowReadReceipts")}
          />
        </div>
      </SettingSection>

      {/* Keyboard Shortcuts */}
      <SettingSection
        icon={Keyboard}
        title="Keyboard Shortcuts"
        description="View and manage keyboard shortcuts"
      >
        <div className="space-y-3">
          <p className="text-sm text-muted-foreground">
            Keyboard shortcuts help you navigate and perform actions faster.
          </p>
          <Button
            variant="outline"
            onClick={() => document.dispatchEvent(new CustomEvent("toggle-shortcuts"))}
          >
            View Keyboard Shortcuts
          </Button>
        </div>
      </SettingSection>

      {/* Push Notifications */}
      <SettingSection
        icon={Bell}
        title="Push Notifications"
        description="Get desktop push notifications for new mail"
      >
        <div className="flex flex-wrap items-center gap-2">
          <Button onClick={handleEnablePush} disabled={pushBusy || !pushSupported()}>
            {pushBusy ? "Working..." : "Enable push notifications"}
          </Button>
          <Button variant="outline" onClick={handleDisablePush} disabled={pushBusy || !pushSupported()}>
            Disable
          </Button>
          {!pushSupported() && (
            <span className="text-sm text-muted-foreground">
              Not supported in this browser.
            </span>
          )}
        </div>
      </SettingSection>

      {/* Active Sessions */}
      <SettingSection
        icon={Monitor}
        title="Active Sessions"
        description="Devices currently signed in to your account"
      >
        {sessions.length === 0 ? (
          <p className="text-sm text-muted-foreground">No active sessions found.</p>
        ) : (
          <div className="space-y-2">
            {sessions.map((s) => (
              <div key={s.id} className="flex items-center justify-between gap-4 rounded-lg border p-3">
                <div className="min-w-0">
                  <p className="text-sm font-medium truncate">
                    {s.device_type || "Unknown device"} · {s.client_ip || "unknown IP"}
                  </p>
                  <p className="text-xs text-muted-foreground truncate">
                    {s.user_agent || "—"}
                  </p>
                  <p className="text-xs text-muted-foreground">Last active: {s.last_active}</p>
                </div>
                <Button variant="outline" size="sm" onClick={() => handleRevokeSession(s.id)}>
                  Revoke
                </Button>
              </div>
            ))}
          </div>
        )}
      </SettingSection>

      {/* Account */}
      <div className="rounded-lg border bg-card p-6">
        <div className="flex items-center justify-between">
          <div className="flex items-center gap-4">
            <div className="rounded-full bg-destructive/10 p-2">
              <Lock className="h-5 w-5 text-destructive" />
            </div>
            <div>
              <h3 className="font-semibold">Account Security</h3>
              <p className="text-sm text-muted-foreground">
                Manage your password and security settings
              </p>
            </div>
          </div>
          <Button variant="outline" onClick={() => setPwOpen(true)}>Manage Account</Button>
        </div>
      </div>

      <Dialog open={pwOpen} onOpenChange={setPwOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Change password</DialogTitle>
            <DialogDescription>
              Update the password for your mailbox account.
            </DialogDescription>
          </DialogHeader>
          <div className="space-y-3">
            <div className="space-y-1">
              <Label htmlFor="pw-current">Current password</Label>
              <Input
                id="pw-current"
                type="password"
                value={pwCurrent}
                onChange={(e) => setPwCurrent(e.target.value)}
                autoComplete="current-password"
              />
            </div>
            <div className="space-y-1">
              <Label htmlFor="pw-new">New password</Label>
              <Input
                id="pw-new"
                type="password"
                value={pwNew}
                onChange={(e) => setPwNew(e.target.value)}
                autoComplete="new-password"
              />
            </div>
            <div className="space-y-1">
              <Label htmlFor="pw-confirm">Confirm new password</Label>
              <Input
                id="pw-confirm"
                type="password"
                value={pwConfirm}
                onChange={(e) => setPwConfirm(e.target.value)}
                autoComplete="new-password"
              />
            </div>
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={() => setPwOpen(false)} disabled={pwSaving}>
              Cancel
            </Button>
            <Button onClick={handleChangePassword} disabled={pwSaving}>
              {pwSaving ? "Saving..." : "Update password"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <div className="text-center text-sm text-muted-foreground pb-8">
        <p>uMail Server v1.0.0</p>
        <p className="mt-1">Built with care for your privacy</p>
      </div>
    </div>
  )
}
