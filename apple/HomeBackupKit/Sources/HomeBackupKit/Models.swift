import Foundation

/// A file or folder on the server (`File` in api/openapi.yaml).
public struct FileItem: Codable, Hashable, Sendable, Identifiable {
    /// Empty for folders that are only implied by file paths.
    public var id: String
    public var source: String
    public var path: String
    public var name: String
    public var isDir: Bool
    public var size: Int64
    public var modified: Date?
    public var sha256: String?
    public var deletedAt: Date?
    /// "photo" or "video" when the server can make a thumbnail.
    public var media: String?
    /// ID of the containing folder; empty at the top level and under
    /// folders that are only implied by file paths.
    public var parentId: String

    enum CodingKeys: String, CodingKey {
        case id, source, path, name, isDir, size, modified, sha256, deletedAt, media, parentId
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        id = try c.decodeIfPresent(String.self, forKey: .id) ?? ""
        source = try c.decode(String.self, forKey: .source)
        path = try c.decode(String.self, forKey: .path)
        name = try c.decode(String.self, forKey: .name)
        isDir = try c.decodeIfPresent(Bool.self, forKey: .isDir) ?? false
        size = try c.decodeIfPresent(Int64.self, forKey: .size) ?? 0
        modified = try c.decodeIfPresent(Date.self, forKey: .modified)
        sha256 = try c.decodeIfPresent(String.self, forKey: .sha256)
        deletedAt = try c.decodeIfPresent(Date.self, forKey: .deletedAt)
        media = try c.decodeIfPresent(String.self, forKey: .media)
        parentId = try c.decodeIfPresent(String.self, forKey: .parentId) ?? ""
    }

    public var isPhoto: Bool { media == "photo" }
    public var isVideo: Bool { media == "video" }
    public var isTrashed: Bool { deletedAt != nil }
}

/// One entry of the change feed.
public struct Change: Codable, Hashable, Sendable {
    public var seq: Int64
    /// True when the item was purged for good (not just moved to the trash).
    public var removed: Bool
    public var file: FileItem

    enum CodingKeys: String, CodingKey { case seq, removed, file }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        seq = try c.decode(Int64.self, forKey: .seq)
        removed = try c.decodeIfPresent(Bool.self, forKey: .removed) ?? false
        file = try c.decode(FileItem.self, forKey: .file)
    }
}

public struct ChangesPage: Codable, Sendable {
    public var changes: [Change]
    public var cursor: String
    public var more: Bool
}

public struct SourceStatus: Codable, Hashable, Sendable, Identifiable {
    public var id: String
    public var type: String
    public var schedule: String?
    public var running: Bool
    public var lastSync: Date?
    public var lastError: String?

    /// Sources the apps can write to (My Files).
    public var isWritable: Bool { type == "drive" }
}

public struct Health: Codable, Sendable {
    public var status: String
    public var version: String
    public var certFingerprint: String?
}

public struct PairResult: Codable, Sendable {
    public var deviceId: String
    public var token: String
    public var certFingerprint: String?
}

public struct Version: Codable, Hashable, Sendable, Identifiable {
    public var id: String
    public var size: Int64
    public var sha256: String
    public var modified: Date
    public var created: Date
}

/// An error returned by the server.
public struct APIError: Error, LocalizedError, Sendable {
    public var status: Int
    public var code: String
    public var message: String

    public var errorDescription: String? { message.isEmpty ? "Server error \(status)" : message }
}

enum JSON {
    static let decoder: JSONDecoder = {
        let d = JSONDecoder()
        d.dateDecodingStrategy = .custom { decoder in
            let s = try decoder.singleValueContainer().decode(String.self)
            guard let date = parseDate(s) else {
                throw DecodingError.dataCorrupted(.init(codingPath: decoder.codingPath, debugDescription: "invalid date \(s)"))
            }
            return date
        }
        return d
    }()

    static let encoder: JSONEncoder = {
        let e = JSONEncoder()
        e.dateEncodingStrategy = .iso8601
        return e
    }()

    /// Parses RFC 3339 dates as Go writes them, with 0 to 9 fractional digits.
    static func parseDate(_ s: String) -> Date? {
        var s = s
        var fraction = 0.0
        if let dot = s.firstIndex(of: "."),
           let end = s[dot...].firstIndex(where: { $0 == "Z" || $0 == "+" || $0 == "-" }) {
            fraction = Double("0" + s[dot..<end]) ?? 0
            s.removeSubrange(dot..<end)
        }
        let f = ISO8601DateFormatter()
        f.formatOptions = [.withInternetDateTime]
        return f.date(from: s).map { $0.addingTimeInterval(fraction) }
    }
}
