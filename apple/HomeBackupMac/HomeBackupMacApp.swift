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
                .frame(width: 460)
        }
        .windowResizability(.contentSize)

        Window("Pair another device", id: "paircode") {
            MacPairCodeView()
                .frame(width: 440)
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
    private(set) var status: ServerStatus?
    /// What went wrong the last time we talked to the server, in plain words.
    private(set) var lastError: String?
    /// True when the server no longer accepts this Mac's key.
    private(set) var needsRepair = false
    /// A problem on the Finder side, in plain words.
    private(set) var finderError: String?
    /// True when macOS has HomeBackup turned off under File Providers, so
    /// Finder cannot show anything until the person turns it on.
    private(set) var finderDisabled = false
    private(set) var lastCheck: Date?
    private(set) var checking = false
    private var timer: Task<Void, Never>?

    init() {
        settings = SharedStore.settings
        client = SharedStore.makeClient()
        startPolling()
    }

    var isPaired: Bool { client != nil }

    var serverName: String {
        status?.hostname ?? settings?.serverURL.host() ?? "your home server"
    }

    var statusSymbol: String {
        if !isPaired { return "externaldrive.badge.questionmark" }
        if lastError != nil || finderDisabled { return "externaldrive.badge.exclamationmark" }
        if sources.contains(where: \.running) { return "arrow.triangle.2.circlepath" }
        return "externaldrive.badge.checkmark"
    }

    func paired(url: URL, fingerprint: String?, client: HomeBackupClient) {
        SharedStore.settings = SharedStore.Settings(serverURL: url, fingerprint: fingerprint)
        SharedStore.token = client.token
        settings = SharedStore.settings
        self.client = client
        lastError = nil
        needsRepair = false
        Task { await refresh() }
    }

    func disconnect() {
        SharedStore.clear()
        NSFileProviderManager.removeAllDomains { _ in }
        client = nil
        settings = nil
        sources = []
        status = nil
        lastError = nil
        finderError = nil
        finderDisabled = false
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
        guard let client, !checking else { return }
        checking = true
        defer { checking = false }
        do {
            sources = try await client.sources()
            if var s = SharedStore.settings {
                s.writableSources = sources.filter(\.isWritable).map(\.id)
                SharedStore.settings = s
                settings = s
            }
            // Older servers answer /status with fewer fields; a failure here
            // is not worth an error in the menu.
            status = try? await client.status()
            await syncDomains()
            lastError = nil
            needsRepair = false
        } catch {
            needsRepair = (error as? APIError)?.status == 401
            lastError = UserMessage.describe(error, server: settings?.serverURL.host())
        }
        lastCheck = Date()
    }

    /// One Finder location per source; asks Finder to pick up changes.
    private func syncDomains() async {
        let wanted = Dictionary(uniqueKeysWithValues: sources.map { ($0.id, $0.displayName) })
        var existing = (try? await NSFileProviderManager.domains()) ?? []
        for d in existing where wanted[d.identifier.rawValue] == nil {
            try? await NSFileProviderManager.remove(d)
        }
        var addError: String?
        for (id, name) in wanted where !existing.contains(where: { $0.identifier.rawValue == id }) {
            do {
                try await NSFileProviderManager.add(NSFileProviderDomain(identifier: NSFileProviderDomainIdentifier(id), displayName: name))
            } catch {
                addError = "Finder couldn't add \(name): \(error.localizedDescription)"
            }
        }
        existing = (try? await NSFileProviderManager.domains()) ?? []
        finderDisabled = !existing.isEmpty && existing.contains { !$0.userEnabled }
        finderError = addError
        for d in existing where d.userEnabled {
            try? await NSFileProviderManager(for: d)?.signalEnumerator(for: .workingSet)
        }
    }

    func openInFinder(_ source: SourceStatus) {
        if finderDisabled {
            finderError = "Finder access is turned off. Turn on HomeBackup under File Providers in System Settings, then try again."
            return
        }
        let domain = NSFileProviderDomain(identifier: NSFileProviderDomainIdentifier(source.id), displayName: source.displayName)
        NSFileProviderManager(for: domain)?.getUserVisibleURL(for: .rootContainer) { url, _ in
            Task { @MainActor in
                if let url {
                    self.finderError = nil
                    NSWorkspace.shared.open(url)
                } else {
                    self.finderError = "Finder hasn't set up \(source.displayName) yet. Give it a minute and try again."
                }
            }
        }
    }

    func syncNow(_ source: SourceStatus) {
        Task {
            do {
                try await client?.syncNow(source: source.id)
            } catch {
                lastError = UserMessage.describe(error, server: settings?.serverURL.host())
            }
            await refresh()
        }
    }

    /// Opens the File Providers list in System Settings (Login Items &
    /// Extensions on macOS 15 and later, Extensions on macOS 14).
    func openSystemSettings() {
        if let url = URL(string: "x-apple.systempreferences:com.apple.ExtensionsPreferences") {
            NSWorkspace.shared.open(url)
        }
    }

    /// Opens the server's web dashboard in the default browser.
    func openDashboard() {
        if let url = settings?.serverURL {
            NSWorkspace.shared.open(url)
        }
    }
}

