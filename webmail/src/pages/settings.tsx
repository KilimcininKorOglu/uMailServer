import { useState, useEffect, useCallback } from "react"
import { Moon, Sun, Bell, Shield, Palette, Keyboard, Mail, Globe, Lock, Plane, Monitor } from "lucide-react"
import { useTheme } from "@/components/theme-provider"
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
import type { VacationAutoReply, ClientSession } from "@/utils/api"
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
        <DialogContent aria-describedby="change-password-desc">
          <DialogHeader>
            <DialogTitle>Change password</DialogTitle>
            <DialogDescription id="change-password-desc">
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
