import Foundation

indirect enum JSONValue: Codable, Equatable, Sendable {
    case object([String: JSONValue]), array([JSONValue]), string(String), number(Double), bool(Bool), null

    init(from decoder: Decoder) throws {
        let c = try decoder.singleValueContainer()
        if c.decodeNil() { self = .null }
        else if let v = try? c.decode(Bool.self) { self = .bool(v) }
        else if let v = try? c.decode(Double.self) { self = .number(v) }
        else if let v = try? c.decode(String.self) { self = .string(v) }
        else if let v = try? c.decode([String: JSONValue].self) { self = .object(v) }
        else { self = .array(try c.decode([JSONValue].self)) }
    }

    func encode(to encoder: Encoder) throws {
        var c = encoder.singleValueContainer()
        switch self {
        case .object(let v): try c.encode(v)
        case .array(let v): try c.encode(v)
        case .string(let v): try c.encode(v)
        case .number(let v): try c.encode(v)
        case .bool(let v): try c.encode(v)
        case .null: try c.encodeNil()
        }
    }
    var object: [String: JSONValue] { if case .object(let v) = self { v } else { [:] } }
    var array: [JSONValue] { if case .array(let v) = self { v } else { [] } }
    var string: String { if case .string(let v) = self { v } else { "" } }
    var int: Int { if case .number(let v) = self { Int(v) } else { 0 } }
    var bool: Bool { if case .bool(let v) = self { v } else { false } }
}

struct ImportRequest: Encodable, Sendable {
    let existing: [String: JSONValue]
    let link: String
    let replace: Bool
    var subscribe: Bool = false
}

struct RefreshResult: Decodable, Sendable {
    let config: [String: JSONValue]
    let changed: Bool
}

struct ImportResult: Decodable, Sendable {
    let config: [String: JSONValue]
    let signature: String
    let mode: String
    let collision: Bool
    let route_ids: [String]?
}

struct CoreError: Decodable, Error, LocalizedError, Sendable {
    let error: String
    var errorDescription: String? { error }
}

struct CoreStatus: Decodable, Sendable {
    let running: Bool
    let route: String?
    let via: String?
    let mode: String?
    let state: String?
    let groups: [String: JSONValue]?
    let tunnels: [JSONValue]?
}

struct SavedRoute: Identifiable, Equatable, Sendable {
    let id: String
    let type: String
    let priority: Int
    let off: Bool
    var summary: String { "\(priority) \(id) (\(type))" }
    var mode: String { "route:\(id)" }

    static func list(_ config: [String: JSONValue]) -> [SavedRoute] {
        let edits = config["route_edits"]?.object ?? [:]
        let rows = config["routes"]?.array ?? []
        if rows.isEmpty, !(config["zone"]?.string ?? "").isEmpty {
            return [SavedRoute(id: "legacy-dns", type: "soar", priority: 100, off: false)]
        }
        return rows.map {
            let r = $0.object
            let id = r["id"]?.string ?? ""
            let edit = edits[id]?.object ?? [:]
            return SavedRoute(id: id, type: r["type"]?.string ?? "",
                              priority: (edit["priority"] ?? r["priority"])?.int ?? 0,
                              off: edit["off"]?.bool ?? false)
        }.sorted {
            if $0.off != $1.off { return !$0.off }
            if $0.priority != $1.priority { return $0.priority < $1.priority }
            return $0.id < $1.id
        }
    }
}

func localized(_ key: String) -> String { NSLocalizedString(key, comment: "") }
func routeName(_ route: String, via: String = "") -> String {
    switch route {
    case "direct": return localized("route_direct")
    case "smart": return localized("route_smart")
    case "tunnel": return via.isEmpty ? localized("route_tunnel") : routeLabel(via)
    default: return route
    }
}
func routeLabel(_ type: String) -> String {
    ["http": "HTTP", "wss": "WSS", "iroh": "Iroh", "soar": "Soar"][type] ?? type
}

extension CoreStatus {
    var summary: String {
        guard running else { return localized("proxy_not_running") }
        let groups = groups ?? [:]
        var counts: [String: Int] = [:]
        var order: [String] = []
        let types = Set((tunnels ?? []).map { $0.object["type"]?.string ?? "" })
        let only = types.count == 1 ? (types.first ?? "") : ""
        func name(_ r: String, _ v: String) -> String { routeName(r, via: v.isEmpty ? only : v) }
        for key in groups.keys.sorted() {
            let g = groups[key]?.object ?? [:]
            guard g["kind"]?.string == "relay" else { continue }
            let n = name(g["route"]?.string ?? "", g["via"]?.string ?? "")
            if counts[n] == nil { order.append(n) }
            counts[n, default: 0] += 1
        }
        var parts: [String] = []
        if !counts.isEmpty {
            let text = order.map { counts.count == 1 ? $0 : "\($0) (\(counts[$0, default: 0]))" }.joined(separator: ", ")
            parts.append(String(format: localized("group_mail"), text))
        }
        let tg = groups["telegram"]?.object ?? [:]
        if !(tg["targets"]?.array ?? []).isEmpty {
            parts.append(String(format: localized("group_telegram"), name(tg["route"]?.string ?? "", tg["via"]?.string ?? "")))
        }
        if parts.isEmpty { parts.append(String(format: localized("notification_route"), name(route ?? "", via ?? ""))) }
        if mode == "auto" {
            let key = ["direct": "net_open", "blocked": "net_restricted", "relays-down": "net_relays_down", "offline": "net_offline"][state ?? ""] ?? "net_unchecked"
            parts.append(localized(key))
        }
        let rows = tunnels ?? []
        if rows.isEmpty { parts.append(String(format: localized("tunnel_label"), localized("tunnel_no_link"))) }
        else {
            let types = Dictionary(grouping: rows, by: { $0.object["type"]?.string ?? "" })
            parts.append(rows.map {
                let r = $0.object
                let type = r["type"]?.string ?? ""
                let title = (types[type]?.count ?? 0) > 1 ? (r["id"]?.string ?? "") : routeLabel(type)
                let state = r["state"]?.string ?? ""
                return String(format: localized("tunnel_route"), title, localized("route_state_\(state)"))
            }.joined(separator: ", "))
        }
        return parts.joined(separator: " · ")
    }
}