struct MenuContent: View {
    let model: MacModel
    @Environment(\.openWindow) private var openWindow
    @State private var confirmDisconnect = false

    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            if let s = model.settings, model.isPaired {
                connected(s)
            } else {
                notConnected
            }
            Divider()
            HStack {
                Text(checkedCaption).font(.caption).foregroundStyle(.secondary)
                Spacer()
                Button("Quit HomeBackup") { NSApp.terminate(nil) }
                    .buttonStyle(.borderless)
                    .keyboardShortcut("q")
            }
        }
        .padding(12)
        .frame(width: 340)
    }

    private var checkedCaption: String {
        if model.checking { return "Checking…" }
        guard let t = model.lastCheck else { return "" }
        let f = RelativeDateTimeFormatter()
        f.unitsStyle = .short
        return "Checked \(f.localizedString(for: t, relativeTo: Date()))"
    }

    @ViewBuilder
    private func connected(_ s: SharedStore.Settings) -> some View {
        HStack(alignment: .top) {
            Image(systemName: "externaldrive.connected.to.line.below").font(.title2)
            VStack(alignment: .leading, spacing: 2) {
                Text(model.serverName).font(.headline)
                Text("Server key " + (s.fingerprint.map(Pinning.short) ?? "none"))
                    .font(.caption.monospaced()).foregroundStyle(.secondary)
            }
            Spacer()
            Button { Task { await model.refresh() } } label: { Image(systemName: "arrow.clockwise") }
                .buttonStyle(.borderless).help("Check now").disabled(model.checking)
        }
        if let disk = model.status?.disk {
            StorageBar(disk: disk)
        }
        Divider()
        if model.finderDisabled {
            notice(title: "Finder can't show your files yet",
                   text: "macOS turned HomeBackup off under File Providers. Turn it on, and your files appear in Finder within a minute.",
                   button: "Open System Settings…", action: model.openSystemSettings)
        }
        if model.sources.isEmpty && model.lastError == nil {
            Text("No backups are set up on the server yet.").font(.caption).foregroundStyle(.secondary)
        }
        ForEach(model.sources) { src in
            SourceRow(source: src, model: model)
        }
        if let e = model.finderError {
            Text(e).font(.caption).foregroundStyle(.orange).fixedSize(horizontal: false, vertical: true)
        }
        if let e = model.lastError {
            notice(title: model.needsRepair ? "Not connected" : "Can't reach \(model.serverName)",
                   text: e,
                   button: model.needsRepair ? "Pair again…" : nil,
                   action: { openWindow(id: "pair"); NSApp.activate(ignoringOtherApps: true) },
                   tint: .red)
        }
        Divider()
        HStack {
            Button("Pair another device…") {
                openWindow(id: "paircode")
                NSApp.activate(ignoringOtherApps: true)
            }
            Spacer()
            Button("Open dashboard") { model.openDashboard() }
                .help("Manage the server in your web browser")
        }
        .buttonStyle(.borderless)
        if confirmDisconnect {
            HStack {
                Text("Remove the Finder locations and forget this server?").font(.caption)
                Spacer()
                Button("Disconnect", role: .destructive) { confirmDisconnect = false; model.disconnect() }
                Button("Cancel") { confirmDisconnect = false }
            }
            .buttonStyle(.borderless)
        } else {
            Button("Disconnect from this server", role: .destructive) { confirmDisconnect = true }
                .buttonStyle(.borderless)
        }
    }

    private var notConnected: some View {
        VStack(alignment: .leading, spacing: 8) {
            Text("Not connected").font(.headline)
            Text("Pair this Mac with your home server to see its files in Finder.")
                .font(.caption).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
            Button("Connect to your home server…") {
                openWindow(id: "pair")
                NSApp.activate(ignoringOtherApps: true)
            }
        }
    }

    private func notice(title: String, text: String, button: String?, action: @escaping () -> Void, tint: Color = .orange) -> some View {
        VStack(alignment: .leading, spacing: 6) {
            Label(title, systemImage: "exclamationmark.triangle.fill")
                .font(.callout.bold()).foregroundStyle(tint)
            Text(text).font(.caption).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
            if let button {
                Button(button, action: action).controlSize(.small)
            }
        }
        .padding(8)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(tint.opacity(0.12), in: RoundedRectangle(cornerRadius: 8))
    }
}

