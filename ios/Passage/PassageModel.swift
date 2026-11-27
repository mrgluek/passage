import Foundation
import SwiftUI
import Network

@MainActor
final class PassageModel: ObservableObject {
    @Published var link = ""
    @Published private(set) var settings = StoredSettings()
    @Published private(set) var status: CoreStatus?
    @Published private(set) var busy = false
    @Published var error: String?
    @Published var preview: ImportResult?
    @Published var diagnosis: String?
    private let core = PassageCore()
    private let store = KeychainStore()
    private var poll: Task<Void, Never>?
    private var monitor: NWPathMonitor?
    private var pendingLink = ""
    private var ready = false
    var running: Bool { status?.running ?? false }
    var routes: [SavedRoute] { SavedRoute.list(settings.config) }

    init() {
        do {
            if let saved = try store.load() { settings = saved }
            else { try store.save(settings) }
            ready = true
        } catch { self.error = error.localizedDescription }
    }

    func active(_ active: Bool) {
        poll?.cancel()
        poll = nil
        monitor?.cancel()
        monitor = nil
        guard active else { return }
        let core = core
        let monitor = NWPathMonitor()
        monitor.pathUpdateHandler = { _ in Task { await core.networkChanged() } }
        monitor.start(queue: DispatchQueue(label: "info.gluek.passage.network"))
        self.monitor = monitor
        poll = Task { [weak self] in
            await core.recheck()
            while !Task.isCancelled {
                guard let self else { return }
                await self.refresh()
                do { try await Task.sleep(for: .seconds(1)) } catch { return }
            }
        }
    }

    private func refresh() async {
        do { status = try await core.status() }
        catch { self.error = error.localizedDescription }
    }

    func toggle() async {
        guard !busy, ready else { return }
        busy = true
        defer { busy = false }
        do {
            if running { await core.stop() }
            else { try await core.start(settings.startConfig) }
            await refresh()
        } catch { self.error = error.localizedDescription }
    }

    func prepareImport() async {
        guard !busy, ready else { return }
        busy = true
        defer { busy = false }
        pendingLink = link.trimmingCharacters(in: .whitespacesAndNewlines)
        do { preview = try await core.importLink(ImportRequest(existing: settings.config, link: pendingLink, replace: false)) }
        catch { self.error = error.localizedDescription }
    }

    func confirmImport() async {
        guard !busy, ready else { return }
        busy = true
        defer { busy = false }
        do {
            let subscribing = preview?.mode == "subscribe"
            let result = try await core.importLink(ImportRequest(existing: settings.config, link: pendingLink, replace: true, subscribe: subscribing))
            var next = settings
            next.config = result.config
            if subscribing, let loaded = try? await core.refreshSubscriptions(existing: result.config, initial: true) {
                next.config = loaded.config
            }
            next.link = pendingLink
            if next.mode != "auto", next.mode != "off",
               !SavedRoute.list(next.config).contains(where: { !$0.off && $0.mode == next.mode }) { next.mode = "auto" }
            try store.save(next)
            settings = next
            link = ""
            if running { try await core.start(settings.startConfig) }
            await refresh()
        } catch { self.error = error.localizedDescription }
    }

    func setMode(_ mode: String) async {
        guard !busy, ready else { return }
        busy = true
        defer { busy = false }
        do {
            var next = settings
            next.mode = mode
            try store.save(next)
            settings = next
            if running { try await core.setMode(mode) }
            await refresh()
        } catch { self.error = error.localizedDescription }
    }

    func diagnose() async {
        guard !busy, running else { return }
        busy = true
        defer { busy = false }
        do { diagnosis = DiagnosisText.format(try await core.diagnose()) }
        catch { self.error = error.localizedDescription }
    }

    var importMessage: String {
        guard let p = preview else { return "" }
        let trust = localized(p.signature == "trusted_author" ? "import_trusted" : "import_untrusted")
        let operation = localized(pendingLink.isEmpty ? "import_clear" : (p.mode == "replace" ? "import_replace" : "import_merge"))
        return [trust, operation, p.collision ? localized("import_collision") : "",
                (p.route_ids ?? []).joined(separator: ", "), localized("import_personal")]
            .filter { !$0.isEmpty }.joined(separator: "\n\n")
    }
}

enum DiagnosisText {
    static func format(_ d: [String: JSONValue]) -> String {
        let ways = (d["ways"]?.array ?? []).map(\.string)
        var lines = [String(format: localized("test_time"), d["time"]?.string ?? ""),
                     ([localized("test_target")] + ways).joined(separator: "  ")]
        for row in d["rows"]?.array ?? [] {
            let r = row.object
            let cells = (r["results"]?.array ?? []).map { value in
                let x = value.object
                if x["skip"]?.bool == true { return "-" }
                if x["ok"]?.bool == true { return "ok \(x["ms"]?.int ?? 0)ms" }
                return x["error"]?.string ?? "fail"
            }
            lines.append(([String((r["target"]?.string ?? "").prefix(32))] + cells).joined(separator: "  "))
        }
        for check in d["checks"]?.array ?? [] {
            let c = check.object
            let stages = (c["stages"]?.array ?? []).map { value in
                let s = value.object
                let result = s["ok"]?.bool == true ? "ok \(s["ms"]?.int ?? 0)ms" : (s["error"]?.string ?? "fail")
                return "\(s["stage"]?.string ?? "") \(result)"
            }
            lines.append("\(c["route"]?.string ?? ""): \(stages.joined(separator: ", "))")
        }
        return lines.joined(separator: "\n")
    }
}
