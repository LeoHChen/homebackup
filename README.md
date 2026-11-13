# homebackup

Self-hosted backup of Google Drive, Google Photos, and iCloud to a home server
(Mac mini, Raspberry Pi, or any PC), with first-class iPhone and Mac apps.

- **Plain files on disk**: your backup is browsable without this software.
- **Drive-like history**: previous versions and deleted files are kept for a
  retention period.
- **One Go binary**, no cgo: macOS, Linux ARM64 (Raspberry Pi), Linux/Windows x86.
- **HTTP API** for the Apple apps: see [`api/openapi.yaml`](api/openapi.yaml).

Status: early development. See [`docs/design.md`](docs/design.md) for the
design and roadmap.

## Build

```sh
make build            # current platform -> bin/
make all-platforms    # macOS, Linux (arm64/amd64), Windows
make test
```

## Run

```sh
cp homebackup.example.json homebackup.json   # edit "root"
./bin/homebackupd-darwin-arm64 -config homebackup.json
curl http://localhost:8420/api/v1/health
```

### Pair a device

```sh
./bin/homebackupd-darwin-arm64 -config homebackup.json pair
# Pairing code: 482 913 (valid for 10 minutes)
```

Enter the code in the iPhone or Mac app (or `POST /api/v1/pair`). Each device
gets its own token, which can be revoked with `DELETE /api/v1/devices/{id}`.
For scripts, you can also set an admin token with `HOMEBACKUP_TOKEN`.

### Device uploads

A `device-upload` source (e.g. `"iphone"`) receives files pushed by the apps
through the resumable upload API (`/api/v1/uploads`): create a session,
send chunks with `Upload-Offset`, then complete; the server verifies the
SHA-256. Interrupted uploads resume where they stopped; unfinished sessions
are removed after 7 days.

## Google Drive setup

1. In the [Google Cloud Console](https://console.cloud.google.com/), create a
   project and enable the **Google Drive API**.
2. Configure the **OAuth consent screen** (External) and add your Google
   account as a test user.
3. Under **Credentials**, create an **OAuth client ID** of type **Desktop app**
   and download the JSON file. Keep it private.
4. Point `options.credentials` of the `gdrive` source at that file, then sign in once:

   ```sh
   ./bin/homebackupd-darwin-arm64 -config homebackup.json auth gdrive
   ```

   The token is saved (mode 0600) in `<root>/state/gdrive-token.json` and
   refreshed automatically. Access is read-only (`drive.readonly`).

Google Docs, Sheets, Slides, and Drawings are saved as `.docx`, `.xlsx`,
`.pptx`, and `.pdf`. Forms, Sites, and shortcuts are skipped. Files larger than
Google's 10 MB export limit are skipped with a warning.

## Google Photos import (Takeout)

Google's Photos API no longer lets apps read your existing library, so the
history comes from [Google Takeout](https://takeout.google.com/):

1. Select only **Google Photos**, choose **.zip** and the largest file size.
2. Put every downloaded part (`takeout-...-001.zip`, `-002.zip`, ...) in the
   `inbox` folder of the `gphotos-takeout` source. Extracted folders work too.
3. Run a sync (`POST /api/v1/sources/gphotos/sync`) or wait for the schedule.

Photos are stored as `<year>/<month>/<name>` using the capture time from the
Takeout metadata, with the file date set to the capture time. Photos repeated
in albums, and photos already imported from an earlier Takeout, are stored
once. Imported archives are remembered; you can delete them afterwards.

## Layout

```
cmd/homebackupd/   server daemon
internal/api/      HTTP API
internal/config/   configuration file
internal/store/    on-disk layout: data, versions, trash
internal/source/   source interface
internal/sources/  source implementations (gdrive, ...)
internal/catalog/  SQLite index of files, versions, trash, sync state
internal/engine/   sync engine, retention janitor, scheduler
api/openapi.yaml   API specification
docs/design.md     design document
```

## License

MIT
