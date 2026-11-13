import Foundation
import Security

/// Server settings shared by the app and its extensions (File Provider)
/// through the app group. The device token is kept in the Keychain.
public enum SharedStore {
    /// The app group shared with the extensions. On iOS it is the "group."
    /// identifier. On macOS it is read from the app's own entitlements: a
    /// Team-ID-prefixed group (`<TEAMID>.com.github.leohchen.homebackup`),
    /// which macOS 15 and later accept without a provisioning-profile entry
    /// or a "wants to access data from other apps" prompt, and which is also
    /// valid as a keychain access group, so the extension can read the token.
    public static let appGroup: String = {
        #if os(macOS)
        if let g = entitledAppGroups().first { return g }
        #endif
        return "group.com.github.leohchen.homebackup"
    }()

    #if os(macOS)
    static func entitledAppGroups() -> [String] {
        guard let task = SecTaskCreateFromSelf(nil),
              let value = SecTaskCopyValueForEntitlement(task, "com.apple.security.application-groups" as CFString, nil)
        else { return [] }
        return (value as? [String]) ?? []
    }
    #endif

    private static let tokenAccount = "token"
    private static let service = "com.github.leohchen.homebackup"

    public struct Settings: Codable, Sendable, Equatable {
        public var serverURL: URL
        public var fingerprint: String?
        /// Sources the apps may write to (drive sources).
        public var writableSources: [String]

        public init(serverURL: URL, fingerprint: String?, writableSources: [String] = []) {
            self.serverURL = serverURL
            self.fingerprint = fingerprint
            self.writableSources = writableSources
        }
    }

    static var defaults: UserDefaults { UserDefaults(suiteName: appGroup) ?? .standard }

    public static var settings: Settings? {
        get {
            guard let data = defaults.data(forKey: "settings") else { return nil }
            return try? JSONDecoder().decode(Settings.self, from: data)
        }
        set {
            if let newValue, let data = try? JSONEncoder().encode(newValue) {
                defaults.set(data, forKey: "settings")
            } else {
                defaults.removeObject(forKey: "settings")
            }
        }
    }

    public static var token: String? {
        get { keychainGet() }
        set { keychainSet(newValue) }
    }

    /// A client for the paired server, or nil before pairing.
    public static func makeClient() -> HomeBackupClient? {
        guard let s = settings, let token else { return nil }
        return HomeBackupClient(baseURL: s.serverURL, fingerprint: s.fingerprint, token: token)
    }

    public static func clear() {
        settings = nil
        token = nil
    }

    // MARK: Keychain (shared through the app group when entitled)

    private static func query(group: Bool) -> [String: Any] {
        var q: [String: Any] = [kSecClass as String: kSecClassGenericPassword,
                                kSecAttrService as String: service,
                                kSecAttrAccount as String: tokenAccount]
        if group {
            q[kSecAttrAccessGroup as String] = appGroup
            // macOS: access groups need the data protection keychain.
            q[kSecUseDataProtectionKeychain as String] = true
        }
        return q
    }

    private static func keychainSet(_ value: String?) {
        for group in [true, false] { SecItemDelete(query(group: group) as CFDictionary) }
        guard let value else { return }
        for group in [true, false] {
            var add = query(group: group)
            add[kSecValueData as String] = Data(value.utf8)
            add[kSecAttrAccessible as String] = kSecAttrAccessibleAfterFirstUnlockThisDeviceOnly
            // Without the app group entitlement (unsigned simulator builds)
            // the shared group is refused; fall back to the app's own.
            if SecItemAdd(add as CFDictionary, nil) == errSecSuccess { return }
        }
    }

    private static func keychainGet() -> String? {
        for group in [true, false] {
            var q = query(group: group)
            q[kSecReturnData as String] = true
            q[kSecMatchLimit as String] = kSecMatchLimitOne
            var out: CFTypeRef?
            if SecItemCopyMatching(q as CFDictionary, &out) == errSecSuccess, let data = out as? Data {
                return String(data: data, encoding: .utf8)
            }
        }
        return nil
    }
}
