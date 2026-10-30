import SwiftUI

/// Mirrors `clawflow execution status --json` (schema 1). The CLI owns readiness and
/// its on-disk cache; this app only presents the document and never probes Offload.
struct ExecutionGuidance: Decodable, Equatable {
    let steps: [String]
    let link: String?
    let recheck: String
}
struct WorkerReadiness: Decodable, Equatable, Identifiable {
    var id: String { worker ?? "this machine" }
    let state: String
    let reason: String?
    let summary: String
    let worker: String?
    let configuredCommand: String?
    let command: String?
    let profiles: [String]?
    let guidance: ExecutionGuidance?
    let unverified: [String]?
}
struct ExecutionStatus: Decodable, Equatable {
    let schemaVersion: Int
    let state: String
    let summary: String
    let workers: [WorkerReadiness]

    static func decode(_ data: Data) throws -> Self {
        let value = try decoder().decode(Self.self, from: data)
        guard value.schemaVersion == 1 else { throw DashboardError.message("Update ClawFlow: unsupported execution status schema.") }
        return value
    }
    var blocked: Bool { state == "blocked" }
    var needsAttention: Bool { state == "blocked" || state == "unreported" }
    /// Workers that lack a usable executor, by their service name.
    var affected: [WorkerReadiness] { workers.filter { $0.state == "blocked" || $0.state == "unreported" } }
    func readiness(forService label: String) -> WorkerReadiness? {
        let name = label.replacingOccurrences(of: "com.zhoushoujianwork.clawflow.worker-", with: "")
        return workers.first { $0.worker == name }
    }
    /// A stable key for one distinct condition, so repeated polls of the same failure
    /// raise at most one notification.
    var attentionKey: String? {
        needsAttention ? affected.map { "\($0.worker ?? "")|\($0.reason ?? $0.state)|\($0.summary)" }.sorted().joined(separator: ";") : nil
    }
    /// Text block shown identically in setup, service status and the menu.
    func lines(for row: WorkerReadiness) -> [String] {
        var out = [row.summary]
        if let configured = row.configuredCommand, row.state == "blocked" { out.append("Configured command: \(configured)") }
        if let guidance = row.guidance {
            out += guidance.steps.map { "• \($0)" }
            if let link = guidance.link { out.append("Docs: \(link)") }
            out.append("Recheck: \(guidance.recheck)")
        }
        if row.state == "ready" { out.append("Live authentication is not verified.") }
        return out
    }
}

@MainActor final class ExecutionReadiness: ObservableObject {
    @Published private(set) var status: ExecutionStatus?
    @Published private(set) var error: String?
    @Published private(set) var rechecking = false
    /// Called once per distinct blocked condition; cleared when the condition recovers.
    var notify: ((String) -> Void)?
    private var announced: String?
    private var inFlight = false

    /// Reads the CLI's shared cache (cheap; the CLI spawns a probe only when its cache expired).
    func refresh(cli: String) async { await run(cli: cli, recheck: false) }
    /// Explicit user action after installing Offload.
    func recheck(cli: String) async { await run(cli: cli, recheck: true) }

    private func run(cli: String, recheck: Bool) async {
        guard !inFlight else { return }
        inFlight = true; rechecking = recheck; defer { inFlight = false; rechecking = false }
        do {
            var args = ["execution", "status", "--json", "--no-fail"]
            if recheck { args.append("--recheck") }
            let data = try await BridgeProcess().run(executable: cli, arguments: args, timeout: 30)
            apply(try ExecutionStatus.decode(data))
        } catch {
            self.error = error.localizedDescription
        }
    }
    func apply(_ next: ExecutionStatus) {
        status = next; error = nil
        if let key = next.attentionKey {
            if key != announced { announced = key; notify?(next.summary) }
        } else {
            announced = nil
        }
    }
}

struct ExecutionReadinessView: View {
    @ObservedObject var readiness: ExecutionReadiness
    let cli: String
    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            Text("Offload execution").font(.headline)
            if let status = readiness.status {
                Label(status.summary, systemImage: status.needsAttention ? "exclamationmark.triangle" : status.state == "ready" ? "checkmark.circle" : "minus.circle")
                    .foregroundStyle(status.needsAttention ? .orange : .primary)
                ForEach(status.workers) { row in
                    VStack(alignment: .leading, spacing: 2) {
                        Text(row.worker ?? "This machine").font(.callout)
                        ForEach(Array(status.lines(for: row).enumerated()), id: \.offset) { _, line in
                            Text(line).font(.caption).foregroundStyle(.secondary).textSelection(.enabled)
                        }
                    }
                }
            } else {
                Text(readiness.error ?? "Checking Offload…").font(.caption).foregroundStyle(.secondary)
            }
            Button("Recheck Offload") { Task { await readiness.recheck(cli: cli) } }.disabled(readiness.rechecking)
            Text("Detection is read-only. Install Offload yourself, then recheck.").font(.caption).foregroundStyle(.secondary)
        }
    }
}

import UserNotifications

enum Notifier {
    /// No-op outside an application bundle (tests, `swift run`), where the notification center is unavailable.
    static func post(_ title: String, body: String) {
        guard Bundle.main.bundleIdentifier != nil, Bundle.main.bundleURL.pathExtension == "app" else { return }
        let center = UNUserNotificationCenter.current()
        center.requestAuthorization(options: [.alert]) { granted, _ in
            guard granted else { return }
            let content = UNMutableNotificationContent()
            content.title = title; content.body = body
            // A fixed identifier replaces an earlier delivery instead of stacking.
            UNUserNotificationCenter.current().add(UNNotificationRequest(identifier: "clawflow.offload-unavailable", content: content, trigger: nil))
        }
    }
}
