import Foundation

/// Client for the homebackupd API (see api/openapi.yaml).
public final class HomeBackupClient: @unchecked Sendable {
    public let baseURL: URL
    public var token: String?
    let session: URLSession
    private let pinning: PinningDelegate

    /// - Parameters:
    ///   - baseURL: e.g. https://mac-mini.local:8420
    ///   - fingerprint: the pinned server key; nil only before pairing.
    ///   - token: the device token from pairing.
    ///   - configuration: for tests (e.g. a mock URLProtocol).
    public init(baseURL: URL, fingerprint: String?, token: String?,
                configuration: URLSessionConfiguration = .default) {
        self.baseURL = baseURL
        self.token = token
        pinning = PinningDelegate(expected: fingerprint)
        session = URLSession(configuration: configuration, delegate: pinning, delegateQueue: nil)
    }

    deinit { session.finishTasksAndInvalidate() }

    /// The key of the server reached last, to show while pairing.
    public var observedFingerprint: String? { pinning.observed }

    /// Pins the server key (after the person confirmed it while pairing).
    public func pin(_ fingerprint: String) { pinning.setExpected(fingerprint) }

    // MARK: Server and devices

    public func health() async throws -> Health {
        try await get("health")
    }

    /// Pairs this device with the code shown by `homebackupd pair`.
    public func pair(code: String, deviceName: String, platform: String) async throws -> PairResult {
        let r: PairResult = try await send("POST", "pair",
            body: ["code": code, "deviceName": deviceName, "platform": platform], auth: false)
        token = r.token
        return r
    }

    public func sources() async throws -> [SourceStatus] {
        try await get("sources")
    }

    /// Server, disk, and source status.
    public func status() async throws -> ServerStatus {
        try await get("status")
    }

    /// Asks the server for a pairing code to enter on another device.
    public func newPairCode() async throws -> PairCode {
        try await send("POST", "pair/codes", body: [String: String]())
    }

    public func devices() async throws -> [Device] {
        try await get("devices")
    }

    /// Signs a device out for good; it must pair again to reconnect.
    public func revokeDevice(id: String) async throws {
        try await sendNoContent("DELETE", "devices/\(escape(id))")
    }

    public func syncNow(source: String) async throws {
        try await sendNoContent("POST", "sources/\(escape(source))/sync")
    }

    // MARK: Files

    private struct Items: Decodable { var items: [FileItem] }

    public func list(source: String, path: String = "") async throws -> [FileItem] {
        let r: Items = try await get("files", query: ["source": source, "path": path])
        return r.items
    }

    public func file(id: String) async throws -> FileItem {
        try await get("files/\(escape(id))")
    }

    public func versions(id: String) async throws -> [Version] {
        try await get("files/\(escape(id))/versions")
    }

    public func trash(source: String) async throws -> [FileItem] {
        try await get("trash", query: ["source": source])
    }

    /// Restores a previous version, or a trashed file when version is nil.
    public func restore(id: String, version: String? = nil, to path: String? = nil) async throws -> FileItem {
        var body: [String: String] = [:]
        body["version"] = version
        body["path"] = path
        return try await send("POST", "files/\(escape(id))/restore", body: body)
    }

    /// One page of the change feed. Start with cursor "0".
    public func changes(source: String, cursor: String, limit: Int = 1000) async throws -> ChangesPage {
        try await get("changes", query: ["source": source, "cursor": cursor, "limit": String(limit)])
    }

    /// All changes since cursor, and the cursor to pass next time.
    public func allChanges(source: String, since cursor: String) async throws -> (changes: [Change], cursor: String) {
        var all: [Change] = []
        var cursor = cursor
        while true {
            let page = try await changes(source: source, cursor: cursor)
            all += page.changes
            cursor = page.cursor
            if !page.more { return (all, cursor) }
        }
    }

    public func createFolder(source: String, path: String) async throws -> FileItem {
        try await send("POST", "folders", body: ["source": source, "path": path])
    }

