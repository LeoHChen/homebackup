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

    var body: some View {
        Form {
            if target == nil {
                Section("Servers on this network") {
                    if servers.isEmpty {
                        HStack { ProgressView().controlSize(.small); Text("Looking…").foregroundStyle(.secondary) }
                    }
                    ForEach(servers, id: \.self) { s in
                        Button(s.name) { Task { await choose(s) } }
                    }
                }
                Section("Or enter its address") {
                    TextField("Address", text: $address, prompt: Text("https://your-server.local:8420"))
                    Button("Connect") {
                        if let url = URL(string: address.trimmingCharacters(in: .whitespaces)) {
                            Task { await connect(url, advertised: nil) }
                        }
                    }
                    .disabled(URL(string: address) == nil || busy)
                }
            } else {
                Section {
                    LabeledContent("Server", value: target?.host() ?? "")
                    LabeledContent("Server key", value: seenKey.map(Pinning.short) ?? "None (not encrypted)")
                        .font(.body.monospaced())
                    Text("On the server, run `homebackupd pair`. Check that the server key matches, then enter the code.")
                        .font(.caption).foregroundStyle(.secondary)
                }
                Section("Pairing code") {
                    TextField("Code", text: $code, prompt: Text("123 456")).font(.title2.monospaced())
                    HStack {
                        Button("Back") { target = nil; client = nil }
                        Spacer()
                        Button("Pair") { Task { await pair() } }
                            .keyboardShortcut(.defaultAction)
                            .disabled(code.filter(\.isNumber).count != 6 || busy)
                    }
                }
            }
            if let error {
                Text(error).foregroundStyle(.red)
            }
        }
        .formStyle(.grouped)
        .task {
            for await list in ServerBrowser().servers() { servers = list }
        }
    }

    private func choose(_ s: DiscoveredServer) async {
        do {
            await connect(try await ServerBrowser.resolve(s), advertised: s.fingerprint)
        } catch {
            self.error = error.localizedDescription
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
                error = "The server's key does not match. Do not pair."
                return
            }
            client = c
            seenKey = seen
            target = url
        } catch {
            self.error = "Cannot reach \(url.host() ?? url.absoluteString): \(error.localizedDescription)"
        }
    }

    private func pair() async {
        guard let client, let target else { return }
        busy = true
        defer { busy = false }
        do {
            if let seenKey { client.pin(seenKey) }
            let name = Host.current().localizedName ?? "Mac"
            let r = try await client.pair(code: code.filter(\.isNumber), deviceName: name, platform: "macos")
            if let fp = r.certFingerprint, let seenKey, fp.lowercased() != seenKey {
                error = "The server's key changed while pairing. Do not use this server."
                return
            }
            MacModel.shared.paired(url: target, fingerprint: seenKey, client: client)
            dismiss()
        } catch let e as APIError where e.status == 401 {
            error = "That code is wrong or expired. Run `homebackupd pair` again."
        } catch {
            self.error = error.localizedDescription
        }
    }
}
