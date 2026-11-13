import FileProvider
import HomeBackupKit
import UniformTypeIdentifiers

/// Shows one server source (a File Provider domain) in Files / Finder.
/// Files download on demand; edits, new files, renames, moves, and deletes
/// go to the server. Edits made from an out-of-date copy are kept by the
/// server as conflict copies.
final class FileProviderExtension: NSObject, NSFileProviderReplicatedExtension {
    let domain: NSFileProviderDomain
    var source: String { domain.identifier.rawValue }
    var writable: Bool { SharedStore.settings?.writableSources.contains(source) ?? false }

    required init(domain: NSFileProviderDomain) {
        self.domain = domain
        super.init()
    }

    func invalidate() {}

    private func client() throws -> HomeBackupClient {
        guard let c = SharedStore.makeClient() else { throw NSFileProviderError(.notAuthenticated) }
        return c
    }

    // MARK: Items

    func item(for identifier: NSFileProviderItemIdentifier, request: NSFileProviderRequest,
              completionHandler: @escaping (NSFileProviderItem?, Error?) -> Void) -> Progress {
        Task {
            do {
                completionHandler(try await self.item(identifier), nil)
            } catch {
                completionHandler(nil, mapError(error))
            }
        }
        return Progress()
    }

    private func item(_ identifier: NSFileProviderItemIdentifier) async throws -> Item {
        if identifier == .rootContainer { return Item(rootOf: source, writable: writable) }
        if let p = Item.impliedPath(identifier) { return Item(impliedFolder: p) }
        let f = try await client().file(id: identifier.rawValue)
        if f.isTrashed { throw NSFileProviderError(.noSuchItem) }
        return Item(f, writable: writable)
    }

    private func path(of identifier: NSFileProviderItemIdentifier) async throws -> String {
        if identifier == .rootContainer { return "" }
        if let p = Item.impliedPath(identifier) { return p }
        return try await client().file(id: identifier.rawValue).path
    }

    func fetchContents(for itemIdentifier: NSFileProviderItemIdentifier, version requestedVersion: NSFileProviderItemVersion?,
                       request: NSFileProviderRequest,
                       completionHandler: @escaping (URL?, NSFileProviderItem?, Error?) -> Void) -> Progress {
        let progress = Progress(totalUnitCount: 1)
        Task {
            do {
                let c = try client()
                let f = try await c.file(id: itemIdentifier.rawValue)
                let tmp = try await c.download(id: f.id)
                let dir = try NSFileProviderManager(for: domain)?.temporaryDirectoryURL() ?? FileManager.default.temporaryDirectory
                let dest = dir.appendingPathComponent(UUID().uuidString)
                try FileManager.default.moveItem(at: tmp, to: dest)
                progress.completedUnitCount = 1
                completionHandler(dest, Item(f, writable: writable), nil)
            } catch {
                completionHandler(nil, nil, mapError(error))
            }
        }
        return progress
    }

    func createItem(basedOn itemTemplate: NSFileProviderItem, fields: NSFileProviderItemFields, contents url: URL?,
                    options: NSFileProviderCreateItemOptions = [], request: NSFileProviderRequest,
                    completionHandler: @escaping (NSFileProviderItem?, NSFileProviderItemFields, Bool, Error?) -> Void) -> Progress {
        Task {
            do {
                guard writable else { throw NSError(domain: NSCocoaErrorDomain, code: NSFileWriteNoPermissionError) }
                let c = try client()
                let parent = try await path(of: itemTemplate.parentItemIdentifier)
                let p = parent.isEmpty ? itemTemplate.filename : parent + "/" + itemTemplate.filename
                let f: FileItem
                if itemTemplate.contentType == .folder {
                    f = try await c.createFolder(source: source, path: p)
                } else if let url {
                    f = try await c.upload(UploadRequest(fileURL: url, source: source, path: p,
                                                         created: itemTemplate.contentModificationDate ?? nil)).file
                } else {
                    throw NSFileProviderError(.noSuchItem)
                }
                completionHandler(Item(f, writable: true), [], false, nil)
            } catch {
                completionHandler(nil, [], false, mapError(error))
            }
        }
        return Progress()
    }

