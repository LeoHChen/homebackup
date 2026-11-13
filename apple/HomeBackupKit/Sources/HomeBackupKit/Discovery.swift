import Foundation
import Network

/// A home server found on the local network over Bonjour.
public struct DiscoveredServer: Hashable, Sendable {
    public var name: String
    public var endpoint: NWEndpoint
    /// The server's key fingerprint from its TXT record, if it serves HTTPS.
    public var fingerprint: String?
    public var usesTLS: Bool
}

/// Finds homebackupd servers (`_homebackup._tcp`) on the local network.
/// iOS apps need NSBonjourServices and NSLocalNetworkUsageDescription in
/// their Info.plist.
public final class ServerBrowser: @unchecked Sendable {
    public static let serviceType = "_homebackup._tcp"
    private let browser: NWBrowser
    private let queue = DispatchQueue(label: "homebackup.browser")

    public init() {
        browser = NWBrowser(for: .bonjourWithTXTRecord(type: Self.serviceType, domain: nil), using: .tcp)
    }

    /// Streams the current set of servers whenever it changes.
    public func servers() -> AsyncStream<[DiscoveredServer]> {
        AsyncStream { cont in
            browser.browseResultsChangedHandler = { results, _ in
                cont.yield(results.compactMap(Self.server(from:)).sorted { $0.name < $1.name })
            }
            browser.stateUpdateHandler = { state in
                if case .failed = state { cont.finish() }
                if case .cancelled = state { cont.finish() }
            }
            cont.onTermination = { [browser] _ in browser.cancel() }
            browser.start(queue: queue)
        }
    }

    static func server(from r: NWBrowser.Result) -> DiscoveredServer? {
        guard case let .service(name, _, _, _) = r.endpoint else { return nil }
        var fp: String?
        var tls = false
        if case let .bonjour(txt) = r.metadata {
            fp = txt["fp"]
            tls = txt["tls"] == "true"
        }
        return DiscoveredServer(name: name, endpoint: r.endpoint, fingerprint: fp, usesTLS: tls)
    }

    /// Resolves a discovered server to a base URL such as https://192.168.1.5:8420.
    public static func resolve(_ server: DiscoveredServer, timeout: TimeInterval = 10) async throws -> URL {
        let conn = NWConnection(to: server.endpoint, using: .tcp)
        let q = DispatchQueue(label: "homebackup.resolve")
        return try await withCheckedThrowingContinuation { cont in
            let once = Once()
            conn.stateUpdateHandler = { state in
                switch state {
                case .ready:
                    defer { conn.cancel() }
                    guard case let .hostPort(host, port)? = conn.currentPath?.remoteEndpoint else {
                        once.run { cont.resume(throwing: URLError(.cannotFindHost)) }
                        return
                    }
                    var h: String
                    switch host {
                    case .ipv4(let a): h = "\(a)"
                    case .ipv6(let a): h = "[\("\(a)".replacingOccurrences(of: "%", with: "%25"))]"
                    case .name(let n, _): h = n
                    @unknown default: h = "\(host)"
                    }
                    if let pct = h.firstIndex(of: "%"), !h.hasPrefix("[") { h = String(h[..<pct]) }
                    let scheme = server.usesTLS ? "https" : "http"
                    if let url = URL(string: "\(scheme)://\(h):\(port.rawValue)") {
                        once.run { cont.resume(returning: url) }
                    } else {
                        once.run { cont.resume(throwing: URLError(.badURL)) }
                    }
                case .failed(let err):
                    once.run { cont.resume(throwing: err) }
                    conn.cancel()
                default:
                    break
                }
            }
            conn.start(queue: q)
            q.asyncAfter(deadline: .now() + timeout) {
                once.run { cont.resume(throwing: URLError(.timedOut)) }
                conn.cancel()
            }
        }
    }
}

/// Runs a closure at most once (continuations must resume exactly once).
final class Once: @unchecked Sendable {
    private let lock = NSLock()
    private var done = false
    func run(_ f: () -> Void) {
        lock.lock()
        defer { lock.unlock() }
        if !done {
            done = true
            f()
        }
    }
}
