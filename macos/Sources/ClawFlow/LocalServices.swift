import SwiftUI

struct LocalService: Identifiable {
    let id: String
    let path: String
    var loaded = false
    var running = false
    var pid: String?
    var error: String?
    var title: String { id.hasSuffix(".postgres") ? "Local database" : id.hasSuffix(".controller") ? "Local controller" : "Worker · " + id.replacingOccurrences(of: "com.zhoushoujianwork.clawflow.worker-", with: "") }
    var status: String { error != nil ? "Status unavailable" : running ? "Running · PID \(pid ?? "—")" : loaded ? "Loaded · not running" : "Stopped" }
    static func accepts(_ label: String, identity: AppIdentity = .current) -> Bool {
        identity.manages(label)
    }
    mutating func apply(_ text: String) {
        loaded = true; running = false; pid = nil; error = nil
        var depth = 0
        for line in text.components(separatedBy: .newlines) {
            let parts = line.trimmingCharacters(in: .whitespaces).components(separatedBy: " = ")
            if depth == 1, parts.count == 2 {
                if parts[0] == "state" { running = parts[1] == "running" }
                if parts[0] == "pid" { pid = parts[1] }
            }
            depth += line.filter { $0 == "{" }.count - line.filter { $0 == "}" }.count
        }
    }
    func arguments(for action: String, uid: UInt32) -> [String] {
        let domain = "gui/\(uid)"
        switch action {
        case "stop": return ["bootout", domain + "/" + id]
        case "restart" where loaded: return ["kickstart", "-k", domain + "/" + id]
        case "start" where loaded: return ["kickstart", domain + "/" + id]
        default: return ["bootstrap", domain, path]
        }
    }
}

@MainActor final class LocalServices: ObservableObject {
    @Published var rows: [LocalService] = []
    @Published var busy = false
    @Published var message: String?
    private var refreshing = false
    private var loop: Task<Void, Never>?
    func start(ensureRunning: Bool = false, identity: AppIdentity = .current) {
        guard loop == nil else { return }
        loop = Task { [weak self] in
            if ensureRunning && identity.startsHostServices { await self?.ensureHostRunning() }
            while !Task.isCancelled {
                await self?.refresh()
                try? await Task.sleep(for: .seconds(5))
            }
        }
    }
    private func ensureHostRunning() async {
        await refresh()
        guard rows.contains(where: { $0.id.hasSuffix(".controller") }) else { return }
        let ordered = rows.sorted { priority($0) < priority($1) }
        for row in ordered where !row.loaded && row.error == nil {
            await perform("start", service: row)
        }
    }
    private func priority(_ service: LocalService) -> Int {
        service.id.hasSuffix(".postgres") ? 0 : service.id.hasSuffix(".controller") ? 1 : 2
    }
    func refresh() async {
        guard !refreshing else { return }; refreshing = true; defer { refreshing = false }
        let folder = FileManager.default.homeDirectoryForCurrentUser.appendingPathComponent("Library/LaunchAgents")
        let files = (try? FileManager.default.contentsOfDirectory(at: folder, includingPropertiesForKeys: nil)) ?? []
        var next: [LocalService] = []
        for file in files.sorted(by: { $0.path < $1.path }) where file.pathExtension == "plist" {
            guard let data = try? Data(contentsOf: file), let plist = try? PropertyListSerialization.propertyList(from: data, format: nil) as? [String: Any],
                  let label = plist["Label"] as? String, LocalService.accepts(label) else { continue }
            var row = LocalService(id: label, path: file.path)
            do {
                let data = try await BridgeProcess().run(executable: "/bin/launchctl", arguments: ["print", "gui/\(getuid())/\(label)"], timeout: 5)
                row.apply(String(decoding: data, as: UTF8.self))
            } catch {
                let text = (error as? BridgeFailure)?.diagnostic ?? error.localizedDescription
                if !text.localizedCaseInsensitiveContains("could not find service") { row.error = text }
            }
            next.append(row)
        }
        rows = next
    }
    func perform(_ action: String, service: LocalService) async {
        guard !busy, ["start", "stop", "restart"].contains(action), LocalService.accepts(service.id), service.error == nil else { return }
        busy = true; message = nil; defer { busy = false }
        do {
            _ = try await BridgeProcess().run(executable: "/bin/launchctl", arguments: service.arguments(for: action, uid: getuid()), timeout: 10)
            await refresh()
            message = "\(action.capitalized) request accepted. Status updates automatically."
        } catch { message = error.localizedDescription }
    }
}

struct LocalServiceControls: View {
    @ObservedObject var services: LocalServices
    @State private var pending: LocalService?
    @State private var action = ""
    @State private var confirming = false
    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            Text("On this Mac").font(.headline)
            if services.rows.isEmpty { Text("No installed local ClawFlow services.").font(.caption).foregroundStyle(.secondary) }
            ForEach(services.rows) { row in
                VStack(alignment: .leading, spacing: 5) {
                    Text(row.title).font(.callout)
                    Text(row.status).font(.caption).foregroundStyle(.secondary)
                    HStack {
                        Button("Start") { Task { await services.perform("start", service: row) } }.disabled(row.running)
                        Button("Restart") { pending = row; action = "restart"; confirming = true }.disabled(!row.loaded)
                        Button("Stop") { pending = row; action = "stop"; confirming = true }.disabled(!row.loaded)
                    }.disabled(services.busy || row.error != nil)
                }
            }
            if let message = services.message { Text(message).font(.caption).foregroundStyle(.secondary) }
            Text("These controls affect this Mac only. Quitting the app leaves services running.").font(.caption).foregroundStyle(.secondary)
        }
        .confirmationDialog("\(action.capitalized) \(pending?.title ?? "service")?", isPresented: $confirming) {
            Button(action.capitalized, role: .destructive) {
                if let pending { Task { await services.perform(action, service: pending) } }
            }
        } message: { Text("This interrupts active work handled by this local service. Use Stop new assignments in Workers to drain work first.") }
    }
}
