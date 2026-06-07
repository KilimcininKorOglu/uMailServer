// Centralized, timezone-aware date formatting for the webmail UI.
//
// The user picks a display timezone during onboarding (or in Settings); every
// presentation must render instants in THAT zone instead of the browser's. The
// chosen IANA zone is kept in a module singleton so the pure formatter functions
// (used across many pages) can read it without threading React context through
// every call site. AuthContext sets it from /auth/me on load and onboarding /
// settings update it on change. An empty value means "follow the device" —
// formatters then omit the timeZone option and fall back to the browser zone.

const TZ_STORAGE_KEY = 'umailserver-timezone'

// Seed synchronously from localStorage so the very first render already uses the
// chosen zone (no flash of browser-zone times before /auth/me resolves).
let displayTimeZone = ''
try {
  displayTimeZone = localStorage.getItem(TZ_STORAGE_KEY) || ''
} catch {
  displayTimeZone = ''
}

export function setDisplayTimeZone(tz: string): void {
  displayTimeZone = tz || ''
  try {
    if (displayTimeZone) {
      localStorage.setItem(TZ_STORAGE_KEY, displayTimeZone)
    } else {
      localStorage.removeItem(TZ_STORAGE_KEY)
    }
  } catch {
    // ignore storage failures (private mode, quota) — the in-memory value still
    // applies for this session.
  }
}

export function getDisplayTimeZone(): string {
  return displayTimeZone
}

// withTz merges the chosen timezone into Intl options. Callers pass their format
// options and get them back with { timeZone } added when a zone is set, so
// inline toLocale* calls (calendar, tasks, compose, email-detail) localize to
// the user's zone with a one-line change.
export function withTz(opts: Intl.DateTimeFormatOptions = {}): Intl.DateTimeFormatOptions {
  return displayTimeZone ? { ...opts, timeZone: displayTimeZone } : opts
}

// formatDate is the compact relative format used in message lists: time within a
// day, weekday within a week, otherwise month + day.
export function formatDate(dateString: string): string {
  const date = new Date(dateString)
  const now = new Date()
  const diff = now.getTime() - date.getTime()

  if (diff < 86400000) {
    // Less than 24 hours
    return date.toLocaleTimeString([], withTz({ hour: '2-digit', minute: '2-digit' }))
  } else if (diff < 604800000) {
    // Less than 7 days
    return date.toLocaleDateString([], withTz({ weekday: 'short' }))
  } else {
    return date.toLocaleDateString([], withTz({ month: 'short', day: 'numeric' }))
  }
}

// formatFullDate is the long date+time used in detail/header contexts.
export function formatFullDate(dateString: string): string {
  const date = new Date(dateString)
  return date.toLocaleString([], withTz({
    year: 'numeric',
    month: 'long',
    day: 'numeric',
    hour: '2-digit',
    minute: '2-digit'
  }))
}

// formatAbsolute renders a full, unambiguous localized date+time for the message
// lists (which previously showed the raw server Date string in the server's
// zone). Invalid input is returned unchanged so a malformed header still shows
// something.
export function formatAbsolute(dateString: string): string {
  const date = new Date(dateString)
  if (isNaN(date.getTime())) return dateString
  return date.toLocaleString([], withTz({
    year: 'numeric',
    month: 'short',
    day: 'numeric',
    hour: '2-digit',
    minute: '2-digit'
  }))
}
