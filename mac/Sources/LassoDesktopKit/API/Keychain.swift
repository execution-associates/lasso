import Foundation
import Security

/// Secrets live in the login Keychain, never in UserDefaults or files
/// (constraint 4). Generic passwords under one service; read at the moment
/// of use. Values are never logged.
public enum Keychain {
    public static let service = "com.orangecountyai.LassoDesktop"

    public enum Account: String, CaseIterable, Sendable {
        /// "user:pass" for a lasso behind UI_AUTH.
        case uiAuth = "ui-auth"
        /// A Cloudflare Access service token, for lasso behind Access.
        case cfAccessID = "cf-access-id"
        case cfAccessSecret = "cf-access-secret"
        case groq
        case openai
    }

    public static func read(_ account: Account) -> String? {
        let q: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: account.rawValue,
            kSecReturnData as String: true,
            kSecMatchLimit as String: kSecMatchLimitOne,
        ]
        var out: CFTypeRef?
        guard SecItemCopyMatching(q as CFDictionary, &out) == errSecSuccess, let data = out as? Data else {
            return nil
        }
        return String(data: data, encoding: .utf8)
    }

    public static func isSet(_ account: Account) -> Bool { read(account)?.isEmpty == false }

    @discardableResult
    public static func write(_ account: Account, _ value: String) -> Bool {
        let base: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: account.rawValue,
        ]
        let data = Data(value.utf8)
        if SecItemUpdate(base as CFDictionary, [kSecValueData as String: data] as CFDictionary) == errSecSuccess {
            return true
        }
        var add = base
        add[kSecValueData as String] = data
        return SecItemAdd(add as CFDictionary, nil) == errSecSuccess
    }

    public static func delete(_ account: Account) {
        let q: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: account.rawValue,
        ]
        SecItemDelete(q as CFDictionary)
    }
}
