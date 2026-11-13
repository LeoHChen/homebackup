# Apple apps

Swift code for the iPhone, iPad, and Mac apps. Build on a Mac with Xcode.

- `HomeBackupKit/`: Swift package shared by all apps:
  - API client with TLS key pinning.
  - Pairing.
  - Change feed.
  - Thumbnails.
  - Resumable uploads.
  - Bonjour discovery.

  Run the tests with `cd HomeBackupKit && swift test`.

Planned apps, built on HomeBackupKit:

- **iPhone/iPad app:** a Photos-like library with automatic backup of the camera roll, and a My Files browser.
- **File Provider extension:** shows My Files in the Files app (iOS) and Finder (macOS), with on-demand download and offline editing.
- **Mac menu bar app:** pairing, status, and sync of chosen folders.
