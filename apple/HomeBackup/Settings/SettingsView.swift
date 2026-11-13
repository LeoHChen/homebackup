import HomeBackupKit
import SwiftUI

struct SettingsView: View {
    @State private var model = AppModel.shared
    @State private var backup = BackupManager.shared
    @State private var confirmUnpair = false

    var body: some View {
        NavigationStack {
            Form {
                Section("Photo backup") {
                    Toggle("Back up this device's photos", isOn: Binding(
                        get: { backup.enabled },
                        set: { on in
                            backup.enabled = on
                            if on { backup.start() } else { backup.stop() }
                        }))
                    if !model.photoSources.filter({ $0.type == "device-upload" }).isEmpty {
                        Picker("Save to", selection: Binding(get: { model.backupSource }, set: { model.backupSource = $0 })) {
                            ForEach(model.sources.filter { $0.type == "device-upload" }) { Text($0.id).tag($0.id) }
                        }
                    }
                    if backup.running {
                        LabeledContent("Status", value: "\(backup.done) of \(backup.total)")
                        if let c = backup.current { LabeledContent("Uploading", value: c) }
                    } else if backup.total > 0 {
                        LabeledContent("Status", value: backup.remaining == 0 ? "Up to date" : "\(backup.remaining) waiting")
                    }
                    if let last = backup.lastRun {
                        LabeledContent("Last run", value: last.formatted(date: .abbreviated, time: .shortened))
                    }
                    if let err = backup.lastError { Text(err).foregroundStyle(.red).font(.footnote) }
                    Button("Back up now") { Task { await backup.runNow() } }
                        .disabled(backup.running || !backup.enabled)
                }
                Section("Server") {
                    LabeledContent("Address", value: model.serverURL?.absoluteString ?? "")
                    LabeledContent("Server key", value: model.fingerprint.map(Pinning.short) ?? "None")
                        .font(.body.monospaced())
                    ForEach(model.sources) { s in
                        LabeledContent(FilesView.title(s)) {
                            if s.running { ProgressView() }
                            else if s.lastError != nil { Image(systemName: "exclamationmark.triangle").foregroundStyle(.orange) }
                            else if let t = s.lastSync { Text(t, style: .relative) }
                        }
                    }
                    if let err = model.lastError { Text(err).foregroundStyle(.red).font(.footnote) }
                }
                Section {
                    Button("Disconnect from this server", role: .destructive) { confirmUnpair = true }
                }
            }
            .navigationTitle("Settings")
            .refreshable { await model.refreshSources() }
            .confirmationDialog("Disconnect?", isPresented: $confirmUnpair) {
                Button("Disconnect", role: .destructive) {
                    backup.stop()
                    model.unpair()
                }
            } message: {
                Text("Backed-up files stay on the server. You can pair again with a new code.")
            }
        }
    }
}
