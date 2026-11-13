# homebackup — Design

Status: draft (step 1 of the project plan)

## 1. Goals

- Keep a complete, local copy of Google Drive, Google Photos, and (on macOS)
  iCloud Drive on a home server.
- Start as a **backup of the cloud**; over time make the **home server the
  primary copy** and push an encrypted copy back to the cloud.
- Drive-like history: keep previous versions and keep deleted files for a
  retention period before purging.
- One Go codebase that builds for macOS (Mac mini), Linux ARM64 (Raspberry
  Pi 4), and Linux/Windows x86 (old PC). No cgo, single static binary.
- First-class Apple clients (iPhone, Mac) through a documented HTTP API.

### Non-goals (for now)

- Two-way sync / conflict resolution with the cloud (until step 4).
- Multi-user accounts. One owner, many devices.
- Web UI. Apple apps and a CLI come first.

## 2. Architecture

```
   ┌────────────┐   ┌──────────────┐   ┌─────────────┐
   │ iPhone app │   │   Mac app    │   │ homebackup  │
   │ (PhotoKit) │   │ (menu bar)   │   │    CLI      │
   └─────┬──────┘   └──────┬───────┘   └──────┬──────┘
         │  HTTPS / JSON  (LAN, Bonjour, Tailscale)
         ▼                 ▼                  ▼
   ┌──────────────────────────────────────────────────┐
   │                 homebackupd (Go)                 │
   │  ┌──────────┐  ┌───────────┐  ┌───────────────┐  │
   │  │ HTTP API │  │ Scheduler │  │ Source plugins│  │
   │  └────┬─────┘  └─────┬─────┘  │ gdrive        │  │
   │       │              │        │ gphotos-takeout│ │
   │       ▼              ▼        │ device-upload │  │
   │  ┌───────────────────────┐    │ icloud-local  │  │
   │  │ Catalog (SQLite)      │◄───┤               │  │
   │  └───────────┬───────────┘    └───────────────┘  │
   │              ▼                                   │
   │  ┌───────────────────────┐                       │
   │  │ Store (plain files,   │                       │
   │  │ versions, trash)      │                       │
   │  └───────────────────────┘                       │
   └──────────────────────────────────────────────────┘
                 │ internal + external drives
```

- **homebackupd**: long-running daemon. Runs the scheduler, the sources, and
  the API.
- **Sources** implement one Go interface (`internal/source`). Each source
  reports changes since a cursor; the engine writes them to the store and
  catalog.
- **Catalog**: SQLite (pure-Go driver `modernc.org/sqlite`, keeps cross-compile
  cgo-free). Holds files, versions, sync cursors, devices, upload sessions.
- **Store**: the files themselves, on local disks.

## 3. Storage layout

Design rule: **your data must be readable without this software.** Current
files are stored as normal files with their real names, so a drive can be
plugged into any computer and browsed.

```
<root>/
  data/<source>/<original path>                     current version, plain file
  versions/<source>/<original path>/<UTC time>_<name> previous versions
  trash/<source>/<UTC date>/<original path>         deleted files
  state/catalog.db                                  SQLite catalog
  tmp/                                              in-progress downloads/uploads
```

- Writes go to `tmp/` then are atomically renamed into place.
- Each version records a SHA-256 so corruption can be detected by a periodic
  scrub.
- Multiple disks: one root per volume in v1 (e.g. Drive on internal disk,
  Photos on external). Pooling/mirroring is left to the OS (APFS, mdraid,
  ZFS) rather than reimplemented.

### Retention (defaults, configurable)

| Item               | Default                                      |
| ------------------ | -------------------------------------------- |
| Deleted files      | kept in `trash/` for 30 days                 |
| Previous versions  | last 100 versions **or** 30 days, whichever keeps more |
| Photos             | never versioned (immutable), deletions go to trash |

A daily janitor job purges expired trash and versions.

## 4. Sources

### 4.1 gdrive (step 2)

- OAuth 2.0 "installed app" flow; refresh token stored in the catalog,
  encrypted with a key in the OS keychain where available, else a key file
  with `0600` permissions.
- First run: full listing. After that: Drive **Changes API** using a stored
  `startPageToken`, so only changed items are fetched.
- Google Docs/Sheets/Slides/Drawings are exported to `.docx/.xlsx/.pptx/.pdf`
  (configurable). Binary files downloaded as-is, verified with `md5Checksum`.
- Shared drives and "shared with me": opt-in.

### 4.2 gphotos-takeout (step 2)

