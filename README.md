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
HOMEBACKUP_TOKEN=$(openssl rand -hex 32) ./bin/homebackupd-darwin-arm64 -config homebackup.json
curl http://localhost:8420/api/v1/health
```

`HOMEBACKUP_TOKEN` is a temporary single-token auth until device pairing lands.

## Layout

```
cmd/homebackupd/   server daemon
internal/api/      HTTP API
internal/config/   configuration file
internal/store/    on-disk layout: data, versions, trash
internal/source/   source interface (gdrive, gphotos-takeout, ...)
api/openapi.yaml   API specification
docs/design.md     design document
```

## License

MIT
