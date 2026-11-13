import FileProvider
import HomeBackupKit

/// Shows each server source as a location in the Files app.
enum FileProviderDomains {
    static func sync(_ sources: [SourceStatus]) async {
        let wanted = Dictionary(uniqueKeysWithValues: sources.map { ($0.id, FilesView.title($0)) })
        let existing = (try? await NSFileProviderManager.domains()) ?? []
        for d in existing where wanted[d.identifier.rawValue] == nil {
            try? await NSFileProviderManager.remove(d)
        }
        for (id, title) in wanted where !existing.contains(where: { $0.identifier.rawValue == id }) {
            try? await NSFileProviderManager.add(NSFileProviderDomain(identifier: NSFileProviderDomainIdentifier(id), displayName: title))
        }
        signalAll()
    }

    /// Asks the Files app to fetch changes from the server.
    static func signalAll() {
        NSFileProviderManager.getDomainsWithCompletionHandler { domains, _ in
            for d in domains {
                NSFileProviderManager(for: d)?.signalEnumerator(for: .workingSet) { _ in }
            }
        }
    }

    static func removeAll() {
        NSFileProviderManager.removeAllDomains { _ in }
    }
}
