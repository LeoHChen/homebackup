import HomeBackupKit
import SwiftUI

/// Finds the home server and pairs this device with the code from
/// `homebackupd pair`, after the person compares the server key.
struct PairingView: View {
    @State private var servers: [DiscoveredServer] = []
    @State private var manualURL = ""
    @State private var target: URL?
    @State private var advertisedKey: String?
    @State private var client: HomeBackupClient?
    @State private var seenKey: String?
    @State private var code = ""
    @State private var busy = false
    @State private var error: String?

    var body: some View {
        NavigationStack {
            Form {
                if target == nil {
                    Section("Servers on this network") {
                        if servers.isEmpty {
                            HStack { ProgressView(); Text("Looking for your home server…").foregroundStyle(.secondary) }
                        }
                        ForEach(servers, id: \.self) { s in
                            Button(s.name) { Task { await choose(s) } }
                        }
                    }
                    Section("Or enter its address") {
                        TextField("https://mac-mini.local:8420", text: $manualURL)
                            .textInputAutocapitalization(.never)
                            .keyboardType(.URL)
                            .autocorrectionDisabled()
                        Button("Connect") {
                            if let url = URL(string: manualURL.trimmingCharacters(in: .whitespaces)) {
                                Task { await connect(url, advertised: nil) }
                            }
                        }
                        .disabled(URL(string: manualURL) == nil || busy)
                    }
                } else {
                    Section {
                        LabeledContent("Server", value: target?.host() ?? "")
                        if let seenKey {
                            LabeledContent("Server key", value: Pinning.short(seenKey))
                                .font(.body.monospaced())
                        } else {
                            LabeledContent("Server key", value: "None (not encrypted)")
                        }
                    } footer: {
                        Text("On the server, run `homebackupd pair`. Check that the server key it prints matches, then enter the code.")
                    }
                    Section("Pairing code") {
                        TextField("123 456", text: $code)
                            .keyboardType(.numberPad)
                            .font(.title2.monospaced())
                        Button("Pair") { Task { await pair() } }
                            .disabled(code.filter(\.isNumber).count != 6 || busy)
                        Button("Choose another server", role: .cancel) { target = nil; client = nil }
                    }
                }
                if let error {
                    Section { Text(error).foregroundStyle(.red) }
                }
            }
            .navigationTitle("Connect to HomeBackup")
            .task {
                for await list in ServerBrowser().servers() { servers = list }
            }
        }
    }

    private func choose(_ s: DiscoveredServer) async {
        busy = true
        defer { busy = false }
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
            if let advertised, let seen, advertised.lowercased() != seen {
                error = "The server's key does not match the one it advertised. Do not pair."
                return
            }
            if let reported = health.certFingerprint, let seen, reported.lowercased() != seen {
                error = "The server's key does not match. Do not pair."
                return
            }
            client = c
            seenKey = seen
            advertisedKey = advertised
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
            let r = try await client.pair(code: code.filter(\.isNumber), deviceName: UIDevice.current.name, platform: "ios")
            if let fp = r.certFingerprint, let seenKey, fp.lowercased() != seenKey {
                error = "The server's key changed while pairing. Do not use this server."
                return
            }
            AppModel.shared.paired(url: target, fingerprint: seenKey, client: client)
        } catch let e as APIError where e.status == 401 {
            error = "That code is wrong or expired. Run `homebackupd pair` again."
        } catch {
            self.error = error.localizedDescription
        }
    }
}
