import CryptoKit
import Foundation

/// What to upload and where.
public struct UploadRequest: Sendable {
    public var fileURL: URL
    public var source: String
    /// Destination path in the source, e.g. "2026/11/IMG_0001.HEIC".
    public var path: String
    /// Photos: the PhotoKit localIdentifier, so the same asset is stored once.
    public var assetId: String?
    /// Capture or modification date to record.
    public var created: Date?
    /// Drive sources: replace this file with a new version.
    public var fileId: String?
    /// Drive sources: the content the app last saw (sha256), so an edit to a
    /// stale copy is kept as a conflict copy instead of overwriting.
    public var baseSha256: String?

    public init(fileURL: URL, source: String, path: String, assetId: String? = nil, created: Date? = nil,
                fileId: String? = nil, baseSha256: String? = nil) {
        self.fileURL = fileURL
        self.source = source
        self.path = path
        self.assetId = assetId
        self.created = created
        self.fileId = fileId
        self.baseSha256 = baseSha256
    }
}

public enum UploadResult: Sendable {
    /// Uploaded and stored (for drive conflicts, at a "(conflict ...)" path).
    case stored(FileItem)
    /// The server already had this content; nothing was sent.
    case alreadyBackedUp(FileItem)

    public var file: FileItem {
        switch self {
        case .stored(let f), .alreadyBackedUp(let f): return f
        }
    }
}

extension HomeBackupClient {
    /// Uploads a file with the resumable upload API. If the app is stopped
    /// part way, calling this again with the same request continues from
    /// the bytes the server already has.
    public func upload(_ req: UploadRequest, chunkSize: Int = 8 << 20,
                       progress: (@Sendable (_ sent: Int64, _ total: Int64) -> Void)? = nil) async throws -> UploadResult {
        let (size, sha) = try Self.sizeAndSHA256(of: req.fileURL)

        struct Create: Encodable {
            var source, path: String
            var size: Int64
            var sha256: String
            var assetId: String?
            var created: Date?
            var fileId: String?
            var baseSha256: String?
        }
        struct Created: Decodable {
            var uploadId: String?
            var offset: Int64?
            var status: String?
            var file: FileItem?
        }
        let body = Create(source: req.source, path: req.path, size: size, sha256: sha, assetId: req.assetId,
                          created: req.created, fileId: req.fileId, baseSha256: req.baseSha256)
        let created: Created = try await send("POST", "uploads", body: body)
        if created.status == "exists", let f = created.file { return .alreadyBackedUp(f) }
        guard let id = created.uploadId else { throw URLError(.badServerResponse) }
        var offset = created.offset ?? 0

        let handle = try FileHandle(forReadingFrom: req.fileURL)
        defer { try? handle.close() }
        for attempt in 0..<2 {
            while offset < size {
                try Task.checkCancellation()
                progress?(offset, size)
                try handle.seek(toOffset: UInt64(offset))
                let chunk = try handle.read(upToCount: chunkSize) ?? Data()
                if chunk.isEmpty { throw CocoaError(.fileReadCorruptFile) } // file shrank
                var r = try request("PATCH", "uploads/\(escape(id))")
                r.setValue(String(offset), forHTTPHeaderField: "Upload-Offset")
                r.setValue("application/offset+octet-stream", forHTTPHeaderField: "Content-Type")
                let (data, resp) = try await session.upload(for: r, from: chunk)
                guard let http = resp as? HTTPURLResponse else { throw URLError(.badServerResponse) }
                switch http.statusCode {
                case 204, 409, 400:
                    // 409: the server has a different offset (a retried
                    // chunk); 400: the chunk was cut off. Both report where
                    // to continue.
                    guard let next = http.value(forHTTPHeaderField: "Upload-Offset").flatMap(Int64.init) else {
                        try check(resp, data)
                        throw URLError(.badServerResponse)
                    }
                    if next == offset && http.statusCode != 204 { try check(resp, data) }
                    offset = next
                default:
                    try check(resp, data)
                }
            }
            progress?(size, size)
            let (data, resp) = try await session.data(for: request("POST", "uploads/\(escape(id))/complete"))
            if let http = resp as? HTTPURLResponse, http.statusCode == 409, attempt == 0,
               let next = http.value(forHTTPHeaderField: "Upload-Offset").flatMap(Int64.init) {
                offset = next // some bytes went missing; send them and complete again
                continue
            }
            try check(resp, data)
            return .stored(try JSON.decoder.decode(FileItem.self, from: data))
        }
        throw URLError(.cannotCreateFile)
    }

    /// Size and lowercase hex SHA-256 of a file, read in blocks.
    public static func sizeAndSHA256(of url: URL) throws -> (Int64, String) {
        let handle = try FileHandle(forReadingFrom: url)
        defer { try? handle.close() }
        var hasher = SHA256()
        var size: Int64 = 0
        while let block = try handle.read(upToCount: 4 << 20), !block.isEmpty {
            hasher.update(data: block)
            size += Int64(block.count)
        }
        return (size, hasher.finalize().map { String(format: "%02x", $0) }.joined())
    }
}