/// Free space on the server's backup drive.
struct StorageBar: View {
    let disk: DiskUsage

    var body: some View {
        VStack(alignment: .leading, spacing: 3) {
            HStack {
                Text("Storage").font(.caption.bold())
                Spacer()
                Text("\(Self.format(disk.free)) free of \(Self.format(disk.total))")
                    .font(.caption).foregroundStyle(.secondary)
            }
            ProgressView(value: Double(disk.used), total: Double(max(disk.total, 1)))
                .tint(disk.fractionUsed > 0.9 ? .red : .accentColor)
            if disk.fractionUsed > 0.9 {
                Text("The backup drive is almost full.").font(.caption).foregroundStyle(.red)
            }
        }
    }

    static func format(_ bytes: UInt64) -> String {
        let f = ByteCountFormatter()
        f.countStyle = .file
        return f.string(fromByteCount: Int64(clamping: bytes))
    }
}

struct SourceRow: View {
    let source: SourceStatus
    let model: MacModel

    var body: some View {
        HStack {
            VStack(alignment: .leading) {
                Text(source.displayName)
                Text(detail).font(.caption).foregroundStyle(source.lastError == nil ? Color.secondary : Color.orange).lineLimit(2)
            }
            Spacer()
            Button { model.openInFinder(source) } label: { Image(systemName: "folder") }
                .help("Show in Finder")
            if !source.isWritable && source.type != "device-upload" {
                Button { model.syncNow(source) } label: { Image(systemName: "arrow.clockwise") }
                    .help("Sync now")
                    .disabled(source.running)
            }
        }
        .buttonStyle(.borderless)
    }

    private var detail: String {
        if source.running { return "Syncing…" }
        if let e = source.lastError { return "Problem: \(e)" }
        if let t = source.lastSync {
            let f = RelativeDateTimeFormatter()
            f.unitsStyle = .short
            return "Synced \(f.localizedString(for: t, relativeTo: Date()))"
        }
        switch source.type {
        case "drive": return "Your files, synced with Finder"
        case "device-upload": return "Receives photos from your phone"
        case "gphotos-takeout": return "Waiting for Takeout files on the server"
        default: return "Not synced yet"
        }
    }
}
