import FileProvider
import HomeBackupKit
import SwiftUI

/// Menu bar app: pairs this Mac with the home server and shows each source
/// in Finder (File Provider), like iCloud Drive or Google Drive.
@main
struct HomeBackupMacApp: App {
    @State private var model = MacModel.shared

    var body: some Scene {
        MenuBarExtra("HomeBackup", systemImage: model.statusSymbol) {
            MenuContent(model: model)
        }
        .menuBarExtraStyle(.window)

        Window("Connect to HomeBackup", id: "pair") {
            MacPairingView()
                .frame(width: 420)
        }
        .windowResizability(.contentSize)
    }
}

@MainActor @Observable
final class MacModel {
    static let shared = MacModel()

    private(set) var client: HomeBackupClient?
    private(set) var settings: SharedStore.Settings?
    private(set) var sources: [SourceStatus] = []
    private(set) var lastError: String?
    private(set) var lastCheck: Date?
    private var timer: Task<Void, Never>?

    init() {
        settings = SharedStore.settings
        client = SharedStore.makeClient()
        startPolling()
    }

    var isPaired: Bool { client != nil }

    var statusSymbol: String {
        if !isPaired { return "externaldrive.badge.questionmark" }
        if lastError != nil { return "externaldrive.badge.exclamationmark" }
        if sources.contains(where: \.running) { return "arrow.triangle.2.circlepath" }
        return "externaldrive.badge.checkmark"
    }

    func paired(url: URL, fingerprint: String?, client: HomeBackupClient) {
        SharedStore.settings = SharedStore.Settings(serverURL: url, fingerprint: fingerprint)
        SharedStore.token = client.token
        settings = SharedStore.settings
        self.client = client
        Task { await refresh() }
    }

    func disconnect() {
        SharedStore.clear()
        NSFileProviderManager.removeAllDomains { _ in }
        client = nil
        settings = nil
        sources = []
    }

    private func startPolling() {
        timer = Task {
            while !Task.isCancelled {
                await refresh()
                try? await Task.sleep(for: .seconds(60))
            }
        }
    }

    func refresh() async {
        guard let client else { return }
        do {
            sources = try await client.sources()
            if var s = SharedStore.settings {
                s.writableSources = sources.filter(\.isWritable).map(\.id)
                SharedStore.settings = s
                settings = s
            }
            await syncDomains()
            lastError = nil
        } catch {
            lastError = error.localizedDescription
        }
        lastCheck = Date()
    }

    /// One Finder location per source; asks Finder to pick up changes.
    private func syncDomains() async {
        let wanted = Dictionary(uniqueKeysWithValues: sources.map { ($0.id, $0.displayName) })
        let existing = (try? await NSFileProviderManager.domains()) ?? []
        for d in existing where wanted[d.identifier.rawValue] == nil {
            try? await NSFileProviderManager.remove(d)
        }
        for (id, name) in wanted where !existing.contains(where: { $0.identifier.rawValue == id }) {
            do {
                try await NSFileProviderManager.add(NSFileProviderDomain(identifier: NSFileProviderDomainIdentifier(id), displayName: name))
            } catch {
                lastError = "Finder: \(error.localizedDescription)"
            }
        }
        for d in (try? await NSFileProviderManager.domains()) ?? [] {
            try? await NSFileProviderManager(for: d)?.signalEnumerator(for: .workingSet)
        }
    }

    func openInFinder(_ source: SourceStatus) {
        let domain = NSFileProviderDomain(identifier: NSFileProviderDomainIdentifier(source.id), displayName: source.displayName)
        NSFileProviderManager(for: domain)?.getUserVisibleURL(for: .rootContainer) { url, error in
            Task { @MainActor in
                if let url {
                    NSWorkspace.shared.open(url)
                } else {
                    self.lastError = error?.localizedDescription ?? "Not available in Finder yet."
                }
            }
        }
    }

    func syncNow(_ source: SourceStatus) {
        Task {
            try? await client?.syncNow(source: source.id)
            await refresh()
        }
    }
}

struct MenuContent: View {
    let model: MacModel
    @Environment(\.openWindow) private var openWindow

    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            if let s = model.settings, model.isPaired {
                HStack {
                    Image(systemName: "externaldrive.connected.to.line.below")
                    VStack(alignment: .leading) {
                        Text(s.serverURL.host() ?? s.serverURL.absoluteString).font(.headline)
                        Text("Server key " + (s.fingerprint.map(Pinning.short) ?? "none"))
                            .font(.caption.monospaced()).foregroundStyle(.secondary)
                    }
                }
                Divider()
                ForEach(model.sources) { src in
                    HStack {
                        VStack(alignment: .leading) {
                            Text(src.displayName)
                            Group {
                                if src.running { Text("Syncing…") }
                                else if let e = src.lastError { Text(e).foregroundStyle(.orange) }
                                else if let t = src.lastSync { Text("Synced ") + Text(t, style: .relative) + Text(" ago") }
                                else { Text("Not synced yet") }
                            }
                            .font(.caption).foregroundStyle(.secondary).lineLimit(1)
                        }
                        Spacer()
                        Button { model.openInFinder(src) } label: { Image(systemName: "folder") }
                            .help("Show in Finder")
                        if !src.isWritable && src.type != "device-upload" {
                            Button { model.syncNow(src) } label: { Image(systemName: "arrow.clockwise") }
                                .help("Sync now")
                        }
                    }
                    .buttonStyle(.borderless)
                }
                if let e = model.lastError {
                    Text(e).font(.caption).foregroundStyle(.red)
                }
                Divider()
                Button("Disconnect from this server", role: .destructive) { model.disconnect() }
                    .buttonStyle(.borderless)
            } else {
                Text("Not connected").font(.headline)
                Button("Connect to your home server…") {
                    openWindow(id: "pair")
                    NSApp.activate(ignoringOtherApps: true)
                }
            }
            Divider()
            Button("Quit HomeBackup") { NSApp.terminate(nil) }
                .buttonStyle(.borderless)
                .keyboardShortcut("q")
        }
        .padding(12)
        .frame(width: 320)
    }
}
