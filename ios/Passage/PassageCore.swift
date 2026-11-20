import Foundation
import PassageCore

actor PassageCore {
    private func decode<T: Decodable>(_ pointer: UnsafeMutablePointer<CChar>?, as: T.Type) throws -> T {
        guard let pointer else { throw CoreError(error: "Empty core response") }
        defer { passage_free(pointer) }
        let data = Data(String(cString: pointer).utf8)
        if let error = try? JSONDecoder().decode(CoreError.self, from: data) { throw error }
        return try JSONDecoder().decode(T.self, from: data)
    }

    private func input<T: Encodable>(_ value: T) throws -> String {
        String(decoding: try JSONEncoder().encode(value), as: UTF8.self)
    }

    func importLink(_ request: ImportRequest) throws -> ImportResult {
        var bytes = Array(try input(request).utf8CString)
        return try bytes.withUnsafeMutableBufferPointer {
            try decode(passage_importLink($0.baseAddress), as: ImportResult.self)
        }
    }

    func refreshSubscriptions(existing: [String: JSONValue], initial: Bool) throws -> RefreshResult {
        var bytes = Array(try input(RefreshRequest(existing: existing, initial: initial)).utf8CString)
        return try bytes.withUnsafeMutableBufferPointer {
            try decode(passage_refreshSubscriptions($0.baseAddress), as: RefreshResult.self)
        }
    }

    func removeSubscription(existing: [String: JSONValue], id: String) throws -> [String: JSONValue] {
        var bytes = Array(try input(RemoveRequest(existing: existing, id: id)).utf8CString)
        let reply = try bytes.withUnsafeMutableBufferPointer {
            try decode(passage_removeSubscription($0.baseAddress), as: [String: [String: JSONValue]].self)
        }
        return reply["config"] ?? existing
    }

    func start(_ config: [String: JSONValue]) throws {
        var bytes = Array(try input(config).utf8CString)
        _ = try bytes.withUnsafeMutableBufferPointer {
            try decode(passage_start($0.baseAddress), as: [String: JSONValue].self)
        }
    }

    func stop() { passage_stop() }
    func status() throws -> CoreStatus { try decode(passage_status(0), as: CoreStatus.self) }
    func recheck() { passage_recheck() }
    func networkChanged() { passage_networkChanged() }

    func setMode(_ mode: String) throws {
        var bytes = Array(mode.utf8CString)
        _ = try bytes.withUnsafeMutableBufferPointer {
            try decode(passage_setMode($0.baseAddress), as: [String: JSONValue].self)
        }
    }

    func diagnose() throws -> [String: JSONValue] {
        try decode(passage_diagnose(), as: [String: JSONValue].self)
    }
}

private struct RefreshRequest: Encodable {
    let existing: [String: JSONValue]
    let initial: Bool
}

private struct RemoveRequest: Encodable {
    let existing: [String: JSONValue]
    let id: String
}
