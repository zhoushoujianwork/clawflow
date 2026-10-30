import Foundation

struct DashboardSnapshot: Decodable {
    let schemaVersion: Int
    let version: String
    let controller: String
    let board: WorkerBoard
    let projects: [ProjectRow]

    static func decode(_ data: Data) throws -> Self {
        let value = try decoder().decode(Self.self, from: data)
        guard value.schemaVersion == 1 else { throw DashboardError.message("Update ClawFlow: unsupported controller schema.") }
        return value
    }
    func repository(_ name: String) -> RepoRow? { projects.flatMap(\.repos).first { $0.name == name } }
}

func decoder() -> JSONDecoder {
    let decoder = JSONDecoder()
    decoder.keyDecodingStrategy = .convertFromSnakeCase
    decoder.dateDecodingStrategy = .custom { decoder in
        let text = try decoder.singleValueContainer().decode(String.self)
        let fractional = ISO8601DateFormatter()
        fractional.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        if let date = fractional.date(from: text) ?? ISO8601DateFormatter().date(from: text) { return date }
        throw DashboardError.message("Invalid controller timestamp.")
    }
    return decoder
}

struct WorkerBoard: Decodable {
    let generatedAt: Date
    let workers: [WorkerRow]
    let tasks: [TaskRow]
}
struct WorkerRow: Decodable, Identifiable {
    var id: String { name }
    let name: String
    let tags: [String]
    let roles: [String]
    let live: Bool
    let paused: Bool
    let revoked: Bool
    let maxParallel: Int
    let lastSeen: Date?
    let lastHeartbeat: Date?
    let runningTasks: [String]
    var state: String { revoked ? "Removed" : !live ? "Offline" : paused ? "Assignments paused" : "Ready" }
}
enum TaskGroup: Int { case attention, running, waiting, history }
enum TaskActivity {
    case development, review, repair, reply, unknown
    init(kind: String) {
        switch kind { case "implement": self = .development; case "acceptance": self = .review; case "repair": self = .repair; case "reply": self = .reply; default: self = .unknown }
    }
    var title: String {
        switch self { case .development: return "Development"; case .review: return "PR Review"; case .repair: return "PR Repair"; case .reply: return "Issue Reply"; case .unknown: return "Unknown activity" }
    }
    var symbol: String {
        switch self { case .development: return "hammer"; case .review: return "checkmark.shield"; case .repair: return "wrench.and.screwdriver"; case .reply: return "text.bubble"; case .unknown: return "questionmark.circle" }
    }
}
struct TaskRow: Decodable, Identifiable {
	let subject: String?
    let id: String
    let repo: String
    let issue: Int
    let pr: Int
    let kind: String
    let state: String
    let stage: String
    let worker: String
    let attempt: Int
    let updatedAt: Date
    let waitingReason: String?
    let applicationError: String?
    let tags: [String]
    var activity: TaskActivity { TaskActivity(kind: kind) }
    var group: TaskGroup {
        if ["done", "failed", "cancelled"].contains(state) { return .history }
        if stage == "lease_expired" || !(applicationError ?? "").isEmpty { return .attention }
        if stage == "running" || stage == "finalizing" { return .running }
        if state == "pending" { return .waiting }
        return .attention
    }
    var title: String { "\((subject?.isEmpty == false ? subject : nil) ?? kind.capitalized) · \(repo)\(issue > 0 ? "#\(issue)" : " PR #\(pr)")" }
    var status: String {
        switch stage {
        case "lease_expired": return "Connection lost"
        case "running": return "Running"
        case "finalizing": return "Finalizing result"
        case "pending": return "Waiting"
        case "done": return "Attempt completed"
        case "failed": return "Attempt failed"
        case "cancelled": return "Cancelled"
        default: return "Unknown state"
        }
    }
    var reason: String {
        if let error = applicationError, !error.isEmpty { return error }
        if let reason = waitingReason, !reason.isEmpty { return reason }
        if stage == "lease_expired" { return "The worker lease expired. The controller owns reconciliation; do not duplicate this attempt." }
        if state == "done" { return "This attempt finished. CI, acceptance and merge remain separate delivery gates." }
        if state == "pending" { return "Waiting for an eligible worker and available capacity." }
        return status
    }
    var symbol: String {
        switch group { case .attention: return "exclamationmark.triangle"; case .running: return "arrow.triangle.2.circlepath"; case .waiting: return "hourglass"; case .history: return state == "done" ? "checkmark.circle" : "minus.circle" }
    }
    func link(in snapshot: DashboardSnapshot) -> URL? {
        guard let repo = snapshot.repository(repo), let base = safeWebURL(repo.url) else { return nil }
        if issue > 0 { return base.appendingPathComponent(repo.platform == "gitlab" ? "-/issues/\(issue)" : "issues/\(issue)") }
        guard pr > 0 else { return nil }
        return base.appendingPathComponent(repo.platform == "gitlab" ? "-/merge_requests/\(pr)" : "pull/\(pr)")
    }
    func prLink(in snapshot: DashboardSnapshot) -> URL? {
        guard pr > 0, let repo = snapshot.repository(repo), let base = safeWebURL(repo.url) else { return nil }
        return base.appendingPathComponent(repo.platform == "gitlab" ? "-/merge_requests/\(pr)" : "pull/\(pr)")
    }
}
struct ProjectRow: Decodable, Identifiable {
    var id: String { name }
    let name: String
    let repos: [RepoRow]
}
struct RepoRow: Decodable, Identifiable {
    var id: String { name }
    let name: String
    let url: String
    let platform: String
    let enabled: Bool
    let ciRequired: Bool
    let acceptanceRequired: Bool
    let autoMerge: Bool
    let workerTags: [String]
}
struct TaskEvent: Decodable, Identifiable {
    var id: String { "\(at.timeIntervalSince1970):\(action)" }
    let at: Date
    let action: String
}
struct JoinOffer: Decodable {
    let name: String
    let secret: String
    let expiresAt: Date
}
enum DashboardError: LocalizedError {
    case message(String)
    var errorDescription: String? { if case let .message(text) = self { return text }; return nil }
}
func safeWebURL(_ text: String) -> URL? {
    guard let u = URL(string: text), ["https", "http"].contains(u.scheme), u.host != nil, u.user == nil, u.password == nil else { return nil }
    return u
}

struct StatusSummary {
    let title: String
    let symbol: String
    let count: String
    let connected: Bool
    static func reduce(snapshot: DashboardSnapshot?, received: Date?, error: String?, now: Date = Date()) -> Self {
        guard let snapshot, let received else {
            return Self(title: error == nil ? "Not connected" : "Unavailable", symbol: "questionmark.circle", count: "–", connected: false)
        }
        guard error == nil, now.timeIntervalSince(received) < 20,
              now.timeIntervalSince(snapshot.board.generatedAt) < 30,
              snapshot.board.generatedAt.timeIntervalSince(now) < 5 else {
            return Self(title: "Connection lost · last known data", symbol: "clock.badge.exclamationmark", count: "–", connected: false)
        }
        let running = snapshot.board.tasks.filter { $0.group == .running }.count
        if snapshot.board.tasks.contains(where: { $0.group == .attention }) {
            return Self(title: "Needs attention", symbol: "exclamationmark.triangle", count: "\(running)", connected: true)
        }
        if running > 0 { return Self(title: "Working", symbol: "arrow.triangle.2.circlepath", count: "\(running)", connected: true) }
        let waiting = snapshot.board.tasks.contains { $0.group == .waiting }
        return Self(title: waiting ? "Waiting" : "Idle", symbol: waiting ? "hourglass" : "circle", count: "0", connected: true)
    }
}
