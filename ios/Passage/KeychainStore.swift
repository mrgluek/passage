import Foundation
import Security

struct StoredSettings: Codable, Sendable {
    var link = ""
    var config: [String: JSONValue] = [:]
    var mode = "auto"
    var port = Int.random(in: 20000..<32768)
    var user = UUID().uuidString.replacingOccurrences(of: "-", with: "").prefix(8).lowercased()
    var pass = UUID().uuidString.replacingOccurrences(of: "-", with: "").lowercased()
    var listen: String { "127.0.0.1:\(port)" }
    var startConfig: [String: JSONValue] {
        var result = config
        result["mode"] = .string(mode)
        result["listen"] = .string(listen)
        result["user"] = .string(user)
        result["pass"] = .string(pass)
        return result
    }
}

struct KeychainStore {
    private var query: [String: Any] {
        [kSecClass as String: kSecClassGenericPassword,
         kSecAttrService as String: "info.gluek.passage",
         kSecAttrAccount as String: "configuration"]
    }
    func load() throws -> StoredSettings? {
        var q = query
        q[kSecReturnData as String] = true
        q[kSecMatchLimit as String] = kSecMatchLimitOne
        var result: CFTypeRef?
        let status = SecItemCopyMatching(q as CFDictionary, &result)
        if status == errSecItemNotFound { return nil }
        guard status == errSecSuccess, let data = result as? Data else { throw failure(status) }
        return try JSONDecoder().decode(StoredSettings.self, from: data)
    }
    func save(_ settings: StoredSettings) throws {
        let data = try JSONEncoder().encode(settings)
        let attributes: [String: Any] = [kSecValueData as String: data,
            kSecAttrAccessible as String: kSecAttrAccessibleAfterFirstUnlockThisDeviceOnly]
        let status = SecItemUpdate(query as CFDictionary, attributes as CFDictionary)
        if status == errSecItemNotFound {
            let added = SecItemAdd(query.merging(attributes) { _, new in new } as CFDictionary, nil)
            guard added == errSecSuccess else { throw failure(added) }
        } else if status != errSecSuccess { throw failure(status) }
    }
    private func failure(_ status: OSStatus) -> CoreError {
        CoreError(error: SecCopyErrorMessageString(status, nil) as String? ?? "Keychain error \(status)")
    }
}
