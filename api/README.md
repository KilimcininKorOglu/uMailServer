# uMailServer API Documentation

This directory contains the current machine-readable API specs and a human summary of the live HTTP API surface.

## Files

- `openapi.yaml` — authoritative OpenAPI 3.0.3 document
- `swagger.yaml` — Swagger 2.0 compatibility snapshot
- `swagger.json` — JSON form of `swagger.yaml`

## Scope

These files describe the current HTTP API served by `internal/api`.

They intentionally focus on the live routes mounted by the server:

- `/api/v1/*` REST endpoints
- `/health`, `/health/live`, `/health/ready`
- `/metrics`

They do **not** try to model every non-REST surface in the binary, such as:

- `/mcp` JSON-RPC
- autoconfig / autodiscover XML endpoints
- embedded frontend assets

## Authentication Model

### Login

Authenticate with:

```http
POST /api/v1/auth/login
```

with:

```json
{
  "email": "user@example.com",
  "password": "secret",
  "totp_code": "123456"
}
```

### Session behavior

- Browser clients receive the JWT via the `jwt` HttpOnly cookie.
- Non-browser API clients also receive `token` in the JSON response.
- Login and refresh responses can include `must_change_password`.

### Protected routes

All `/api/v1/*` routes except login require authentication.

## Listener Model

uMailServer can run in two layouts:

1. **Single listener mode** — user API and admin API share the main HTTP listener.
2. **Separate admin listener mode** — admin UI and admin-only API routes are hidden from the main listener and served only from the dedicated admin listener.

When separate admin mode is enabled, admin routes such as `/api/v1/domains`, `/api/v1/accounts`, `/api/v1/aliases`, `/api/v1/queue`, `/api/v1/stats`, and `/api/v1/admin/*` are **not** available on the main user-facing listener.

## Metrics Endpoints

There are two different metrics-style endpoints:

- `GET /metrics` — Prometheus text output, admin-auth protected
- `GET /api/v1/metrics` — JSON metrics payload, admin-auth protected

## Endpoint Summary

### Authentication

| Method | Path | Notes |
|--------|------|-------|
| POST | `/api/v1/auth/login` | Login, sets cookie, may also return `token` |
| POST | `/api/v1/auth/logout` | Logout |
| DELETE | `/api/v1/auth/logout` | Also accepted |
| POST | `/api/v1/auth/refresh` | Refresh current authenticated session |

### Health and Realtime

| Method | Path | Notes |
|--------|------|-------|
| GET | `/health` | Full health report |
| GET | `/health/live` | Liveness probe |
| GET | `/health/ready` | Readiness probe |
| GET | `/metrics` | Prometheus text metrics, admin auth |
| GET | `/api/v1/events` | Server-Sent Events stream |

### Search, Threads, Vacation

| Method | Path | Notes |
|--------|------|-------|
| GET | `/api/v1/search` | Search endpoint |
| GET | `/api/v1/threads` | List thread summaries |
| GET | `/api/v1/threads/search` | Search threads |
| GET | `/api/v1/threads/{id}` | Thread detail |
| DELETE | `/api/v1/threads/{id}` | Delete thread |
| POST | `/api/v1/threads/{id}/read` | Mark thread read |
| GET | `/api/v1/vacation` | Get vacation config |
| PUT | `/api/v1/vacation` | Update vacation config |
| DELETE | `/api/v1/vacation` | Delete vacation config |

### Push and Filters

| Method | Path | Notes |
|--------|------|-------|
| GET | `/api/v1/push/vapid-public-key` | Public VAPID key |
| POST | `/api/v1/push/subscribe` | Create push subscription |
| DELETE | `/api/v1/push/unsubscribe` | Remove push subscription |
| GET | `/api/v1/push/subscriptions` | List current user subscriptions |
| POST | `/api/v1/push/test` | Send test notification |
| GET | `/api/v1/filters` | List filters |
| POST | `/api/v1/filters` | Create filter |
| POST | `/api/v1/filters/reorder` | Reorder filters |
| GET | `/api/v1/filters/{id}` | Get filter |
| PUT | `/api/v1/filters/{id}` | Update filter |
| DELETE | `/api/v1/filters/{id}` | Delete filter |

### Mail

| Method | Path | Notes |
|--------|------|-------|
| GET | `/api/v1/mail/inbox` | Inbox mail list |
| GET | `/api/v1/mail/sent` | Sent mail list |
| GET | `/api/v1/mail/drafts` | Draft mail list |
| GET | `/api/v1/mail/trash` | Trash mail list |
| GET | `/api/v1/mail/spam` | Spam mail list |
| POST | `/api/v1/mail/send` | Send mail |
| DELETE | `/api/v1/mail/delete` | Delete mail |

