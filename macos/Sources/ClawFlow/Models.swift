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
    var projectRepositories: [ProjectRepository] {
        projects.flatMap { project in project.repos.map { ProjectRepository(project: project.name, repo: $0) } }
    }
}

/// A repository can belong to several projects. List sections must identify the membership.
struct ProjectRepository: Identifiable {
    struct Identity: Hashable { let project: String; let repo: String }
    let project: String
    let repo: RepoRow
    var id: Identity { Identity(project: project, repo: repo.name) }
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
    let startedAt: Date?
    let endedAt: Date?
    var durationAvailable: Bool { startedAt != nil && (group != .history || endedAt != nil) }
    func elapsed(at now: Date) -> String {
        guard durationAvailable, let startedAt else { return "Duration unavailable" }
        let seconds = max(0, Int((endedAt ?? now).timeIntervalSince(startedAt)))
        return String(format: "%02d:%02d:%02d", seconds / 3600, (seconds / 60) % 60, seconds % 60)
    }

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
        if stage == "lease_expired" || stage == "recovery_pending" || !(applicationError ?? "").isEmpty { return .attention }
        if stage == "running" || stage == "finalizing" { return .running }
        if state == "pending" { return .waiting }
        return .attention
    }
    var title: String { "\((subject?.isEmpty == false ? subject : nil) ?? kind.capitalized) · \(repo)\(issue > 0 ? "#\(issue)" : " PR #\(pr)")" }
    var status: String {
        switch stage {
        case "lease_expired": return "Connection lost"
        case "recovery_pending": return "Needs recovery"
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
struct IssueQueueRow: Decodable, Identifiable {
    var id: String { key }
    let key: String
    let position: Int
    let number: Int
    let title: String?
    let tier: String
    let state: String
    var status: String {
        if state.hasPrefix("waiting:needs-confirmation") || state.hasPrefix("waiting:coordination_needs_confirmation") { return "Needs confirmation" }
        if state.hasPrefix("closed:superseded") { return "Superseded" }
        if state.hasPrefix("closed:already-delivered") { return "Already delivered" }
        if state.hasPrefix("eligible:coordination checked") { return "Ready / coordination checked" }
        switch state.split(separator: ":").first.map(String.init) {
        case "in_flight": return "In progress"
        case "eligible": return "Ready / waiting for capacity"
        case "blocked": return "Blocked"
        case "parked": return "Parked"
        case "waiting": return "Waiting"
        default: return "Unknown"
        }
    }
}
struct RepoRow: Decodable, Identifiable {
    let issueQueue: [IssueQueueRow]?
    let queueUpdatedAt: Date?
    let queueError: String?
    let skippedIssues: Int?
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

enum StatusKind: Equatable { case unknown, unavailable, stale, attention, running, waiting, idle }

struct StatusSummary {
    let title: String
    let symbol: String
    /// Running tasks, or "–" when status is unknown or stale. Never a guessed zero.
    let count: String
    let connected: Bool
    var kind: StatusKind = .idle
    /// Task counts from the controller snapshot; nil while unknown or when only last-known data exists.
    var attention: Int?
    var running: Int?
    var waiting: Int?

    /// Compact text beside the menu-bar mark. Icons never carry state alone.
    var menuText: String? {
        switch kind {
        case .attention: return "\(attention ?? 0)"
        case .running: return "\(running ?? 0)"
        case .waiting: return "\(waiting ?? 0)"
        default: return nil
        }
    }
    var menuSymbol: String? {
        switch kind {
        case .unknown, .unavailable: return "questionmark"
        case .stale: return "slash.circle"
        case .attention: return "exclamationmark"
        case .waiting: return "hourglass"
        case .running, .idle: return nil
        }
    }
    var accessibilityLabel: String {
        switch kind {
        case .unknown: return "ClawFlow: not connected, status unknown"
        case .unavailable: return "ClawFlow: controller unavailable, status unknown"
        case .stale: return "ClawFlow: connection lost, showing last known data"
        case .attention: return "ClawFlow: needs attention, \(attention ?? 0) need attention, \(running ?? 0) running, \(waiting ?? 0) queued"
        case .running: return "ClawFlow: working, \(running ?? 0) running, \(waiting ?? 0) queued"
        case .waiting: return "ClawFlow: waiting, \(waiting ?? 0) queued, none running"
        case .idle: return "ClawFlow: idle, nothing running or queued"
        }
    }

    static func reduce(snapshot: DashboardSnapshot?, received: Date?, error: String?, now: Date = Date()) -> Self {
        guard let snapshot, let received else {
            return Self(title: error == nil ? "Not connected" : "Unavailable", symbol: "questionmark.circle", count: "–", connected: false,
                        kind: error == nil ? .unknown : .unavailable)
        }
        guard error == nil, now.timeIntervalSince(received) < 20,
              now.timeIntervalSince(snapshot.board.generatedAt) < 30,
              snapshot.board.generatedAt.timeIntervalSince(now) < 5 else {
            return Self(title: "Connection lost · last known data", symbol: "clock.badge.exclamationmark", count: "–", connected: false, kind: .stale)
        }
        let running = snapshot.board.tasks.filter { $0.group == .running }.count
        let attention = snapshot.board.tasks.filter { $0.group == .attention }.count
        let waiting = snapshot.board.tasks.filter { $0.group == .waiting }.count
        if attention > 0 {
            return Self(title: "Needs attention", symbol: "exclamationmark.triangle", count: "\(running)", connected: true,
                        kind: .attention, attention: attention, running: running, waiting: waiting)
        }
        if running > 0 {
            return Self(title: "Working", symbol: "arrow.triangle.2.circlepath", count: "\(running)", connected: true,
                        kind: .running, attention: 0, running: running, waiting: waiting)
        }
        return Self(title: waiting > 0 ? "Waiting" : "Idle", symbol: waiting > 0 ? "hourglass" : "circle", count: "0", connected: true,
                    kind: waiting > 0 ? .waiting : .idle, attention: 0, running: 0, waiting: waiting)
    }
}
