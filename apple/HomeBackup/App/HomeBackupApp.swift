import SwiftUI

@main
struct HomeBackupApp: App {
    @Environment(\.scenePhase) private var phase

    init() {
        BackupManager.registerBackgroundTask()
    }

    var body: some Scene {
        WindowGroup {
            RootView()
        }
        .onChange(of: phase) { _, newPhase in
            switch newPhase {
            case .active:
                BackupManager.shared.start()
            case .background:
                BackupManager.shared.scheduleBackground()
            default:
                break
            }
        }
    }
}

struct RootView: View {
    @State private var model = AppModel.shared

    var body: some View {
        if model.isPaired {
            TabView {
                PhotosView()
                    .tabItem { Label("Photos", systemImage: "photo.on.rectangle") }
                FilesView()
                    .tabItem { Label("Files", systemImage: "folder") }
                SettingsView()
                    .tabItem { Label("Settings", systemImage: "gear") }
            }
            .task { await model.refreshSources() }
        } else {
            PairingView()
        }
    }
}
