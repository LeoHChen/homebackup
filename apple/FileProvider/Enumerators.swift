import FileProvider
import HomeBackupKit

/// Lists one folder.
final class FolderEnumerator: NSObject, NSFileProviderEnumerator {
    let source: String
    let container: NSFileProviderItemIdentifier
    let writable: Bool

    init(source: String, container: NSFileProviderItemIdentifier, writable: Bool) {
        self.source = source
        self.container = container
        self.writable = writable
    }

    func invalidate() {}

    func enumerateItems(for observer: NSFileProviderEnumerationObserver, startingAt page: NSFileProviderPage) {
        Task {
            do {
                guard let c = SharedStore.makeClient() else { throw NSFileProviderError(.notAuthenticated) }
                let path: String
                if container == .rootContainer {
                    path = ""
                } else if let p = Item.impliedPath(container) {
                    path = p
                } else {
                    let f = try await c.file(id: container.rawValue)
                    guard f.isDir, !f.isTrashed else { throw NSFileProviderError(.noSuchItem) }
                    path = f.path
                }
                let items = try await c.list(source: source, path: path)
                observer.didEnumerate(items.map { Item($0, writable: writable) })
                observer.finishEnumerating(upTo: nil)
            } catch {
                observer.finishEnumeratingWithError(mapError(error))
            }
        }
    }

    // Changes are reported through the working set (ChangeEnumerator).
    func enumerateChanges(for observer: NSFileProviderChangeObserver, from anchor: NSFileProviderSyncAnchor) {
        observer.finishEnumeratingChanges(upTo: anchor, moreComing: false)
    }

    func currentSyncAnchor(completionHandler: @escaping (NSFileProviderSyncAnchor?) -> Void) {
        ChangeEnumerator.latestAnchor(source: source, completionHandler: completionHandler)
    }
}

/// Follows the server's change feed for the whole source.
final class ChangeEnumerator: NSObject, NSFileProviderEnumerator {
    let source: String
    let writable: Bool

    init(source: String, writable: Bool) {
        self.source = source
        self.writable = writable
    }

    func invalidate() {}

    func enumerateItems(for observer: NSFileProviderEnumerationObserver, startingAt page: NSFileProviderPage) {
        // Folders are listed when opened; the working set only reports changes.
        observer.finishEnumerating(upTo: nil)
    }

    func enumerateChanges(for observer: NSFileProviderChangeObserver, from anchor: NSFileProviderSyncAnchor) {
        Task {
            do {
                guard let c = SharedStore.makeClient() else { throw NSFileProviderError(.notAuthenticated) }
                let cursor = String(data: anchor.rawValue, encoding: .utf8) ?? "0"
                let page = try await c.changes(source: source, cursor: cursor)
                var updated: [NSFileProviderItem] = []
                var deleted: [NSFileProviderItemIdentifier] = []
                for ch in page.changes {
                    if ch.removed || ch.file.isTrashed {
                        deleted.append(Item.identifier(for: ch.file))
                    } else {
                        updated.append(Item(ch.file, writable: writable))
                    }
                }
                if !updated.isEmpty { observer.didUpdate(updated) }
                if !deleted.isEmpty { observer.didDeleteItems(withIdentifiers: deleted) }
                observer.finishEnumeratingChanges(upTo: NSFileProviderSyncAnchor(Data(page.cursor.utf8)),
                                                  moreComing: page.more)
            } catch {
                observer.finishEnumeratingWithError(mapError(error))
            }
        }
    }

    func currentSyncAnchor(completionHandler: @escaping (NSFileProviderSyncAnchor?) -> Void) {
        Self.latestAnchor(source: source, completionHandler: completionHandler)
    }

    static func latestAnchor(source: String, completionHandler: @escaping (NSFileProviderSyncAnchor?) -> Void) {
        Task {
            guard let c = SharedStore.makeClient(),
                  let page = try? await c.changes(source: source, cursor: "latest") else {
                completionHandler(nil)
                return
            }
            completionHandler(NSFileProviderSyncAnchor(Data(page.cursor.utf8)))
        }
    }
}

final class EmptyEnumerator: NSObject, NSFileProviderEnumerator {
    func invalidate() {}
    func enumerateItems(for observer: NSFileProviderEnumerationObserver, startingAt page: NSFileProviderPage) {
        observer.finishEnumerating(upTo: nil)
    }
    func enumerateChanges(for observer: NSFileProviderChangeObserver, from anchor: NSFileProviderSyncAnchor) {
        observer.finishEnumeratingChanges(upTo: anchor, moreComing: false)
    }
    func currentSyncAnchor(completionHandler: @escaping (NSFileProviderSyncAnchor?) -> Void) {
        completionHandler(NSFileProviderSyncAnchor(Data("0".utf8)))
    }
}
