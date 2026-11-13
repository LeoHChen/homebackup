import Combine
import HomeBackupKit
import SwiftUI

/// Shows a pairing code for a new iPhone, Mac, or browser, so nobody needs a
/// terminal on the server.
struct MacPairCodeView: View {
    @Environment(\.dismiss) private var dismiss
    @State private var code: PairCode?
    @State private var error: String?
    @State private var busy = false
    @State private var now = Date()
    private let tick = Timer.publish(every: 1, on: .main, in: .common).autoconnect()

    var body: some View {
        VStack(alignment: .leading, spacing: 14) {
            Text("Pair another device").font(.title2.bold())
            Text("Enter this code on the new device within 10 minutes. It can be used once.")
                .font(.callout).foregroundStyle(.secondary)
            if let code {
                VStack(spacing: 4) {
                    Text(code.formatted)
                        .font(.system(size: 44, weight: .semibold, design: .monospaced))
                        .textSelection(.enabled)
                    Text(remaining(code)).font(.caption).foregroundStyle(expired(code) ? Color.red : Color.secondary)
                }
                .frame(maxWidth: .infinity)
                if let fp = MacModel.shared.settings?.fingerprint {
                    GroupBox {
                        LabeledContent("Server key", value: Pinning.short(fp)).font(.body.monospaced())
                        Text("The new device must show this same key before you enter the code.")
                            .font(.caption).foregroundStyle(.secondary)
                    }
                }
                steps
            } else if let error {
                Label(error, systemImage: "exclamationmark.triangle.fill")
                    .foregroundStyle(.red)
                    .fixedSize(horizontal: false, vertical: true)
            } else {
                HStack { ProgressView().controlSize(.small); Text("Asking \(MacModel.shared.serverName) for a code…") }
                    .frame(maxWidth: .infinity)
            }
            HStack {
                Button("New code") { Task { await fetch() } }.disabled(busy)
                Spacer()
                Button("Done") { dismiss() }.keyboardShortcut(.defaultAction)
            }
        }
        .padding(20)
        .task { await fetch() }
        .onReceive(tick) { now = $0 }
    }

    private var steps: some View {
        VStack(alignment: .leading, spacing: 6) {
            Text("On the new device").font(.caption.bold()).foregroundStyle(.secondary)
            Label("iPhone or iPad: open HomeBackup, choose \(MacModel.shared.serverName), check the key, and enter the code.", systemImage: "iphone")
            Label("Another Mac: install HomeBackup, click its menu bar icon, then Connect to your home server…", systemImage: "laptopcomputer")
            if let url = MacModel.shared.settings?.serverURL {
                Label("Web browser: open \(url.absoluteString) and choose Pair this browser.", systemImage: "globe")
            }
        }
        .font(.caption)
        .fixedSize(horizontal: false, vertical: true)
    }

    private func fetch() async {
        guard let c = MacModel.shared.client else {
            error = "This Mac isn't connected to a server."
            return
        }
        busy = true
        defer { busy = false }
        do {
            code = try await c.newPairCode()
            error = nil
        } catch let e as APIError where e.status == 404 {
            code = nil
            error = "Your server is too old to hand out codes. Update homebackupd on the server, or run `homebackupd pair` there."
        } catch {
            code = nil
            self.error = UserMessage.describe(error, server: MacModel.shared.settings?.serverURL.host())
        }
    }

    private func expired(_ c: PairCode) -> Bool { c.expiresAt <= now }

    private func remaining(_ c: PairCode) -> String {
        let s = Int(c.expiresAt.timeIntervalSince(now))
        if s <= 0 { return "This code expired. Ask for a new one." }
        return String(format: "Valid for %d:%02d", s / 60, s % 60)
    }
}
