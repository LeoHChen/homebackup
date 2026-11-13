# Set up the HomeBackup Mac app on another Mac

This guide installs the HomeBackup menu bar app on a Mac and pairs it with
your homebackup server, so the server's files show up in Finder. You can
follow it yourself, or fill in the settings and paste the whole file into a
Claude Code session on that Mac.

## Settings

Fill these in for your server. Every later step uses these values.

```sh
HB_VERSION=0.2.0                              # release to install
HB_SERVER=https://homeserver.local:8420       # server address: Bonjour host name or LAN IP, port from "listen"
HB_SERVER_NAME="homebackup on homeserver"     # name the app lists for your server
HB_KEY="XXXX XXXX XXXX XXXX"                  # server key, printed by `homebackupd pair` and in the server log
HB_CONFIG=~/homebackup/homebackup.json        # config file on the server
HB_ROOT=/path/to/backup/root                  # "root" in that config file
```

- **Where to find them:** on the server, `homebackupd -config $HB_CONFIG pair`
  prints the server key. The server log shows the listen address and the
  Bonjour name. If another device is already paired, it can show the key and
  issue the code instead: Mac app → **Pair another device…**, or the
  dashboard at `$HB_SERVER` → Devices → **Add a device**.
- **If you use Claude Code:** paste this file after filling in the settings.
  Claude should run each block in one shell with the settings block first.

**Rules for an assistant following this guide:**
- Don't change the server or the GitHub repo. Don't push or commit anything.
- Ask before any step that needs the user's password or Apple ID.
- Stop and ask when a check below says so. Never pair when the key doesn't match.

## 1. Prerequisites

- macOS 14 or newer, on the same network as the server.
- No Xcode or Apple developer account is needed. Release builds of the app
  are signed with Developer ID and notarized by Apple.

## 2. Check that the server is reachable

```sh
want=$(echo "$HB_KEY" | tr -d ' ' | tr 'A-F' 'a-f')
got=$(curl -sk "$HB_SERVER/api/v1/health" | sed -n 's/.*"certFingerprint":"\([0-9a-f]*\)".*/\1/p')
echo "server: ${got:-unreachable}"
if [ ${#want} -eq 16 ] && [ "${got#"$want"}" != "$got" ]; then echo "key OK"; else echo "KEY MISMATCH OR UNREACHABLE: stop"; fi
```

- **Expected:** `key OK`.
- **If the server is unreachable:**
  - Try the server's LAN IP in `HB_SERVER` instead of the `.local` name.
  - Check that this Mac is on the same network as the server.
  - Check that the server listens on the network (`"listen": ":8420"`, not
    `127.0.0.1`). See [Open it to your home network](../README.md#open-it-to-your-home-network).
- **If the key doesn't match:** stop. Do not pair. Someone else may be
  answering at that address, or the server's TLS key changed.

## 3. Download and install the app

```sh
cd ~/Downloads
base=https://github.com/LeoHChen/homebackup/releases/download/v$HB_VERSION
zip=HomeBackup-$HB_VERSION-macOS.zip
curl -fLO "$base/$zip" && curl -fLO "$base/SHA256SUMS"
grep " $zip\$" SHA256SUMS | shasum -a 256 -c
ditto -x -k "$zip" . && rm -rf /Applications/HomeBackup.app && mv HomeBackup.app /Applications/
spctl -a -vv /Applications/HomeBackup.app
open /Applications/HomeBackup.app
```

- **The checksum line must print `OK`.** If it doesn't, stop.
- **`spctl` must print `accepted` and `source=Notarized Developer ID`.**
- **Older release without a Mac zip:** build the app from source instead. See
  [Mac app](../README.md#mac-app-menu-bar--finder) in the README.

## 4. Check that the app started

- **Menu bar:** it is a menu bar app with no Dock icon. Look for the
  external-drive icon at the top right of the screen. A question mark on the
  icon means it is not paired yet; an exclamation mark means it needs
  attention and the menu says why.
- **Finder extension:** check that it is registered:

  ```sh
  pluginkit -m -p com.apple.fileprovider-nonui | grep homebackup
  ```

  It should list `com.github.leohchen.homebackup.mac.files`.

## 5. Pair (needs the server owner)

1. **On the server,** run this. It prints a 6-digit code, valid for 10 minutes, and the server key.

   ```sh
   homebackupd -config "$HB_CONFIG" pair
   ```

2. **On this Mac,** click the HomeBackup menu bar icon and choose
   **Connect to your home server…**.
3. **Pick the server:** choose `$HB_SERVER_NAME`, or enter `$HB_SERVER`.
4. **If macOS asks** to let the app find devices on the local network, click **Allow**.
5. **Check the key and pair:** the app must show the server key `$HB_KEY`. Then enter the code and click **Pair**.
6. **Allow Finder access:** macOS keeps new File Provider extensions off
   until you allow them. If the menu shows **Finder can't show your files
   yet**, click **Open System Settings…** and turn on HomeBackup under
   **File Providers** (General → Login Items & Extensions on macOS 15 and
   later). Until then Finder says *HomeBackup does not have permission to
   open "HomeBackup-MyFiles"*, and `fileproviderctl dump` shows the domains
   as `user-disabled`. There is no command that turns this on;
   `pluginkit -e use -i com.github.leohchen.homebackup.mac.files` is not
   enough.

## 6. Verify

- **Menu:** the menu shows the server, its key, and its sources, for example
  **My Files**, **Google Photos backup**, and device photos.
- **Finder:** clicking the folder icon next to **My Files** opens Finder.
  HomeBackup locations also appear in the Finder sidebar under Locations.
- **Round trip:**
  1. In Finder → My Files, create a folder `Test from <this Mac>` and put a
     small text file in it.
  2. On the server, check that the folder exists under
     `$HB_ROOT/data/files/`.
  3. Delete the folder in Finder. It moves to the server's trash.
- **If the Finder locations don't appear:**
  - Open **System Settings → General → Login Items & Extensions → File
    Providers** and make sure HomeBackup is enabled (see step 5.6).
  - Quit and reopen the app.
  - Collect the output of:

    ```sh
    pluginkit -m -p com.apple.fileprovider-nonui | grep homebackup
    log show --last 5m --predicate 'subsystem CONTAINS "fileprovider"' | grep -i homebackup | tail -20
    ```

## 7. Report back

If an assistant followed this guide, it should report:

- the install and Gatekeeper result
- whether pairing worked
- which sources appear in Finder
- the round-trip result
- any errors, with their exact text

## Updating the app

Replace `/Applications/HomeBackup.app` with the new build and open it. An
app built from this version or later keeps its pairing across updates. Apps
older than 0.2.0 stored their settings elsewhere, so after updating from
0.1.0, pair the Mac again (menu → **Connect to your home server…**).

## Known limits

- **Sync interval:** Finder fetches changes from the server about once a
  minute while the app runs.
