import CryptoKit
import Foundation
import Security

/// TLS key pinning. The server uses a self-signed certificate; apps trust it
/// by the SHA-256 of its public key (SPKI), which the server shows when
/// pairing ("Server key: 3F2A 91C0 ...") and advertises over Bonjour.
public enum Pinning {
    /// Returns the hex SHA-256 of a DER certificate's SubjectPublicKeyInfo.
    public static func fingerprint(certificateDER der: Data) -> String? {
        guard let spki = subjectPublicKeyInfo(der) else { return nil }
        return SHA256.hash(data: spki).map { String(format: "%02x", $0) }.joined()
    }

    /// Formats the first 8 bytes for people to compare: "3F2A 91C0 7B4E 22D1".
    public static func short(_ fingerprint: String) -> String {
        let hex = Array(fingerprint.uppercased().prefix(16))
        return stride(from: 0, to: hex.count, by: 4)
            .map { String(hex[$0..<min($0 + 4, hex.count)]) }
            .joined(separator: " ")
    }

    /// Fingerprint of the leaf certificate a server presented.
    public static func fingerprint(of trust: SecTrust) -> String? {
        guard let chain = SecTrustCopyCertificateChain(trust) as? [SecCertificate],
              let leaf = chain.first else { return nil }
        return fingerprint(certificateDER: SecCertificateCopyData(leaf) as Data)
    }

    // Minimal DER walk: Certificate -> tbsCertificate -> subjectPublicKeyInfo.
    static func subjectPublicKeyInfo(_ data: Data) -> Data? {
        let b = [UInt8](data)
        guard let cert = element(b, at: 0), cert.tag == 0x30,
              let tbs = element(b, at: cert.contentStart), tbs.tag == 0x30 else { return nil }
        var i = tbs.contentStart
        if i < b.count, b[i] == 0xA0, let version = element(b, at: i) { i = version.end }
        // serialNumber, signature, issuer, validity, subject
        for _ in 0..<5 {
            guard let e = element(b, at: i) else { return nil }
            i = e.end
        }
        guard let spki = element(b, at: i), spki.tag == 0x30 else { return nil }
        return Data(b[i..<spki.end])
    }

    struct Element {
        var tag: UInt8
        var contentStart: Int
        var end: Int
    }

    static func element(_ b: [UInt8], at i: Int) -> Element? {
        guard i + 2 <= b.count else { return nil }
        let first = b[i + 1]
        var length = 0
        var header = 2
        if first < 0x80 {
            length = Int(first)
        } else {
            let n = Int(first & 0x7F)
            guard n >= 1, n <= 4, i + 2 + n <= b.count else { return nil }
            for k in 0..<n { length = length << 8 | Int(b[i + 2 + k]) }
            header += n
        }
        let end = i + header + length
        guard end <= b.count else { return nil }
        return Element(tag: b[i], contentStart: i + header, end: end)
    }
}

/// Accepts a server only if its key matches `expected`. With no expected
/// key (first contact, before pairing), it accepts the server and records
/// the key so the app can compare it with the one shown on the server.
final class PinningDelegate: NSObject, URLSessionDelegate, @unchecked Sendable {
    private let lock = NSLock()
    private var expected: String?
    private var _observed: String?

    init(expected: String?) { self.expected = expected?.lowercased() }

    var observed: String? { lock.withLock { _observed } }

    func setExpected(_ fp: String?) { lock.withLock { expected = fp?.lowercased() } }

    func urlSession(_ session: URLSession, didReceive challenge: URLAuthenticationChallenge,
                    completionHandler: @escaping (URLSession.AuthChallengeDisposition, URLCredential?) -> Void) {
        guard challenge.protectionSpace.authenticationMethod == NSURLAuthenticationMethodServerTrust,
              let trust = challenge.protectionSpace.serverTrust else {
            completionHandler(.performDefaultHandling, nil)
            return
        }
        let fp = Pinning.fingerprint(of: trust)
        let ok: Bool = lock.withLock {
            _observed = fp
            guard let fp else { return false }
            return expected == nil || expected == fp
        }
        if ok {
            completionHandler(.useCredential, URLCredential(trust: trust))
        } else {
            completionHandler(.cancelAuthenticationChallenge, nil)
        }
    }
}