### Backups and Cluster

| Method | Path | Notes |
|--------|------|-------|
| GET | `/api/v1/backups` | List backups |
| GET | `/api/v1/backups/{id}` | Backup detail |
| DELETE | `/api/v1/backups/{id}` | Delete backup |
| POST | `/api/v1/backups/{id}/verify` | Verify backup |
| POST | `/api/v1/backups/{id}/restore` | Restore backup |
| POST | `/api/v1/backups/per-user/{user}` | Run per-user backup |
| POST | `/api/v1/backups/per-mailbox/{user}/{mailbox}` | Run per-mailbox backup |
| GET | `/api/v1/backup-jobs` | List backup jobs |
| GET | `/api/v1/backup-jobs/{id}` | Get backup job |
| PUT | `/api/v1/backup-jobs/{id}` | Update backup job |
| DELETE | `/api/v1/backup-jobs/{id}` | Delete backup job |
| POST | `/api/v1/backup-jobs/{id}/run` | Run backup job now |
| GET | `/api/v1/cluster/status` | Cluster status |
| GET | `/api/v1/cluster/instances` | Cluster instances |
| POST | `/api/v1/cluster/failover` | Trigger failover |
| POST | `/api/v1/cluster/heartbeat` | Heartbeat endpoint |

### Admin Resources

| Method | Path | Notes |
|--------|------|-------|
| GET | `/api/v1/domains` | List domains |
| POST | `/api/v1/domains` | Create domain |
| GET | `/api/v1/domains/{name}` | Get domain |
| PUT | `/api/v1/domains/{name}` | Update domain |
| DELETE | `/api/v1/domains/{name}` | Delete domain |
| GET | `/api/v1/accounts` | List accounts |
| POST | `/api/v1/accounts` | Create account |
| GET | `/api/v1/accounts/{email}` | Get account |
| PUT | `/api/v1/accounts/{email}` | Update account |
| DELETE | `/api/v1/accounts/{email}` | Delete account |
| POST | `/api/v1/accounts/{email}/totp/setup` | Begin TOTP setup |
| POST | `/api/v1/accounts/{email}/totp/verify` | Verify and enable TOTP |
| POST | `/api/v1/accounts/{email}/totp/disable` | Disable TOTP |
| GET | `/api/v1/aliases` | List aliases |
| POST | `/api/v1/aliases` | Create alias |
| GET | `/api/v1/aliases/{alias}` | Get alias |
| PUT | `/api/v1/aliases/{alias}` | Update alias |
| DELETE | `/api/v1/aliases/{alias}` | Delete alias |
| GET | `/api/v1/queue` | List queue entries |
| GET | `/api/v1/queue/{id}` | Queue entry detail |
| POST | `/api/v1/queue/{id}` | Retry queue entry |
| DELETE | `/api/v1/queue/{id}` | Drop queue entry |
| GET | `/api/v1/metrics` | JSON metrics |
| GET | `/api/v1/stats` | Dashboard stats |
| GET | `/api/v1/admin/ratelimits/ip/{ip}` | Per-IP rate-limit stats |
| GET | `/api/v1/admin/ratelimits/user/{user}` | Per-user rate-limit stats |
| GET | `/api/v1/admin/config` | Server settings (incl. `security.rate_limit`) |
| PUT | `/api/v1/admin/config` | Update settings (persisted + live-applied) |
| GET | `/api/v1/admin/vacations` | Active vacation overview |
| GET | `/api/v1/admin/push/stats` | Push stats |
| POST | `/api/v1/admin/jwt/rotate` | Rotate JWT signing secret |
| GET | `/api/v1/admin/jwt/status` | JWT key status |
| GET | `/api/v1/admin/queue` | Alias of admin queue list |
| GET | `/api/v1/admin/queue/{id}` | Alias of admin queue detail |
| POST | `/api/v1/admin/queue/{id}` | Alias of retry queue entry |
| DELETE | `/api/v1/admin/queue/{id}` | Alias of drop queue entry |

## Notes on the Specs

- `openapi.yaml` is the primary document to maintain.
- `swagger.yaml` and `swagger.json` are compatibility views and should mirror the same live route surface.
- If routes change in `internal/api/server.go`, update this directory in the same change.
