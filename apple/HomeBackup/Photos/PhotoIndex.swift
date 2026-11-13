import Foundation
import HomeBackupKit
import Observation

/// Every photo and video on the server, newest first, kept up to date with
/// the change feed and cached on disk so the grid opens instantly.
@MainActor @Observable
final class PhotoIndex {
    static let shared = PhotoIndex()

    private(set) var items: [FileItem] = []
    private(set) var isRefreshing = false
    var lastError: String?

    private struct Saved: Codable {
        var cursors: [String: String]
        var items: [String: FileItem] // "source/id" -> item
    }
    private var saved = Saved(cursors: [:], items: [:])
    private let file = FileManager.default.urls(for: .applicationSupportDirectory, in: .userDomainMask)[0]
        .appendingPathComponent("photo-index.json")

    init() {
        if let data = try? Data(contentsOf: file), let s = try? JSONDecoder().decode(Saved.self, from: data) {
            saved = s
            rebuild()
        }
    }

    func reset() {
        saved = Saved(cursors: [:], items: [:])
        items = []
        try? FileManager.default.removeItem(at: file)
    }

    func refresh() async {
        let model = AppModel.shared
        guard let client = model.client, !isRefreshing else { return }
        isRefreshing = true
        defer { isRefreshing = false }
        if model.sources.isEmpty { await model.refreshSources() }
        do {
            for src in model.photoSources {
                let (changes, cursor) = try await client.allChanges(source: src.id, since: saved.cursors[src.id] ?? "0")
                for c in changes {
                    let key = src.id + "/" + c.file.id
                    if c.removed || c.file.isTrashed || c.file.isDir || c.file.media == nil {
                        saved.items[key] = nil
                    } else {
                        saved.items[key] = c.file
                    }
                }
                saved.cursors[src.id] = cursor
            }
            rebuild()
            save()
            lastError = nil
        } catch {
            lastError = error.localizedDescription
        }
    }

    private func rebuild() {
        items = saved.items.values.sorted {
            ($0.modified ?? .distantPast, $0.id) > ($1.modified ?? .distantPast, $1.id)
        }
    }

    private func save() {
        try? FileManager.default.createDirectory(at: file.deletingLastPathComponent(), withIntermediateDirectories: true)
        if let data = try? JSONEncoder().encode(saved) { try? data.write(to: file, options: .atomic) }
    }
}