Since March 2025, the Google Photos Library API only returns media that the
calling app uploaded, so it cannot back up an existing library. Instead:

- Import Google Takeout archives (`.zip` / `.tgz`), merging the JSON
  sidecar metadata (capture time, GPS, description) into the catalog.
- Layout: `data/gphotos/<YYYY>/<MM>/<file>`.
- Re-importing a newer Takeout is idempotent (dedupe by hash).

### 4.3 device-upload (step 3)

The iPhone app reads the Photos library with PhotoKit (this includes iCloud
Photos) and uploads originals via the resumable upload API (§5.3). This
replaces Google Photos as the ongoing photo backup.

### 4.4 icloud-local (step 3, macOS only)

Reads the locally synced iCloud Drive folder
(`~/Library/Mobile Documents/com~apple~CloudDocs`) on the Mac. Files that are
"evicted" (cloud-only) are downloaded via `brctl download` before copying.
Not available on Linux/Windows hosts.

### Source interface

```go
type Source interface {
    ID() string
    // Changes returns items changed since cursor, plus the next cursor.
    Changes(ctx context.Context, cursor string) (ChangeSet, error)
    // Open streams the content of one item.
    Open(ctx context.Context, item Item) (io.ReadCloser, error)
}
```

## 5. API

REST + JSON under `/api/v1`. Full spec: [`api/openapi.yaml`](../api/openapi.yaml).

### 5.1 Network and discovery

- Listens on the LAN; advertises `_homebackup._tcp` over Bonjour so the
  Apple apps find the server without typing an IP.
- Remote access: **Tailscale** (no router port forwarding). TLS via
  Tailscale HTTPS certs, or a self-signed cert pinned by the apps on pairing.

### 5.2 Auth: device pairing

1. `homebackupd pair` prints a 6-digit code and a QR code.
2. The app calls `POST /api/v1/pair` with the code and a device name.
3. Server returns a long-lived device token (stored in the iOS/macOS
   Keychain). Tokens can be listed and revoked.

All other endpoints require `Authorization: Bearer <token>`.

### 5.3 Endpoints (v1)

| Method | Path                           | Purpose                         |
| ------ | ------------------------------ | ------------------------------- |
| GET    | `/health`                      | liveness, no auth               |
| POST   | `/pair`                        | exchange pairing code for token |
| GET    | `/status`                      | disks, last sync per source, errors |
| GET    | `/sources`                     | list sources and state          |
| POST   | `/sources/{id}/sync`           | trigger a sync now              |
| GET    | `/files?source=&path=`         | list a folder                   |
| GET    | `/files/{id}`                  | metadata                        |
| GET    | `/files/{id}/content`          | download (supports `Range`)     |
| GET    | `/files/{id}/versions`         | version history                 |
| POST   | `/files/{id}/restore`          | restore a version or from trash |
| GET    | `/trash`                       | list deleted files              |
| POST   | `/uploads`                     | start resumable upload          |
| PATCH  | `/uploads/{id}`                | append a chunk (`Upload-Offset`)|
| POST   | `/uploads/{id}/complete`       | finish, verify SHA-256          |
| GET    | `/devices` / DELETE `/devices/{id}` | manage paired devices      |

## 6. Configuration

A single `homebackup.json` (path via `-config`), e.g.:

```json
{
  "listen": ":8420",
  "root": "/Volumes/Backup/homebackup",
  "retention": { "trashDays": 30, "versionDays": 30, "maxVersions": 100 },
  "sources": [
    { "id": "gdrive", "type": "gdrive", "schedule": "1h" },
    { "id": "gphotos", "type": "gphotos-takeout" }
  ]
}
```

## 7. Build and release

- `CGO_ENABLED=0` builds for `darwin/arm64`, `darwin/amd64`, `linux/arm64`,
  `linux/amd64`, `windows/amd64` in CI.
- Service setup: `launchd` plist (macOS), `systemd` unit (Linux).
- Apple apps live in `apple/` (SwiftUI, shared package for iOS + macOS) in
  step 3.

## 8. Roadmap

1. **Design and setup** (done): design, API spec, Go skeleton, CI.
2. **Server v1**: catalog, sync engine, file API, gdrive source (done); Takeout importer, pairing and uploads (next).
3. **Apple apps**: iPhone photo backup, Mac menu bar app, icloud-local.
4. **Local primary**: encrypted off-site push, scrub, alerts.

## 9. Open questions

- Off-site target for step 4 (Google Drive itself, Backblaze B2, or S3).
- Whether to back up Gmail/Contacts later.
- License for the public repo (MIT assumed in this PR).