    func modifyItem(_ item: NSFileProviderItem, baseVersion version: NSFileProviderItemVersion,
                    changedFields: NSFileProviderItemFields, contents newContents: URL?,
                    options: NSFileProviderModifyItemOptions = [], request: NSFileProviderRequest,
                    completionHandler: @escaping (NSFileProviderItem?, NSFileProviderItemFields, Bool, Error?) -> Void) -> Progress {
        Task {
            do {
                guard writable else { throw NSError(domain: NSCocoaErrorDomain, code: NSFileWriteNoPermissionError) }
                let c = try client()
                var f = try await c.file(id: item.itemIdentifier.rawValue)
                if changedFields.contains(.contents), let newContents {
                    let base = String(data: version.contentVersion, encoding: .utf8)
                    let r = try await c.upload(UploadRequest(fileURL: newContents, source: source, path: f.path,
                                                             fileId: f.id, baseSha256: base))
                    if r.file.id == f.id {
                        f = r.file
                    } else {
                        // Saved as a conflict copy; it appears with the next change fetch.
                        FileProviderSignal.workingSet(domain)
                    }
                }
                if changedFields.contains(.filename) || changedFields.contains(.parentItemIdentifier) {
                    let parent = try await path(of: item.parentItemIdentifier)
                    let p = parent.isEmpty ? item.filename : parent + "/" + item.filename
                    if p != f.path { f = try await c.move(id: f.id, to: p) }
                }
                completionHandler(Item(f, writable: true), [], false, nil)
            } catch {
                completionHandler(nil, [], false, mapError(error))
            }
        }
        return Progress()
    }

    func deleteItem(identifier: NSFileProviderItemIdentifier, baseVersion version: NSFileProviderItemVersion,
                    options: NSFileProviderDeleteItemOptions = [], request: NSFileProviderRequest,
                    completionHandler: @escaping (Error?) -> Void) -> Progress {
        Task {
            do {
                guard writable else { throw NSError(domain: NSCocoaErrorDomain, code: NSFileWriteNoPermissionError) }
                try await client().delete(id: identifier.rawValue) // to the server's trash
                completionHandler(nil)
            } catch let e as APIError where e.status == 404 {
                completionHandler(nil)
            } catch {
                completionHandler(mapError(error))
            }
        }
        return Progress()
    }

    // MARK: Enumeration

    func enumerator(for containerItemIdentifier: NSFileProviderItemIdentifier,
                    request: NSFileProviderRequest) throws -> NSFileProviderEnumerator {
        switch containerItemIdentifier {
        case .workingSet:
            return ChangeEnumerator(source: source, writable: writable)
        case .trashContainer:
            return EmptyEnumerator()
        default:
            return FolderEnumerator(source: source, container: containerItemIdentifier, writable: writable)
        }
    }
}

enum FileProviderSignal {
    static func workingSet(_ domain: NSFileProviderDomain) {
        NSFileProviderManager(for: domain)?.signalEnumerator(for: .workingSet) { _ in }
    }
}

/// Maps client errors to the errors the system understands.
func mapError(_ error: Error) -> Error {
    if let e = error as? APIError {
        switch e.status {
        case 401: return NSFileProviderError(.notAuthenticated)
        case 404: return NSFileProviderError(.noSuchItem)
        case 409: return NSFileProviderError(.filenameCollision)
        case 403: return NSError(domain: NSCocoaErrorDomain, code: NSFileWriteNoPermissionError)
        default: return NSFileProviderError(.serverUnreachable)
        }
    }
    if error is URLError { return NSFileProviderError(.serverUnreachable) }
    return error
}
