import { useState, useEffect, useCallback, useRef, useMemo } from "react"
import { Moon, Sun, Bell, Shield, Palette, Keyboard, Mail, Globe, Lock, Plane, Monitor, UserCog, Trash2, Plus, Tag, X, Camera, HardDrive } from "lucide-react"
import { useTheme } from "@/components/theme-provider"
import { useAuth } from "@/contexts/AuthContext"
import { useI18n } from "@/hooks/useI18n"
import { Avatar, AvatarFallback, AvatarImage } from "@/components/ui/avatar"
import { Button } from "@/components/ui/button"
import { Separator } from "@/components/ui/separator"
import { Switch } from "@/components/ui/switch"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Textarea } from "@/components/ui/textarea"
import { Progress } from "@/components/ui/progress"
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
import { detectTimeZone, listTimeZones } from "@/utils/timezone"
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
  external_message: "",
  audience: "all",
}

// formatStorageBytes renders a byte count with a binary-prefix unit for the
// storage gauge, falling back to a raw byte count below 1 KiB.
function formatStorageBytes(n: number): string {
  if (n >= 1024 ** 3) return `${(n / 1024 ** 3).toFixed(2)} GB`
  if (n >= 1024 ** 2) return `${(n / 1024 ** 2).toFixed(1)} MB`
  if (n >= 1024) return `${(n / 1024).toFixed(0)} KB`
  return `${n} B`
}

