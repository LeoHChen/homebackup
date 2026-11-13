import XCTest
@testable import HomeBackupKit

/// Serves requests from a closure, so client logic is tested without a server.
final class MockProtocol: URLProtocol {
    struct Reply {
        var status: Int
        var headers: [String: String] = [:]
        var body: Data = Data()
    }
    nonisolated(unsafe) static var handler: ((URLRequest, Data) -> Reply)?
    nonisolated(unsafe) static var log: [String] = []

    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }
    override func stopLoading() {}

    override func startLoading() {
        var body = request.httpBody ?? Data()
        if let stream = request.httpBodyStream {
            stream.open()
            var buf = [UInt8](repeating: 0, count: 65536)
            while stream.hasBytesAvailable {
                let n = stream.read(&buf, maxLength: buf.count)
                if n <= 0 { break }
                body.append(buf, count: n)
            }
            stream.close()
        }
        Self.log.append("\(request.httpMethod!) \(request.url!.path)")
        let r = Self.handler!(request, body)
        let resp = HTTPURLResponse(url: request.url!, statusCode: r.status, httpVersion: "HTTP/1.1", headerFields: r.headers)!
        client?.urlProtocol(self, didReceive: resp, cacheStoragePolicy: .notAllowed)
        client?.urlProtocol(self, didLoad: r.body)
        client?.urlProtocolDidFinishLoading(self)
    }
}

final class ClientTests: XCTestCase {
    var client: HomeBackupClient!

    override func setUp() {
        let cfg = URLSessionConfiguration.ephemeral
        cfg.protocolClasses = [MockProtocol.self]
        client = HomeBackupClient(baseURL: URL(string: "https://server.local:8420")!, fingerprint: nil,
                                  token: "tok", configuration: cfg)
        MockProtocol.log = []
    }

    static let fileJSON = #"{"id":"5","source":"files","path":"Docs/a b.txt","name":"a b.txt","isDir":false,"size":10}"#

    func tempFile(_ s: String) throws -> URL {
        let url = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        try Data(s.utf8).write(to: url)
        return url
    }

    func testResumableUploadContinuesAfterOffsetMismatch() async throws {
        let url = try tempFile("0123456789")
        var stored = Data()
        var patches = 0
        MockProtocol.handler = { req, body in
            XCTAssertEqual(req.value(forHTTPHeaderField: "Authorization"), "Bearer tok")
            switch (req.httpMethod!, req.url!.path) {
            case ("POST", "/api/v1/uploads"):
                let o = try! JSONSerialization.jsonObject(with: body) as! [String: Any]
                XCTAssertEqual(o["size"] as? Int, 10)
                XCTAssertEqual(o["sha256"] as? String, "84d89877f0d4041efb6bf91a16f0248f2fd573e6af05c19f96bedb9f882f7882")
                XCTAssertEqual(o["fileId"] as? String, "5")
                return .init(status: 201, body: Data(#"{"uploadId":"u1","offset":0}"#.utf8))
            case ("PATCH", "/api/v1/uploads/u1"):
                patches += 1
                let off = Int(req.value(forHTTPHeaderField: "Upload-Offset")!)!
                if patches == 2 {
                    // Pretend the server already had more bytes (a retried chunk).
                    stored.append(body.prefix(2))
                    return .init(status: 409, headers: ["Upload-Offset": String(stored.count)])
                }
                XCTAssertEqual(off, stored.count)
                stored.append(body)
                return .init(status: 204, headers: ["Upload-Offset": String(stored.count)])
            case ("POST", "/api/v1/uploads/u1/complete"):
                XCTAssertEqual(String(decoding: stored, as: UTF8.self), "0123456789")
                return .init(status: 200, body: Data(Self.fileJSON.utf8))
            default:
                XCTFail("unexpected \(req.url!)")
                return .init(status: 500)
            }
        }
        let r = try await client.upload(UploadRequest(fileURL: url, source: "files", path: "Docs/a b.txt",
                                                      fileId: "5", baseSha256: "abc"), chunkSize: 4)
        guard case .stored(let f) = r else { return XCTFail("\(r)") }
        XCTAssertEqual(f.name, "a b.txt")
        XCTAssertGreaterThanOrEqual(patches, 3)
    }

    func testUploadAlreadyBackedUp() async throws {
        let url = try tempFile("x")
        MockProtocol.handler = { _, _ in
            .init(status: 200, body: Data(#"{"status":"exists","file":\#(Self.fileJSON)}"#.utf8))
        }
        let r = try await client.upload(UploadRequest(fileURL: url, source: "iphone", path: "a.jpg", assetId: "A1"))
        guard case .alreadyBackedUp = r else { return XCTFail("\(r)") }
        XCTAssertEqual(MockProtocol.log, ["POST /api/v1/uploads"])
    }

    func testErrorsAndEscaping() async throws {
        MockProtocol.handler = { req, _ in
            XCTAssertEqual(req.url!.query, "path=My%20Docs&source=files")
            return .init(status: 403, body: Data(#"{"error":{"code":"read_only","message":"source is read-only"}}"#.utf8))
        }
        do {
            _ = try await client.list(source: "files", path: "My Docs")
            XCTFail("expected error")
        } catch let e as APIError {
            XCTAssertEqual(e.status, 403)
            XCTAssertEqual(e.code, "read_only")
        }
    }

    func testAllChangesPages() async throws {
        MockProtocol.handler = { req, _ in
            let cursor = URLComponents(url: req.url!, resolvingAgainstBaseURL: false)!
                .queryItems!.first { $0.name == "cursor" }!.value!
            let body = cursor == "0"
                ? #"{"changes":[{"seq":1,"file":\#(Self.fileJSON)}],"cursor":"1","more":true}"#
                : #"{"changes":[{"seq":2,"removed":true,"file":\#(Self.fileJSON)}],"cursor":"2","more":false}"#
            return .init(status: 200, body: Data(body.utf8))
        }
        let r = try await client.allChanges(source: "files", since: "0")
        XCTAssertEqual(r.changes.map(\.seq), [1, 2])
        XCTAssertEqual(r.cursor, "2")
    }
}
