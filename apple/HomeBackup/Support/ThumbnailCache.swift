import HomeBackupKit
import UIKit

/// Thumbnails in memory and on disk, keyed by content hash and size.
actor ThumbnailCache {
    static let shared = ThumbnailCache()

    private let memory = NSCache<NSString, UIImage>()
    private let dir: URL
    private var inflight: [String: Task<UIImage?, Never>] = [:]

    init() {
        dir = FileManager.default.urls(for: .cachesDirectory, in: .userDomainMask)[0].appendingPathComponent("thumbs")
        try? FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        memory.countLimit = 600
    }

    func image(for file: FileItem, size: HomeBackupClient.ThumbnailSize, client: HomeBackupClient) async -> UIImage? {
        let key = (file.sha256 ?? file.id) + "-" + size.rawValue
        if let img = memory.object(forKey: key as NSString) { return img }
        if let task = inflight[key] { return await task.value }
        let url = dir.appendingPathComponent(key + ".jpg")
        let task = Task<UIImage?, Never> {
            if let data = try? Data(contentsOf: url), let img = UIImage(data: data) { return img }
            guard let data = try? await client.thumbnail(id: file.id, size: size),
                  let img = UIImage(data: data) else { return nil }
            try? data.write(to: url, options: .atomic)
            return img
        }
        inflight[key] = task
        let img = await task.value
        inflight[key] = nil
        if let img { memory.setObject(img, forKey: key as NSString) }
        return img
    }
}