export function SettingsPage() {
  const { theme, setTheme, resolvedTheme } = useTheme()
  const { user, updatePrefs } = useAuth()
  const { t } = useI18n()

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
      toast.error(t("settings.profilePhoto.invalidType"))
      return
    }
    if (file.size > 1024 * 1024) {
      toast.error(t("settings.profilePhoto.tooLarge"))
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
      toast.success(t("settings.profilePhoto.updated"))
    } catch (err) {
      toast.error(err instanceof Error ? err.message : t("settings.profilePhoto.updateFailed"))
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
      toast.success(t("settings.profilePhoto.removed"))
    } catch (err) {
      toast.error(err instanceof Error ? err.message : t("settings.profilePhoto.removeFailed"))
    } finally {
      setAvatarBusy(false)
    }
  }

  // Directory profile (display name, title, department, phone) — shown in the
  // GAL and on Outlook contact cards. Self-service via /api/v1/profile.
  const [profile, setProfile] = useState({ display_name: "", title: "", department: "", phone: "" })
  const [profileBusy, setProfileBusy] = useState(false)

  // Read-only storage usage and graduated quota thresholds (absolute bytes,
  // 0 = disabled/unlimited), surfaced by GET /profile for the storage gauge.
  const [quota, setQuota] = useState({ used: 0, limit: 0, warn: 0, prohibitSend: 0 })

  // Display timezone — the IANA zone every date is rendered in. Empty means
  // "follow this device". Saved on change (like the theme buttons) and applied
  // live across the app via updatePrefs.
  const timeZones = useMemo(listTimeZones, [])
  const [timezone, setTimezone] = useState("")

  useEffect(() => {
    api.getProfile()
      .then((p) => {
        setProfile({
          display_name: p.display_name ?? "",
          title: p.title ?? "",
          department: p.department ?? "",
          phone: p.phone ?? "",
        })
        setQuota({
          used: p.quota_used ?? 0,
          limit: p.quota_limit ?? 0,
          warn: p.quota_warn ?? 0,
          prohibitSend: p.quota_prohibit_send ?? 0,
        })
        setTimezone(p.timezone ?? "")
      })
      .catch(() => undefined)
  }, [])

  const handleTimezoneChange = async (tz: string) => {
    setTimezone(tz)
    try {
      await api.updateProfile({ timezone: tz })
      updatePrefs({ timezone: tz })
      toast.success(t("settings.appearance.timezoneSaved"))
    } catch (err) {
      toast.error(err instanceof Error ? err.message : t("settings.appearance.timezoneSaveFailed"))
    }
  }

  const handleSaveProfile = async () => {
    setProfileBusy(true)
    try {
      await api.updateProfile(profile)
      toast.success(t("settings.profile.updated"))
    } catch (err) {
      toast.error(err instanceof Error ? err.message : t("settings.profile.updateFailed"))
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
      toast.success(t("settings.sessions.revoked"))
      setSessions((prev) => prev.filter((s) => s.id !== id))
    } catch (err) {
      console.error("Failed to revoke session:", err)
      toast.error(t("settings.sessions.revokeFailed"))
    }
  }

  const [pushBusy, setPushBusy] = useState(false)

  const handleEnablePush = async () => {
    setPushBusy(true)
    try {
      await enablePushNotifications()
      toast.success(t("settings.push.enabled"))
    } catch (err) {
      toast.error(err instanceof Error ? err.message : t("settings.push.enableFailed"))
    } finally {
      setPushBusy(false)
    }
  }

  const handleDisablePush = async () => {
    setPushBusy(true)
    try {
      await disablePushNotifications()
      toast.success(t("settings.push.disabled"))
    } catch {
      toast.error(t("settings.push.disableFailed"))
    } finally {
      setPushBusy(false)
    }
  }

  const handleChangePassword = async () => {
    if (pwNew.length < 8) {
      toast.error(t("settings.password.tooShort"))
      return
    }
    if (pwNew !== pwConfirm) {
      toast.error(t("settings.password.mismatch"))
      return
    }
    setPwSaving(true)
    try {
      await api.changePassword(pwCurrent, pwNew)
      toast.success(t("settings.password.updated"))
      setPwOpen(false)
      setPwCurrent("")
      setPwNew("")
      setPwConfirm("")
    } catch (err) {
      console.error("Failed to change password:", err)
      toast.error(t("settings.password.changeFailed"))
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
      toast.success(t("settings.settingUpdated"))
    } catch (err) {
      console.error("Failed to save setting:", err)
      toast.error(t("settings.settingSaveFailed"))
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
      toast.error(t("settings.autoReply.subjectRequired"))
      return
    }
    if (vacation.enabled && !vacation.message.trim()) {
      toast.error(t("settings.autoReply.messageRequired"))
      return
    }
    setVacationSaving(true)
    try {
      await api.setVacation(vacation)
      toast.success(t("settings.autoReply.saved"))
      await loadVacation()
    } catch {
      toast.error(t("settings.autoReply.saveFailed"))
    } finally {
      setVacationSaving(false)
    }
  }

  const handleVacationDisable = async () => {
    setVacationSaving(true)
    try {
      await api.deleteVacation()
      toast.success(t("settings.autoReply.disabled"))
      await loadVacation()
    } catch {
      toast.error(t("settings.autoReply.disableFailed"))
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
      toast.error(t("settings.categories.saveFailed"))
    } finally {
      setCatBusy(false)
    }
  }

  const addCategory = () => {
    const name = catName.trim()
    if (!name) return
    if (categories.some((c) => c.name.toLowerCase() === name.toLowerCase())) {
      toast.error(t("settings.categories.exists"))
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
      toast.success(t("settings.signature.saved"))
    } catch {
      toast.error(t("settings.signature.saveFailed"))
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
      toast.error(t("settings.delegates.emailRequired"))
      return
    }
    setDelBusy(true)
    try {
      await api.createDelegation({
        grantee,
        rights: delWrite ? ["read", "write"] : ["read"],
        canSendOnBehalf: delSendOnBehalf,
      })
      toast.success(t("settings.delegates.added"))
      setDelEmail("")
      setDelWrite(false)
      setDelSendOnBehalf(false)
      await loadDelegations()
    } catch (err) {
      toast.error(err instanceof Error ? err.message : t("settings.delegates.addFailed"))
    } finally {
      setDelBusy(false)
    }
  }

  const handleRemoveDelegate = async (id: string) => {
    setDelBusy(true)
    try {
      await api.deleteDelegation(id)
      toast.success(t("settings.delegates.removed"))
      await loadDelegations()
    } catch {
      toast.error(t("settings.delegates.removeFailed"))
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
        <h2 className="text-2xl font-bold">{t("nav.settings")}</h2>
        <p className="text-muted-foreground">
          {t("settings.description")}
        </p>
      </div>

      {/* Profile photo */}
      <SettingSection
        icon={Camera}
        title={t("settings.profilePhoto.title")}
        description={t("settings.profilePhoto.description")}
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
              {avatarBusy ? t("common.saving") : t("settings.profilePhoto.upload")}
            </Button>
            <Button variant="ghost" disabled={avatarBusy} onClick={handleRemoveAvatar}>
              <Trash2 className="mr-2 h-4 w-4" />
              {t("common.remove")}
            </Button>
          </div>
        </div>
        <p className="mt-3 text-xs text-muted-foreground">{t("settings.profilePhoto.hint")}</p>
      </SettingSection>

      {/* Directory profile */}
      <SettingSection
        icon={UserCog}
        title={t("settings.profile.title")}
        description={t("settings.profile.description")}
      >
        <div className="space-y-4">
          <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
            <div className="space-y-2">
              <Label htmlFor="profile-display-name">{t("settings.profile.displayName")}</Label>
              <Input
                id="profile-display-name"
                value={profile.display_name}
                onChange={(e) => setProfile({ ...profile, display_name: e.target.value })}
                placeholder={t("settings.profile.displayNamePlaceholder")}
              />
            </div>
            <div className="space-y-2">
              <Label htmlFor="profile-title">{t("settings.profile.jobTitle")}</Label>
              <Input
                id="profile-title"
                value={profile.title}
                onChange={(e) => setProfile({ ...profile, title: e.target.value })}
                placeholder={t("settings.profile.titlePlaceholder")}
              />
            </div>
            <div className="space-y-2">
              <Label htmlFor="profile-department">{t("settings.profile.department")}</Label>
              <Input
                id="profile-department"
                value={profile.department}
                onChange={(e) => setProfile({ ...profile, department: e.target.value })}
                placeholder={t("settings.profile.departmentPlaceholder")}
              />
            </div>
            <div className="space-y-2">
              <Label htmlFor="profile-phone">{t("settings.profile.phone")}</Label>
              <Input
                id="profile-phone"
                value={profile.phone}
                onChange={(e) => setProfile({ ...profile, phone: e.target.value })}
                placeholder={t("settings.profile.phonePlaceholder")}
              />
            </div>
          </div>
          <Button onClick={handleSaveProfile} disabled={profileBusy}>
            {profileBusy ? t("common.saving") : t("settings.profile.save")}
          </Button>
        </div>
      </SettingSection>

      {/* Storage usage (read-only) */}
      <SettingSection
        icon={HardDrive}
        title={t("settings.storage.title")}
        description={t("settings.storage.description")}
      >
        <div className="space-y-3">
          {quota.limit > 0 ? (
            <>
              <Progress value={Math.min(100, Math.round((quota.used / quota.limit) * 100))} />
              <p className="text-sm text-muted-foreground">
                {t("settings.storage.used", {
                  used: formatStorageBytes(quota.used),
                  limit: formatStorageBytes(quota.limit),
                  pct: String(Math.min(100, Math.round((quota.used / quota.limit) * 100))),
                })}
              </p>
            </>
          ) : (
            <p className="text-sm text-muted-foreground">
              {t("settings.storage.unlimited", { used: formatStorageBytes(quota.used) })}
            </p>
          )}
          {quota.warn > 0 && (
            <p className="text-xs text-muted-foreground">
              {t("settings.storage.warnAt", { size: formatStorageBytes(quota.warn) })}
            </p>
          )}
          {quota.prohibitSend > 0 && (
            <p className="text-xs text-muted-foreground">
              {t("settings.storage.blockSendAt", { size: formatStorageBytes(quota.prohibitSend) })}
            </p>
          )}
        </div>
      </SettingSection>

      {/* Appearance */}
      <SettingSection
        icon={Palette}
        title={t("settings.appearance.title")}
        description={t("settings.appearance.description")}
      >
        <div className="space-y-4">
          <div className="flex items-center justify-between">
            <div>
              <p className="font-medium">{t("settings.appearance.theme")}</p>
              <p className="text-sm text-muted-foreground">{t("settings.appearance.themeDescription")}</p>
            </div>
            <div className="flex gap-2">
              <Button
                variant={theme === "light" ? "default" : "outline"}
                size="icon"
                onClick={() => setTheme("light")}
                title={t("settings.appearance.lightMode")}
              >
                <Sun className="h-4 w-4" />
              </Button>
              <Button
                variant={theme === "dark" ? "default" : "outline"}
                size="icon"
                onClick={() => setTheme("dark")}
                title={t("settings.appearance.darkMode")}
              >
                <Moon className="h-4 w-4" />
              </Button>
              <Button
                variant={theme === "system" ? "default" : "outline"}
                size="icon"
                onClick={() => setTheme("system")}
                title={t("settings.appearance.systemDefault")}
              >
                <Globe className="h-4 w-4" />
              </Button>
            </div>
          </div>
          <Separator />
          <SettingRow
            title={t("settings.appearance.darkMode")}
            description={t("settings.appearance.darkModeDescription")}
            checked={theme === "dark"}
            onChange={() => setTheme(resolvedTheme === "dark" ? "light" : "dark")}
          />
          <Separator />
          <div className="flex items-center justify-between gap-4">
            <div>
              <p className="font-medium">{t("settings.appearance.timezone")}</p>
              <p className="text-sm text-muted-foreground">{t("settings.appearance.timezoneDescription")}</p>
            </div>
            <select
              value={timezone}
              onChange={(e) => void handleTimezoneChange(e.target.value)}
              className="max-w-[16rem] rounded-lg border bg-background px-3 py-2 text-sm outline-none focus:ring-2 focus:ring-primary/20"
            >
              <option value="">{t("settings.appearance.timezoneAuto", { zone: detectTimeZone() })}</option>
              {timeZones.map((z) => (
                <option key={z} value={z}>
                  {z}
                </option>
              ))}
            </select>
          </div>
        </div>
      </SettingSection>

      {/* Notifications */}
      <SettingSection
        icon={Bell}
        title={t("settings.notifications.title")}
        description={t("settings.notifications.description")}
      >
        <div className="space-y-1">
          <SettingRow
            title={t("settings.notifications.email")}
            description={t("settings.notifications.emailDescription")}
            checked={settings.emailNotifications}
            onChange={() => handleToggle("emailNotifications")}
          />
          <Separator />
          <SettingRow
            title={t("settings.notifications.browser")}
            description={t("settings.notifications.browserDescription")}
            checked={settings.browserNotifications}
            onChange={() => handleToggle("browserNotifications")}
          />
          <Separator />
          <SettingRow
            title={t("settings.notifications.sound")}
            description={t("settings.notifications.soundDescription")}
            checked={settings.soundNotifications}
            onChange={() => handleToggle("soundNotifications")}
          />
          <Separator />
          <SettingRow
            title={t("settings.notifications.desktop")}
            description={t("settings.notifications.desktopDescription")}
            checked={settings.desktopNotifications}
            onChange={() => handleToggle("desktopNotifications")}
          />
        </div>
      </SettingSection>

      {/* Email Settings */}
      <SettingSection
        icon={Mail}
        title={t("settings.composition.title")}
        description={t("settings.composition.description")}
      >
        <div className="space-y-1">
          <SettingRow
            title={t("settings.composition.autoSaveDrafts")}
            description={t("settings.composition.autoSaveDraftsDescription")}
            checked={settings.autoSaveDraft}
            onChange={() => handleToggle("autoSaveDraft")}
          />
          <Separator />
          <SettingRow
            title={t("settings.composition.richText")}
            description={t("settings.composition.richTextDescription")}
            checked={settings.richTextMode}
            onChange={() => handleToggle("richTextMode")}
          />
          <Separator />
          <SettingRow
            title={t("settings.composition.autoCorrect")}
            description={t("settings.composition.autoCorrectDescription")}
            checked={settings.autoCorrect}
            onChange={() => handleToggle("autoCorrect")}
          />
          <Separator />
          <SettingRow
            title={t("settings.composition.spellCheck")}
            description={t("settings.composition.spellCheckDescription")}
            checked={settings.spellCheck}
            onChange={() => handleToggle("spellCheck")}
          />
        </div>
      </SettingSection>

      {/* Auto-Reply (Out of Office) */}
      <SettingSection
        icon={Plane}
        title={t("settings.autoReply.title")}
        description={t("settings.autoReply.description")}
      >
        {vacationLoading ? (
          <p className="text-sm text-muted-foreground py-3">{t("common.loading")}</p>
        ) : (
          <div className="space-y-4">
            <SettingRow
              title={t("settings.autoReply.enable")}
              description={t("settings.autoReply.enableDescription")}
              checked={vacation.enabled}
              onChange={() => setVacation({ ...vacation, enabled: !vacation.enabled })}
            />
            <Separator />
            <div className="space-y-2">
              <Label htmlFor="vacation-subject">{t("common.subject")}</Label>
              <Input
                id="vacation-subject"
                value={vacation.subject}
                onChange={(e) => setVacation({ ...vacation, subject: e.target.value })}
                placeholder={t("settings.autoReply.subjectPlaceholder")}
                disabled={!vacation.enabled}
              />
            </div>
            <div className="space-y-2">
              <Label htmlFor="vacation-audience">{t("settings.autoReply.audience")}</Label>
              <select
                id="vacation-audience"
                value={vacation.audience || "all"}
                onChange={(e) => setVacation({ ...vacation, audience: e.target.value })}
                disabled={!vacation.enabled}
                className="max-w-[20rem] rounded-lg border bg-background px-3 py-2 text-sm outline-none focus:ring-2 focus:ring-primary/20"
              >
                <option value="all">{t("settings.autoReply.audienceAll")}</option>
                <option value="internal">{t("settings.autoReply.audienceInternal")}</option>
                <option value="external">{t("settings.autoReply.audienceExternal")}</option>
              </select>
            </div>
            <div className="space-y-2">
              <Label htmlFor="vacation-message">{t("settings.autoReply.internalMessage")}</Label>
              <Textarea
                id="vacation-message"
                value={vacation.message}
                onChange={(e) => setVacation({ ...vacation, message: e.target.value })}
                placeholder={t("settings.autoReply.messagePlaceholder")}
                rows={4}
                disabled={!vacation.enabled}
              />
            </div>
            {vacation.audience !== "internal" && (
              <div className="space-y-2">
                <Label htmlFor="vacation-external-message">{t("settings.autoReply.externalMessage")}</Label>
                <Textarea
                  id="vacation-external-message"
                  value={vacation.external_message || ""}
                  onChange={(e) => setVacation({ ...vacation, external_message: e.target.value })}
                  placeholder={t("settings.autoReply.externalMessagePlaceholder")}
                  rows={4}
                  disabled={!vacation.enabled}
                />
                <p className="text-xs text-muted-foreground">{t("settings.autoReply.externalMessageHelp")}</p>
              </div>
            )}
            <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
              <div className="space-y-2">
                <Label htmlFor="vacation-start">{t("settings.autoReply.startDate")}</Label>
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
                <Label htmlFor="vacation-end">{t("settings.autoReply.endDate")}</Label>
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
                {t("common.save")}
              </Button>
              <Button
                variant="outline"
                onClick={handleVacationDisable}
                disabled={vacationSaving}
              >
                {t("settings.disable")}
              </Button>
            </div>
          </div>
        )}
      </SettingSection>

      {/* Signature */}
      <SettingSection
        icon={Mail}
        title={t("settings.signature.title")}
        description={t("settings.signature.description")}
      >
        <div className="space-y-3">
          <Textarea
            id="signature"
            value={signature}
            onChange={(e) => setSignature(e.target.value)}
            placeholder={t("settings.signature.placeholder")}
            rows={4}
          />
          <Button onClick={handleSignatureSave} disabled={signatureSaving}>
            {t("common.save")}
          </Button>
        </div>
      </SettingSection>

      {/* Categories */}
      <SettingSection
        icon={Tag}
        title={t("settings.categories.title")}
        description={t("settings.categories.description")}
      >
        <div className="space-y-3">
          <div className="flex flex-wrap items-center gap-2">
            {categories.length === 0 ? (
              <p className="text-sm text-muted-foreground">{t("settings.categories.empty")}</p>
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
                    aria-label={t("settings.categories.removeAria", { name: c.name })}
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
              aria-label={t("settings.categories.colorAria")}
            />
            <Input
              value={catName}
              onChange={(e) => setCatName(e.target.value)}
              onKeyDown={(e) => { if (e.key === "Enter") addCategory() }}
              placeholder={t("settings.categories.namePlaceholder")}
            />
            <Button onClick={addCategory} disabled={catBusy || !catName.trim()}>
              <Plus className="mr-1 h-4 w-4" />
              {t("common.add")}
            </Button>
          </div>
        </div>
      </SettingSection>

      {/* Delegates */}
      <SettingSection
        icon={UserCog}
        title={t("settings.delegates.title")}
        description={t("settings.delegates.description")}
      >
        <div className="space-y-4">
          {delegations.length > 0 && (
            <div className="space-y-2">
              {delegations.map((d) => (
                <div key={d.id} className="flex items-center justify-between rounded-lg border p-3">
                  <div className="min-w-0">
                    <p className="font-medium truncate">{d.grantee}</p>
                    <p className="text-xs text-muted-foreground">
                      {d.rights || t("settings.delegates.noAccess")}
                      {d.canSendOnBehalf ? ` · ${t("settings.delegates.sendOnBehalfTag")}` : ""}
                      {d.canSendAs ? ` · ${t("settings.delegates.sendAsTag")}` : ""}
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
              <Label htmlFor="delegate-email">{t("settings.delegates.add")}</Label>
              <Input
                id="delegate-email"
                type="email"
                value={delEmail}
                onChange={(e) => setDelEmail(e.target.value)}
                placeholder={t("settings.delegates.emailPlaceholder")}
              />
            </div>
            <SettingRow
              title={t("settings.delegates.allowEditing")}
              description={t("settings.delegates.allowEditingDescription")}
              checked={delWrite}
              onChange={() => setDelWrite((v) => !v)}
            />
            <SettingRow
              title={t("settings.delegates.sendOnBehalf")}
              description={t("settings.delegates.sendOnBehalfDescription")}
              checked={delSendOnBehalf}
              onChange={() => setDelSendOnBehalf((v) => !v)}
            />
            <Button onClick={handleAddDelegate} disabled={delBusy || !delEmail.trim()}>
              <Plus className="mr-2 h-4 w-4" />
              {t("settings.delegates.addDelegate")}
            </Button>
          </div>
        </div>
      </SettingSection>

      {/* Privacy & Security */}
      <SettingSection
        icon={Shield}
        title={t("settings.privacy.title")}
        description={t("settings.privacy.description")}
      >
        <div className="space-y-1">
          <SettingRow
            title={t("settings.privacy.readReceipts")}
            description={t("settings.privacy.readReceiptsDescription")}
            checked={settings.readReceipts}
            onChange={() => handleToggle("readReceipts")}
          />
          <Separator />
          <SettingRow
            title={t("settings.privacy.deliveryReceipts")}
            description={t("settings.privacy.deliveryReceiptsDescription")}
            checked={settings.deliveryReceipts}
            onChange={() => handleToggle("deliveryReceipts")}
          />
          <Separator />
          <SettingRow
            title={t("settings.privacy.showOnlineStatus")}
            description={t("settings.privacy.showOnlineStatusDescription")}
            checked={settings.showOnlineStatus}
            onChange={() => handleToggle("showOnlineStatus")}
          />
          <Separator />
          <SettingRow
            title={t("settings.privacy.allowReadReceipts")}
            description={t("settings.privacy.allowReadReceiptsDescription")}
            checked={settings.allowReadReceipts}
            onChange={() => handleToggle("allowReadReceipts")}
          />
        </div>
      </SettingSection>

      {/* Keyboard Shortcuts */}
      <SettingSection
        icon={Keyboard}
        title={t("settings.shortcuts.title")}
        description={t("settings.shortcuts.description")}
      >
        <div className="space-y-3">
          <p className="text-sm text-muted-foreground">
            {t("settings.shortcuts.help")}
          </p>
          <Button
            variant="outline"
            onClick={() => document.dispatchEvent(new CustomEvent("toggle-shortcuts"))}
          >
            {t("settings.shortcuts.view")}
          </Button>
        </div>
      </SettingSection>

      {/* Push Notifications */}
      <SettingSection
        icon={Bell}
        title={t("settings.push.title")}
        description={t("settings.push.description")}
      >
        <div className="flex flex-wrap items-center gap-2">
          <Button onClick={handleEnablePush} disabled={pushBusy || !pushSupported()}>
            {pushBusy ? t("settings.push.working") : t("settings.push.enable")}
          </Button>
          <Button variant="outline" onClick={handleDisablePush} disabled={pushBusy || !pushSupported()}>
            {t("settings.disable")}
          </Button>
          {!pushSupported() && (
            <span className="text-sm text-muted-foreground">
              {t("settings.push.notSupported")}
            </span>
          )}
        </div>
      </SettingSection>

      {/* Active Sessions */}
      <SettingSection
        icon={Monitor}
        title={t("settings.sessions.title")}
        description={t("settings.sessions.description")}
      >
        {sessions.length === 0 ? (
          <p className="text-sm text-muted-foreground">{t("settings.sessions.empty")}</p>
        ) : (
          <div className="space-y-2">
            {sessions.map((s) => (
              <div key={s.id} className="flex items-center justify-between gap-4 rounded-lg border p-3">
                <div className="min-w-0">
                  <p className="text-sm font-medium truncate">
                    {s.device_type || t("settings.sessions.unknownDevice")} · {s.client_ip || t("settings.sessions.unknownIp")}
                  </p>
                  <p className="text-xs text-muted-foreground truncate">
                    {s.user_agent || "—"}
                  </p>
                  <p className="text-xs text-muted-foreground">{t("settings.sessions.lastActive", { time: s.last_active })}</p>
                </div>
                <Button variant="outline" size="sm" onClick={() => handleRevokeSession(s.id)}>
                  {t("settings.sessions.revoke")}
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
              <h3 className="font-semibold">{t("settings.account.title")}</h3>
              <p className="text-sm text-muted-foreground">
                {t("settings.account.description")}
              </p>
            </div>
          </div>
          <Button variant="outline" onClick={() => setPwOpen(true)}>{t("settings.account.manage")}</Button>
        </div>
      </div>

      <Dialog open={pwOpen} onOpenChange={setPwOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t("settings.password.title")}</DialogTitle>
            <DialogDescription>
              {t("settings.password.description")}
            </DialogDescription>
          </DialogHeader>
          <div className="space-y-3">
            <div className="space-y-1">
              <Label htmlFor="pw-current">{t("settings.password.current")}</Label>
              <Input
                id="pw-current"
                type="password"
                value={pwCurrent}
                onChange={(e) => setPwCurrent(e.target.value)}
                autoComplete="current-password"
              />
            </div>
            <div className="space-y-1">
              <Label htmlFor="pw-new">{t("settings.password.new")}</Label>
              <Input
                id="pw-new"
                type="password"
                value={pwNew}
                onChange={(e) => setPwNew(e.target.value)}
                autoComplete="new-password"
              />
            </div>
            <div className="space-y-1">
              <Label htmlFor="pw-confirm">{t("settings.password.confirm")}</Label>
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
              {t("common.cancel")}
            </Button>
            <Button onClick={handleChangePassword} disabled={pwSaving}>
              {pwSaving ? t("common.saving") : t("settings.password.update")}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <div className="text-center text-sm text-muted-foreground pb-8">
        <p>uMail Server v1.0.0</p>
        <p className="mt-1">{t("settings.footer.tagline")}</p>
      </div>
    </div>
  )
}
