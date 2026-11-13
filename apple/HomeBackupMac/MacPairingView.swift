import HomeBackupKit
import SwiftUI

/// Finds the server, shows its key to compare with `homebackupd pair`, and
/// pairs with the 6-digit code.
struct MacPairingView: View {
    @Environment(\.dismiss) private var dismiss
    @State private var servers: [DiscoveredServer] = []
    @State private var address = ""
    @State private var target: URL?
    @State private var client: HomeBackupClient?
    @State private var seenKey: String?
    @State private var code = ""
    @State private var busy = false
    @State private var error: String?
    @State private var lookedLongEnough = false
    @State private var done = false

    var body: some View {
        Form {
            if done {
                doneSection
            } else if target == nil {
                chooseServer
            } else {
                enterCode
            }
            if let error {
                Section {
                    Label(error, systemImage: "exclamationmark.triangle.fill")
                        .foregroundStyle(.red)
                        .fixedSize(horizontal: false, vertical: true)
                }
            }
        }
        .formStyle(.grouped)
        .task {
            for await list in ServerBrowser().servers() { servers = list }
        }
        .task {
            try? await Task.sleep(for: .seconds(6))
            lookedLongEnough = true
        }
    }

    @ViewBuilder
    private var chooseServer: some View {
        Section {
            Text("Three steps: pick your server, compare its key, enter the pairing code.")
                .font(.callout).foregroundStyle(.secondary)
        }
        Section("1. Pick your home server") {
            if servers.isEmpty {
                HStack {
                    ProgressView().controlSize(.small)
                    Text(lookedLongEnough ? "No server found on this network yet." : "Looking on this network…")
                        .foregroundStyle(.secondary)
                }
                if lookedLongEnough {
                    Text("Make sure homebackupd is running on your server with \"listen\": \":8420\", and that this Mac is on the same network. You can also type the address below.")
                        .font(.caption).foregroundStyle(.secondary)
                        .fixedSize(horizontal: false, vertical: true)
                }
            }
            ForEach(servers, id: \.self) { s in
                Button { Task { await choose(s) } } label: {
                    HStack {
                        Image(systemName: "externaldrive.connected.to.line.below")
                        Text(s.name)
                        Spacer()
                        if let fp = s.fingerprint { Text(Pinning.short(fp)).font(.caption.monospaced()).foregroundStyle(.secondary) }
                        Image(systemName: "chevron.right").foregroundStyle(.tertiary)
                    }
                }
                .buttonStyle(.plain)
                .disabled(busy)
            }
        }
        Section("Or type its address") {
            TextField("Address", text: $address, prompt: Text("https://macmini.local:8420"))
                .onSubmit { connectTyped() }
            Button("Connect") { connectTyped() }
                .disabled(URL(string: address.trimmingCharacters(in: .whitespaces)) == nil || busy)
        }
    }

    @ViewBuilder
    private var enterCode: some View {
        Section("2. Compare the server key") {
            LabeledContent("Server", value: target?.host() ?? "")
            LabeledContent("Server key", value: seenKey.map(Pinning.short) ?? "None (not encrypted)")
                .font(.body.monospaced())
            Text("The server prints its key when you run `homebackupd pair`, and the dashboard and paired HomeBackup apps show it too. Pair only if the keys match; a different key means something else is answering at this address.")
                .font(.caption).foregroundStyle(.secondary)
                .fixedSize(horizontal: false, vertical: true)
        }
        Section("3. Enter the pairing code") {
            TextField("Code", text: $code, prompt: Text("123 456"))
                .font(.title2.monospaced())
                .onSubmit { Task { await pair() } }
            Text("Get a code from a device that is already paired (HomeBackup menu → Pair another device…, or the dashboard's Devices page), or run `homebackupd pair` on the server. Codes last 10 minutes.")
                .font(.caption).foregroundStyle(.secondary)
                .fixedSize(horizontal: false, vertical: true)
            HStack {
                Button("Back") { target = nil; client = nil; error = nil }
                Spacer()
                if busy { ProgressView().controlSize(.small) }
                Button("Pair") { Task { await pair() } }
                    .keyboardShortcut(.defaultAction)
                    .disabled(code.filter(\.isNumber).count != 6 || busy)
            }
        }
    }

    @ViewBuilder
    private var doneSection: some View {
        Section {
            Label("This Mac is paired with \(target?.host() ?? "your server")", systemImage: "checkmark.circle.fill")
                .font(.headline).foregroundStyle(.green)
            Text("Your files appear in Finder under Locations within a minute. If Finder says HomeBackup doesn't have permission, or nothing shows up, turn on HomeBackup under File Providers in System Settings.")
                .font(.callout).foregroundStyle(.secondary)
                .fixedSize(horizontal: false, vertical: true)
            HStack {
                Button("Open System Settings…") { MacModel.shared.openSystemSettings() }
                Spacer()
                Button("Done") { dismiss() }.keyboardShortcut(.defaultAction)
            }
        }
    }

    private func connectTyped() {
        var text = address.trimmingCharacters(in: .whitespaces)
        if !text.contains("://") { text = "https://" + text }
        if let url = URL(string: text), url.host() != nil {
            address = text
            Task { await connect(url, advertised: nil) }
        } else {
            error = "That doesn't look like a server address. Use the form https://name.local:8420."
        }
    }

    private func choose(_ s: DiscoveredServer) async {
        busy = true
        defer { busy = false }
        do {
            await connect(try await ServerBrowser.resolve(s), advertised: s.fingerprint)
        } catch {
            self.error = "Couldn't look up \(s.name): \(UserMessage.describe(error))"
        }
    }

    private func connect(_ url: URL, advertised: String?) async {
        busy = true
        defer { busy = false }
        error = nil
        let c = HomeBackupClient(baseURL: url, fingerprint: nil, token: nil)
        do {
            let health = try await c.health()
            let seen = c.observedFingerprint
            for expected in [advertised, health.certFingerprint].compactMap({ $0?.lowercased() }) where seen != nil && expected != seen {
                error = "The server's key doesn't match the key it announced. Something else may be answering at this address. Do not pair."
                return
            }
            client = c
            seenKey = seen
            target = url
        } catch {
            self.error = UserMessage.describe(error, server: url.host())
        }
    }

    private func pair() async {
        guard let client, let target else { return }
        busy = true
        defer { busy = false }
        error = nil
        do {
            if let seenKey { client.pin(seenKey) }
            let name = Host.current().localizedName ?? "Mac"
            let r = try await client.pair(code: code.filter(\.isNumber), deviceName: name, platform: "macos")
            if let fp = r.certFingerprint, let seenKey, fp.lowercased() != seenKey {
                error = "The server's key changed while pairing. Do not use this server."
                return
            }
            MacModel.shared.paired(url: target, fingerprint: seenKey, client: client)
            done = true
        } catch let e as APIError where e.status == 401 {
            error = "That code is wrong or expired. Codes last 10 minutes; get a new one and try again."
        } catch let e as APIError where e.status == 429 {
            error = "Too many attempts. Wait a few seconds and try again."
        } catch {
            self.error = UserMessage.describe(error, server: target.host())
        }
    }
}
