import FileProvider
import HomeBackupKit
import UniformTypeIdentifiers

/// A server file or folder as the system sees it.
///
/// Identifiers are server file IDs. Folders that exist only as part of file
/// paths (in mirrored sources) have no ID and use "dir:<path>".
final class Item: NSObject, NSFileProviderItem {
    let itemIdentifier: NSFileProviderItemIdentifier
    let parentItemIdentifier: NSFileProviderItemIdentifier
    let filename: String
    let contentType: UTType
    let documentSize: NSNumber?
    let contentModificationDate: Date?
    let itemVersion: NSFileProviderItemVersion
    let capabilities: NSFileProviderItemCapabilities
    let path: String

    init(_ f: FileItem, writable: Bool) {
        path = f.path
        itemIdentifier = Self.identifier(for: f)
        parentItemIdentifier = Self.parent(of: f)
        filename = f.name
        contentType = f.isDir ? .folder : (UTType(filenameExtension: (f.name as NSString).pathExtension) ?? .data)
        documentSize = f.isDir ? nil : NSNumber(value: f.size)
        contentModificationDate = f.modified
        let content = Data((f.isDir ? "dir" : (f.sha256 ?? String(f.size))).utf8)
        let meta = Data((f.path + "|" + String(f.modified?.timeIntervalSince1970 ?? 0)).utf8)
        itemVersion = NSFileProviderItemVersion(contentVersion: content, metadataVersion: meta)
        capabilities = Self.capabilities(isDir: f.isDir, writable: writable && !f.id.isEmpty)
    }

    /// The root of a source.
    init(rootOf source: String, writable: Bool) {
        path = ""
        itemIdentifier = .rootContainer
        parentItemIdentifier = .rootContainer
        filename = source
        contentType = .folder
        documentSize = nil
        contentModificationDate = nil
        itemVersion = NSFileProviderItemVersion(contentVersion: Data("root".utf8), metadataVersion: Data("root".utf8))
        capabilities = Self.capabilities(isDir: true, writable: writable)
    }

    /// A folder known only by its path.
    init(impliedFolder p: String) {
        path = p
        itemIdentifier = NSFileProviderItemIdentifier("dir:" + p)
        let dir = (p as NSString).deletingLastPathComponent
        parentItemIdentifier = dir.isEmpty ? .rootContainer : NSFileProviderItemIdentifier("dir:" + dir)
        filename = (p as NSString).lastPathComponent
        contentType = .folder
        documentSize = nil
        contentModificationDate = nil
        itemVersion = NSFileProviderItemVersion(contentVersion: Data("dir".utf8), metadataVersion: Data(p.utf8))
        capabilities = Self.capabilities(isDir: true, writable: false)
    }

    static func identifier(for f: FileItem) -> NSFileProviderItemIdentifier {
        f.id.isEmpty ? NSFileProviderItemIdentifier("dir:" + f.path) : NSFileProviderItemIdentifier(f.id)
    }

    static func parent(of f: FileItem) -> NSFileProviderItemIdentifier {
        if !f.parentId.isEmpty { return NSFileProviderItemIdentifier(f.parentId) }
        let dir = (f.path as NSString).deletingLastPathComponent
        return dir.isEmpty ? .rootContainer : NSFileProviderItemIdentifier("dir:" + dir)
    }

    /// The path of an implied folder identifier, if it is one.
    static func impliedPath(_ id: NSFileProviderItemIdentifier) -> String? {
        id.rawValue.hasPrefix("dir:") ? String(id.rawValue.dropFirst(4)) : nil
    }

    static func capabilities(isDir: Bool, writable: Bool) -> NSFileProviderItemCapabilities {
        var c: NSFileProviderItemCapabilities = [.allowsReading]
        if isDir { c.insert(.allowsContentEnumerating) }
        if writable {
            c.formUnion([.allowsWriting, .allowsRenaming, .allowsReparenting, .allowsDeleting])
            if isDir { c.insert(.allowsAddingSubItems) }
        }
        return c
    }
}
