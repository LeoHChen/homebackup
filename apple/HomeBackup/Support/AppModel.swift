import Foundation
import HomeBackupKit
import Observation

/// The paired server and the shared client. Settings are shared with the
/// File Provider extension through SharedStore.
@MainActor @Observable
final class AppModel {
    static let shared = AppModel()

    private(set) var client: HomeBackupClient?
    private(set) var serverURL: URL?
    private(set) var fingerprint: String?
    private(set) var sources: [SourceStatus] = []
    var lastError: String?

    /// Source that receives this device's photos (a device-upload source).
    var backupSource: String {
        get { UserDefaults.standard.string(forKey: "backupSource") ?? "iphone" }
        set { UserDefaults.standard.set(newValue, forKey: "backupSource") }
    }

    var isPaired: Bool { client?.token != nil }

    init() {
        if let s = SharedStore.settings, let c = SharedStore.makeClient() {
            serverURL = s.serverURL
            fingerprint = s.fingerprint
            client = c
        }
    }

    /// Saves a completed pairing.
    func paired(url: URL, fingerprint: String?, client: HomeBackupClient) {
        SharedStore.settings = SharedStore.Settings(serverURL: url, fingerprint: fingerprint)
        SharedStore.token = client.token
        serverURL = url
        self.fingerprint = fingerprint
        self.client = client
        Task { await refreshSources() }
    }

    func unpair() {
        SharedStore.clear()
        FileProviderDomains.removeAll()
        client = nil
        serverURL = nil
        fingerprint = nil
        sources = []
        PhotoIndex.shared.reset()
    }

    func refreshSources() async {
        guard let client else { return }
        do {
            sources = try await client.sources()
            if var s = SharedStore.settings {
                s.writableSources = sources.filter(\.isWritable).map(\.id)
                SharedStore.settings = s
            }
            await FileProviderDomains.sync(sources)
            if !sources.contains(where: { $0.id == backupSource }),
               let first = sources.first(where: { $0.type == "device-upload" }) {
                backupSource = first.id
            }
            lastError = nil
        } catch {
            lastError = error.localizedDescription
        }
    }

    /// Sources shown in Photos: device uploads and Google Photos imports.
    var photoSources: [SourceStatus] {
        sources.filter { $0.type == "device-upload" || $0.type == "gphotos-takeout" }
    }

    /// Writable "My Files" drives.
    var driveSources: [SourceStatus] { sources.filter(\.isWritable) }

    /// All sources, for browsing (Google Drive mirror included).
    var browsableSources: [SourceStatus] { sources }
}
