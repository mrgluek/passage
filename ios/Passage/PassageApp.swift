import SwiftUI

@main
struct PassageApp: App {
    @StateObject private var model = PassageModel()
    @Environment(\.scenePhase) private var phase

    var body: some Scene {
        WindowGroup {
            ContentView(model: model)
                .onChange(of: phase) { model.active($0 == .active) }
                .onAppear { model.active(phase == .active) }
        }
    }
}

struct ContentView: View {
    @ObservedObject var model: PassageModel

    var body: some View {
        NavigationStack {
            Form {
                Section(localized("link_label")) {
                    TextField(localized("link_hint"), text: $model.link, axis: .vertical)
                        .textInputAutocapitalization(.never)
                        .autocorrectionDisabled()
                        .keyboardType(.URL)
                        .privacySensitive()
                    Button(localized("add_routes")) { Task { await model.prepareImport() } }
                }
                Section {
                    Button(localized(model.running ? "action_stop" : "action_start")) {
                        Task { await model.toggle() }
                    }
                    Picker(localized("route_label"), selection: Binding(get: { model.settings.mode }, set: { mode in
                        Task { await model.setMode(mode) }
                    })) {
                        Text(localized("mode_auto")).tag("auto")
                        ForEach(model.routes.filter { !$0.off }) { r in Text(r.summary).tag(r.mode) }
                        Text(localized("mode_off")).tag("off")
                    }
                    Text(localized("route_hint")).font(.caption).foregroundStyle(.secondary)
                }
                Section(localized("status_label")) {
                    Text(model.status?.summary ?? localized("proxy_not_running"))
                    Button(localized("test_network")) { Task { await model.diagnose() } }
                        .disabled(!model.running)
                    if model.busy { ProgressView() }
                }
                Section(localized("saved_routes_label")) {
                    if model.routes.isEmpty { Text(localized("none")) }
                    ForEach(model.routes) { Text($0.summary).font(.system(.body, design: .monospaced)) }
                }
                Section(localized("access_label")) {
                    LabeledContent(localized("proxy_host"), value: "127.0.0.1")
                    LabeledContent(localized("proxy_port"), value: String(model.settings.port))
                    LabeledContent(localized("proxy_user"), value: model.settings.user)
                    LabeledContent(localized("proxy_pass"), value: model.settings.pass)
                        .privacySensitive()
                }.textSelection(.enabled)
            }
            .navigationTitle("Passage")
            .disabled(model.busy)
            .alert(localized("import_title"), isPresented: Binding(get: { model.preview != nil }, set: { if !$0 { model.preview = nil } })) {
                Button(localized("save")) { Task { await model.confirmImport() } }
                Button(localized("cancel"), role: .cancel) {}
            } message: { Text(model.importMessage) }
            .alert(localized("status_label"), isPresented: Binding(get: { model.error != nil }, set: { if !$0 { model.error = nil } })) {
                Button("OK", role: .cancel) {}
            } message: { Text(model.error ?? "") }
            .sheet(isPresented: Binding(get: { model.diagnosis != nil }, set: { if !$0 { model.diagnosis = nil } })) {
                NavigationStack {
                    ScrollView([.horizontal, .vertical]) {
                        Text(model.diagnosis ?? "").font(.system(.caption, design: .monospaced)).padding().textSelection(.enabled)
                    }
                    .navigationTitle(localized("test_network"))
                    .toolbar { Button("OK") { model.diagnosis = nil } }
                }
            }
        }
    }
}
