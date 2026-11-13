import BackgroundTasks
import Foundation
import HomeBackupKit
import Observation
import Photos

/// Backs up the photo library (including iCloud Photos originals) to the
/// home server. Each asset is uploaded once; the server dedupes by the
/// asset's identifier, so reinstalling the app does not upload twice.
@MainActor @Observable
final class BackupManager {
    static let shared = BackupManager()
    static let taskID = "com.github.leohchen.homebackup.backup"

    var enabled: Bool {
        didSet { UserDefaults.standard.set(enabled, forKey: "backupEnabled") }
    }
    private(set) var running = false
    private(set) var total = 0
    private(set) var done = 0
    private(set) var current: String?
    private(set) var lastError: String?
    private(set) var lastRun: Date?
    private(set) var authorization = PHPhotoLibrary.authorizationStatus(for: .readWrite)

    private var uploaded: Set<String>
    private let uploadedFile = FileManager.default.urls(for: .applicationSupportDirectory, in: .userDomainMask)[0]
        .appendingPathComponent("uploaded-assets.json")
    private var task: Task<Void, Never>?

    init() {
        enabled = UserDefaults.standard.bool(forKey: "backupEnabled")
        if let data = try? Data(contentsOf: uploadedFile), let s = try? JSONDecoder().decode(Set<String>.self, from: data) {
            uploaded = s
        } else {
            uploaded = []
        }
    }

    var remaining: Int { max(0, total - done) }

    func requestAccess() async {
        authorization = await PHPhotoLibrary.requestAuthorization(for: .readWrite)
    }

    // MARK: Background

    static func registerBackgroundTask() {
        BGTaskScheduler.shared.register(forTaskWithIdentifier: taskID, using: nil) { task in
            guard let task = task as? BGProcessingTask else { return }
            Task { @MainActor in
                let manager = BackupManager.shared
                task.expirationHandler = { Task { @MainActor in manager.stop() } }
                await manager.runNow()
                task.setTaskCompleted(success: manager.lastError == nil)
                manager.scheduleBackground()
            }
        }
    }

    func scheduleBackground() {
        guard enabled else { return }
        let req = BGProcessingTaskRequest(identifier: Self.taskID)
        req.requiresNetworkConnectivity = true
        req.requiresExternalPower = false
        req.earliestBeginDate = Date(timeIntervalSinceNow: 30 * 60)
        try? BGTaskScheduler.shared.submit(req)
    }

    // MARK: Running

    func start() {
        guard enabled, task == nil else { return }
        task = Task { await runNow() }
    }

    func stop() {
        task?.cancel()
        task = nil
    }

    func runNow() async {
        guard !running, let client = AppModel.shared.client else { return }
        if authorization != .authorized && authorization != .limited { await requestAccess() }
        guard authorization == .authorized || authorization == .limited else {
            lastError = "Allow access to Photos in Settings to back up."
            return
        }
        running = true
        lastError = nil
        defer {
            running = false
            current = nil
            lastRun = Date()
            task = nil
            saveUploaded()
        }

        let opts = PHFetchOptions()
        opts.sortDescriptors = [NSSortDescriptor(key: "creationDate", ascending: false)]
        let result = PHAsset.fetchAssets(with: opts)
        var pending: [PHAsset] = []
        result.enumerateObjects { asset, _, _ in
            if !self.uploaded.contains(asset.localIdentifier) { pending.append(asset) }
        }
        total = result.count
        done = total - pending.count

        let source = AppModel.shared.backupSource
        for asset in pending {
            if Task.isCancelled { break }
            do {
                try await upload(asset, to: source, client: client)
                uploaded.insert(asset.localIdentifier)
                done += 1
                if done % 20 == 0 { saveUploaded() }
            } catch is CancellationError {
                break
            } catch {
                lastError = error.localizedDescription
                if (error as? URLError)?.code == .notConnectedToInternet || (error as? URLError)?.code == .cannotConnectToHost {
                    break // server unreachable: try again later
                }
            }
        }
        if done > 0 { await PhotoIndex.shared.refresh() }
    }

    private func upload(_ asset: PHAsset, to source: String, client: HomeBackupClient) async throws {
        let resources = PHAssetResource.assetResources(for: asset)
        // The original photo or video (not edits); Live Photos also get their video.
        let primaryTypes: [PHAssetResourceType] = asset.mediaType == .video ? [.video] : [.photo]
        var picks = resources.filter { primaryTypes.contains($0.type) }
        if picks.isEmpty, let first = resources.first { picks = [first] }
        picks += resources.filter { $0.type == .pairedVideo }

        let date = asset.creationDate ?? Date()
        let comps = Calendar(identifier: .gregorian).dateComponents(in: .current, from: date)
        let folder = String(format: "%04d/%02d", comps.year ?? 1970, comps.month ?? 1)

        for res in picks {
            current = res.originalFilename
            let tmp = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
            defer { try? FileManager.default.removeItem(at: tmp) }
            let opts = PHAssetResourceRequestOptions()
            opts.isNetworkAccessAllowed = true // iCloud Photos: download the original
            try await PHAssetResourceManager.default().writeData(for: res, toFile: tmp, options: opts)
            let assetID = res.type == .pairedVideo ? asset.localIdentifier + "/video" : asset.localIdentifier
            _ = try await client.upload(UploadRequest(fileURL: tmp, source: source,
                                                      path: folder + "/" + res.originalFilename,
                                                      assetId: assetID, created: date))
        }
    }

    private func saveUploaded() {
        try? FileManager.default.createDirectory(at: uploadedFile.deletingLastPathComponent(), withIntermediateDirectories: true)
        if let data = try? JSONEncoder().encode(uploaded) { try? data.write(to: uploadedFile, options: .atomic) }
    }

    /// Forgets which assets were uploaded (the server still dedupes).
    func resetUploaded() {
        uploaded = []
        saveUploaded()
    }
}