    /// Renames or moves a file or folder (drive sources).
    public func move(id: String, to path: String) async throws -> FileItem {
        try await send("PATCH", "files/\(escape(id))", body: ["path": path])
    }

    /// Moves a file or folder to the trash (drive sources).
    public func delete(id: String) async throws {
        try await sendNoContent("DELETE", "files/\(escape(id))")
    }

    public enum ThumbnailSize: String, Sendable { case small, large }

    /// JPEG thumbnail of a photo or video.
    public func thumbnail(id: String, size: ThumbnailSize = .small) async throws -> Data {
        let req = try request("GET", "files/\(escape(id))/thumbnail", query: ["size": size.rawValue])
        let (data, resp) = try await session.data(for: req)
        try check(resp, data)
        return data
    }

    /// Downloads a file (or one of its versions) to a temporary location;
    /// the caller moves it where it belongs.
    public func download(id: String, version: String? = nil) async throws -> URL {
        var q: [String: String] = [:]
        q["version"] = version
        let req = try request("GET", "files/\(escape(id))/content", query: q)
        let (url, resp) = try await session.download(for: req)
        if let http = resp as? HTTPURLResponse, !(200..<300).contains(http.statusCode) {
            let data = (try? Data(contentsOf: url)) ?? Data()
            try? FileManager.default.removeItem(at: url)
            try check(resp, data)
        }
        return url
    }

    // MARK: Plumbing

    func request(_ method: String, _ path: String, query: [String: String] = [:],
                 body: Data? = nil, auth: Bool = true) throws -> URLRequest {
        guard var comps = URLComponents(url: baseURL, resolvingAgainstBaseURL: false) else { throw URLError(.badURL) }
        var base = comps.percentEncodedPath
        if base.hasSuffix("/") { base.removeLast() }
        comps.percentEncodedPath = base + "/api/v1/" + path
        if !query.isEmpty {
            comps.queryItems = query.sorted { $0.key < $1.key }.map { URLQueryItem(name: $0.key, value: $0.value) }
        }
        guard let url = comps.url else { throw URLError(.badURL) }
        var req = URLRequest(url: url)
        req.httpMethod = method
        if auth, let token { req.setValue("Bearer " + token, forHTTPHeaderField: "Authorization") }
        if let body {
            req.httpBody = body
            req.setValue("application/json", forHTTPHeaderField: "Content-Type")
        }
        return req
    }

    func get<T: Decodable>(_ path: String, query: [String: String] = [:]) async throws -> T {
        let (data, resp) = try await session.data(for: request("GET", path, query: query))
        try check(resp, data)
        return try JSON.decoder.decode(T.self, from: data)
    }

    func send<T: Decodable>(_ method: String, _ path: String, body: some Encodable, auth: Bool = true) async throws -> T {
        let req = try request(method, path, body: JSON.encoder.encode(body), auth: auth)
        let (data, resp) = try await session.data(for: req)
        try check(resp, data)
        return try JSON.decoder.decode(T.self, from: data)
    }

    func sendNoContent(_ method: String, _ path: String) async throws {
        let (data, resp) = try await session.data(for: request(method, path))
        try check(resp, data)
    }

    func check(_ resp: URLResponse, _ data: Data) throws {
        guard let http = resp as? HTTPURLResponse else { throw URLError(.badServerResponse) }
        guard (200..<300).contains(http.statusCode) || http.statusCode == 304 else {
            struct Wrapper: Decodable { struct E: Decodable { var code: String; var message: String }; var error: E }
            let e = try? JSON.decoder.decode(Wrapper.self, from: data)
            throw APIError(status: http.statusCode, code: e?.error.code ?? "", message: e?.error.message ?? "")
        }
    }

    func escape(_ s: String) -> String {
        s.addingPercentEncoding(withAllowedCharacters: .urlPathAllowed.subtracting(CharacterSet(charactersIn: "/"))) ?? s
    }
}
