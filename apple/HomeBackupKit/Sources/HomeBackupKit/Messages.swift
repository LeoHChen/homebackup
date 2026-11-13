import Foundation

/// Plain-language explanations of errors, so the apps can tell people what
/// happened and what to do instead of showing raw error text.
public enum UserMessage {
    /// Describes `error` for the person using the app. `server` is the host
    /// name to mention, if known.
    public static func describe(_ error: Error, server: String? = nil) -> String {
        let at = server.map { " at \($0)" } ?? ""
        if let e = error as? APIError {
            switch e.status {
            case 401:
                return "This device is no longer paired with the server. Pair it again to reconnect."
            case 403:
                return "The server refused this change. Google backups are read-only; your own files belong in My Files."
            case 404:
                return "The server doesn't have that anymore."
            case 409:
                return e.message.isEmpty ? "Something with that name already exists on the server." : e.message
            case 429:
                return "Too many attempts. Wait a few seconds and try again."
            case 500...:
                let detail = e.message.isEmpty ? "error \(e.status)" : e.message
                return "The server hit a problem: \(detail). Check the server's Activity log."
            default:
                return e.message.isEmpty ? "The server answered with error \(e.status)." : e.message
            }
        }
        if let u = error as? URLError {
            switch u.code {
            case .cannotFindHost, .dnsLookupFailed:
                return "Can't find the server\(at). Check the address, and that this device and the server are on the same network."
            case .cannotConnectToHost, .networkConnectionLost, .notConnectedToInternet, .timedOut:
                return "Can't reach the server\(at). Make sure it is turned on, homebackupd is running, and this device is on the same network."
            case .serverCertificateUntrusted, .serverCertificateHasBadDate, .serverCertificateHasUnknownRoot,
                 .serverCertificateNotYetValid, .secureConnectionFailed, .clientCertificateRejected:
                return "The server's key doesn't match the one this device paired with. If you reinstalled the server, disconnect and pair again. Otherwise don't trust this connection."
            case .cancelled:
                return "The connection was stopped. Most often the server's key no longer matches the one this device paired with; if you reinstalled the server, disconnect and pair again."
            case .appTransportSecurityRequiresSecureConnection:
                return "The address must start with https://."
            case .badURL, .unsupportedURL:
                return "That doesn't look like a server address. Use the form https://name.local:8420."
            default:
                break
            }
        }
        return error.localizedDescription
    }
}
