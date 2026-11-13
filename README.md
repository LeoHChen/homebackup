# homebackup

Self-hosted replacement for Google Drive, Google Photos, and iCloud. It runs on
your own Mac mini, Raspberry Pi, or PC, and comes with iPhone and Mac apps.

- **Backs up your clouds:** Google Drive (read-only mirror), Google Photos
  (from Takeout), and your iPhone's photo library, including iCloud Photos.
- **My Files:** a writable drive that lives on your server. It shows up in
  Finder and in the iPhone Files app, like iCloud Drive.
- **Drive-like history:** previous versions are kept, and deleted files go to
  a trash for a while.
- **Plain files on disk:** your backup is browsable without this software.
- **One Go binary with no dependencies:** runs on macOS, Linux (including
  Raspberry Pi), and Windows.
- **Private by design:** HTTPS with a pinned key, devices paired with a
  6-digit code, and everything stays in your home.

Status: early (v0.1.0). Keep your cloud accounts while you try it.

## Contents

1. [How it fits together](#how-it-fits-together)
2. [Install the server](#install-the-server)
3. [Configure](#configure)
4. [Run it as a service](#run-it-as-a-service)
5. [Open it to your home network](#open-it-to-your-home-network)
6. [Install the apps and pair them](#install-the-apps-and-pair-them)
7. [Back up Google Drive](#google-drive-setup)
8. [Import Google Photos](#google-photos-import-takeout)
9. [Update, roll back, uninstall](#update-roll-back-uninstall)
10. [Troubleshooting](#troubleshooting)
11. [Build from source](#build-from-source)
12. [API](#api)

## How it fits together

```
 iPhone app ─┐                        ┌─ Google Drive (read-only mirror)
 Mac app ────┼── HTTPS (home network) ── homebackupd ─┼─ Google Photos (Takeout import)
 Files/Finder┘                        └─ /backup drive: plain files + history
```

`homebackupd` is the server. It keeps everything under one folder, the
**root**, usually on an external drive:

```
<root>/data/<source>/...     your files, as plain files
<root>/versions/...          previous versions
<root>/trash/...             deleted files (kept for trashDays)
<root>/state/                catalog database, TLS key, thumbnails, tokens
```

## Install the server

Download the archive for your server from the
[latest release](https://github.com/LeoHChen/homebackup/releases/latest):

| Server | Archive |
|---|---|
| Mac with Apple silicon | `homebackupd-<version>-darwin-arm64.tar.gz` |
| Intel Mac | `homebackupd-<version>-darwin-amd64.tar.gz` |
| Raspberry Pi 4/5 (64-bit OS) | `homebackupd-<version>-linux-arm64.tar.gz` |
| Linux PC | `homebackupd-<version>-linux-amd64.tar.gz` |
| Windows PC | `homebackupd-<version>-windows-amd64.zip` |

For example, on a Mac with Apple silicon:

```sh
V=v0.1.0
curl -LO https://github.com/LeoHChen/homebackup/releases/download/$V/homebackupd-$V-darwin-arm64.tar.gz
curl -LO https://github.com/LeoHChen/homebackup/releases/download/$V/SHA256SUMS
shasum -a 256 -c SHA256SUMS --ignore-missing
tar xzf homebackupd-$V-darwin-arm64.tar.gz
mkdir -p ~/homebackup/bin
cp homebackupd-$V-darwin-arm64/homebackupd ~/homebackup/bin/
~/homebackup/bin/homebackupd -version
```

If you download with a browser on macOS, Gatekeeper may block the binary.
Clear the download flag with
`xattr -d com.apple.quarantine ~/homebackup/bin/homebackupd`. Downloads made
with `curl` are not flagged.

Optional tools for HEIC, RAW, and video thumbnails:
- macOS has `sips` and `qlmanage` built in; add `brew install ffmpeg` for
  better video thumbnails.
- Linux: `sudo apt install ffmpeg libheif-examples`.

## Configure

Copy `homebackup.example.json` from the archive to
`~/homebackup/homebackup.json` (on Linux, `/etc/homebackup/homebackup.json`)
and edit it:

```json
{
  "listen": ":8420",
  "tls": { "mode": "auto" },
  "root": "/Volumes/Backup/homebackup",
  "retention": { "trashDays": 30, "versionDays": 30, "maxVersions": 100 },
  "sources": [
    { "id": "gdrive", "type": "gdrive", "schedule": "1h",
      "options": { "credentials": "/Users/me/.config/homebackup/google-oauth-client.json" } },
    { "id": "gphotos", "type": "gphotos-takeout", "schedule": "24h",
      "options": { "inbox": "/Volumes/Backup/takeout-inbox" } },
    { "id": "iphone", "type": "device-upload" },
    { "id": "files", "type": "drive" }
  ]
}
```

| Setting | Meaning |
|---|---|
| `listen` | `127.0.0.1:8420` for this computer only; `:8420` for your home network (turns on HTTPS and discovery) |
| `root` | Backup folder. It must exist, which protects you if the drive is unplugged. |
| `retention` | Days to keep trash and old versions. Versions are deleted only when they are older than `versionDays` and more than `maxVersions` exist. |
| `gdrive` | Google Drive mirror (read-only); see [Google Drive setup](#google-drive-setup) |
| `gphotos-takeout` | Google Photos import from Takeout zips placed in `inbox` |
| `device-upload` | Photos backed up by the iPhone app |
| `drive` | **My Files**: the writable drive shown in Finder and Files |

Remove sources you don't use. Create the root folder first, then do a test run:

```sh
mkdir -p /Volumes/Backup/homebackup
~/homebackup/bin/homebackupd -config ~/homebackup/homebackup.json
# Ctrl-C to stop
```

## Run it as a service

### macOS (LaunchAgent)

The archive includes `com.homebackup.homebackupd.plist`:

```sh
sed "s|HOME_DIR|$HOME|g" com.homebackup.homebackupd.plist \
  > ~/Library/LaunchAgents/com.homebackup.homebackupd.plist
launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/com.homebackup.homebackupd.plist
tail -f ~/Library/Logs/homebackupd.log
```

The service starts at login and restarts if it stops. If the backup drive is
not mounted, the server waits and retries every minute. It never writes to the
internal disk by mistake. If the Mac should run without anyone logged in,
turn on automatic login for that user.

### Linux / Raspberry Pi (systemd)

The archive includes `homebackupd.service`; its header lists the install
commands (create a `homebackup` user, copy the binary to `/usr/local/bin`,
config to `/etc/homebackup/`, then `systemctl enable --now homebackupd`).
Logs: `journalctl -u homebackupd -f`.

### Windows

Run `homebackupd.exe -config homebackup.json` in a terminal. To start it at
boot, use Task Scheduler ("At startup", "Run whether user is logged on or
not").

## Open it to your home network

With `"listen": ":8420"`:

- HTTPS is turned on with a self-signed certificate, created on first start
  in `<root>/state/tls/`.
- The server advertises itself over Bonjour (`_homebackup._tcp`), so the apps
  find it without an address.
- The log prints the **server key**, for example `7F3C 09D2 5B8E A41F`. Apps
  show the key they see when pairing; check that the two match.
- On macOS, allow incoming connections when the firewall asks.

Check from another computer on the network:

```sh
curl -k https://<server-name>.local:8420/api/v1/health
# {"certFingerprint":"7f3c09d2...","status":"ok","version":"v0.1.0"}
```

To use another certificate (for example from `tailscale cert`), set
`"tls": {"mode": "files", "certFile": "...", "keyFile": "..."}`. Set
`"bonjour": false` to turn off discovery.

## Install the apps and pair them

### Pairing (all apps)

On the server, run:

```sh
~/homebackup/bin/homebackupd -config ~/homebackup/homebackup.json pair
# Pairing code: 482 913 (valid for 10 minutes)
# Server key: 7F3C 09D2 5B8E A41F
```

In the app, pick your server and check the server key. Then enter the code.
Each device gets its own token. To list devices, use `GET /api/v1/devices`;
to revoke one, use `DELETE /api/v1/devices/{id}`.

### Mac app (menu bar + Finder)

The Mac app shows each source in Finder's sidebar. **My Files** is writable.
The Google backups are read-only.

- **Download:** `HomeBackup-<version>-macOS.zip` from the release, if one is
  attached. Unzip it, move **HomeBackup** to Applications, and open it. The
  icon appears in the menu bar.
- **Build from source:** this needs Xcode 16 or newer and an Apple developer
  account.

  ```sh
  brew install xcodegen
  git clone https://github.com/LeoHChen/homebackup.git && cd homebackup/apple
  echo 'DEVELOPMENT_TEAM = <your Team ID>' > Local.xcconfig
  xcodegen generate
  xcodebuild -project HomeBackup.xcodeproj -scheme HomeBackupMac \
    -destination 'platform=macOS' -configuration Release -derivedDataPath build \
    -allowProvisioningUpdates -allowProvisioningDeviceRegistration build
  ditto build/Build/Products/Release/HomeBackup.app /Applications/HomeBackup.app
  open /Applications/HomeBackup.app
  ```

  If the first build fails with "Build input file cannot be found" for a
  `.provisionprofile`, run it again. Xcode was still downloading the profile.

Then click the menu bar icon, choose **Connect to your home server…**, and
pair. If Finder locations don't appear, check **System Settings → General →
Login Items & Extensions → File Providers**.

### iPhone and iPad app

The app has four parts:
- **Photos:** everything on the server, newest first.
- **Backup:** automatic backup of the photo library. Originals are
  downloaded from iCloud Photos when needed.
- **Files:** browse every source.
- **Files app:** each source appears as a location in the Files app.

Until it is on TestFlight, install it with Xcode:

1. Connect the iPhone to the Mac with a cable and trust the computer.
2. Run `cd homebackup/apple && xcodegen generate`, then
   `open HomeBackup.xcodeproj`.
3. Select the **HomeBackup** scheme and your iPhone, then click **Run**. Xcode
   registers the phone in your developer account.
4. Open the app, choose your server, check the key, and enter the code.
5. In **Settings**, turn on **Back up this device's photos** and allow access
   to Photos.

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

## Update, roll back, uninstall

**Update (macOS):**

```sh
cp ~/homebackup/bin/homebackupd ~/homebackup/bin/homebackupd.prev
cp homebackupd-<new>/homebackupd ~/homebackup/bin/homebackupd
launchctl kickstart -k gui/$(id -u)/com.homebackup.homebackupd
```

**Roll back:** copy `homebackupd.prev` back, then run `kickstart` again.
On Linux, replace `/usr/local/bin/homebackupd` and run
`sudo systemctl restart homebackupd`.

**Uninstall (macOS):** run
`launchctl bootout gui/$(id -u)/com.homebackup.homebackupd`, then delete
`~/homebackup` and the plist. Your backup stays in the root folder as plain
files.

## Troubleshooting

| Problem | Check |
|---|---|
| Server does not start | The log says `backup root ... is not available`: mount the drive or fix `root` |
| Apps can't find the server | Same network? Try `curl -k https://<name>.local:8420/api/v1/health`, then enter the address by hand. Allow incoming connections in the macOS firewall. |
| Server key doesn't match | Don't pair. Someone else may be answering, or the TLS key was replaced (`<root>/state/tls/`). |
| Wrong or expired code | Run `homebackupd pair` again; codes last 10 minutes, and 5 wrong tries lock pairing for a while |
| No video or HEIC thumbnails | The startup line `thumbnails tools=[...]` lists the tools found; install `ffmpeg` |
| Finder/Files locations missing | Pair the app first; enable HomeBackup under File Providers (macOS) |

## Build from source

```sh
make build            # current platform -> bin/
make all-platforms    # macOS, Linux (arm64/amd64), Windows
make test
cd apple/HomeBackupKit && swift test   # Swift client library (macOS)
```

To release, push a tag such as `v0.1.0`. The
[release workflow](.github/workflows/release.yml) builds every platform and
publishes the archives and `SHA256SUMS`.

Layout:

```
cmd/homebackupd/   server daemon
internal/          api, catalog (SQLite), engine, store, sources, thumbs, tlsutil, discovery
apple/             HomeBackupKit (Swift library), iPhone app, File Provider, Mac app
deploy/            macOS LaunchAgent, Linux systemd unit
api/openapi.yaml   API specification
docs/design.md     design document
```

## API

The apps use the HTTP API in [`api/openapi.yaml`](api/openapi.yaml). For
scripts, set an admin token with the `HOMEBACKUP_TOKEN` environment variable
and send `Authorization: Bearer <token>`.

### Device uploads

A `device-upload` source (e.g. `"iphone"`) receives files pushed by the apps
through the resumable upload API (`/api/v1/uploads`): create a session,
send chunks with `Upload-Offset`, then complete; the server verifies the
SHA-256. Interrupted uploads resume where they stopped; unfinished sessions
are removed after 7 days.

### My Files (drive source)

A `drive` source (e.g. `{"id": "files", "type": "drive"}`) is a writable
drive where the home server holds the primary copy, like Google Drive or
iCloud Drive. Apps create folders (`POST /api/v1/folders`), upload files and
new versions through the upload API (with `fileId` and `baseSha256`),
rename or move (`PATCH /api/v1/files/{id}`), and delete to the trash
(`DELETE /api/v1/files/{id}`). Sources that mirror Google are read-only.

Sync clients follow `GET /api/v1/changes`: start at cursor 0, then pass
back the returned cursor. An edit made from a stale copy is never lost; it
is saved next to the file as `name (conflict from <device> <time>).ext`.
Every change keeps the previous version and the trash, as for other sources.

### Thumbnails

`GET /api/v1/files/{id}/thumbnail?size=small|large` returns a JPEG
(256 or 1024 px). Files with `"media": "photo"` or `"video"` have one.
Small thumbnails are made in the background after each sync and cached in
`<root>/state/thumbs` by content hash. JPEG, PNG, GIF, WebP, BMP, and TIFF
work everywhere; HEIC, RAW, and video use `sips`/`qlmanage` (built into
macOS), `heif-convert`, or `ffmpeg` (`brew install ffmpeg` or
`apt install ffmpeg libheif-examples`). The startup log lists the tools found.

## License

MIT
