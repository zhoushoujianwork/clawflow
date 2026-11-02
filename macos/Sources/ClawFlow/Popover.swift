import Foundation

// Contract notes for the menu-bar popover (issue #610).
// Authoritative facts available from `clawflow dashboard snapshot`:
//   - per repository: `enabled` (monitoring), required CI, acceptance, auto-merge, worker tags;
//   - per worker: live, paused (assignments drained), revoked, capacity, active tasks;
//   - per coordinator task: repo, stage, waiting reason, application error.
// Contract gaps, shown as absent rather than invented:
//   - there is no per-project dispatch pause: "paused" for a repository means monitoring only;
//   - queued counts cover coordinator tasks, not open issues the monitor has not yet enqueued;
//   - there is no per-repository last-scan time.

/// Human-readable age such as "just now", "12s ago" or "3 min ago".
func ageText(from date: Date, now: Date) -> String {
    let seconds = max(0, Int(now.timeIntervalSince(date)))
    if seconds < 5 { return "just now" }
    if seconds < 60 { return "\(seconds)s ago" }
    if seconds < 3600 { return "\(seconds / 60) min ago" }
    if seconds < 86400 { return "\(seconds / 3600) h ago" }
    return "\(seconds / 86400) d ago"
}

struct PendingAction: Equatable {
    let action: String
    let name: String
    var text: String {
        switch action {
        case "disable": return "Pausing monitoring for \(name)…"
        case "enable": return "Resuming monitoring for \(name)…"
        default: return "Applying \(action) to \(name)…"
        }
    }
}

struct PopoverTask: Identifiable, Equatable {
    let id: String
    let title: String
    let detail: String
    let url: URL?
    var accessibilityLabel: String { "\(title). \(detail)" }
}

struct PopoverSection: Equatable {
    static let empty = PopoverSection(items: [], total: 0)
    let items: [PopoverTask]
    /// Total in the group; `items` is bounded and the remainder is reached in the dashboard.
    let total: Int
    var hidden: Int { max(0, total - items.count) }
}

struct PopoverRepo: Identifiable, Equatable {
    var id: String { name }
    let name: String
    let project: String
    let url: URL?
    let monitoring: Bool
    let stateLabel: String
    let counts: String
    let policies: String
    /// Says what pausing or resuming does, and what it does not touch.
    let semantics: String
    var accessibilityLabel: String { "\(name), project \(project). \(stateLabel). \(counts). \(policies)" }
}

struct StatusPopover {
    static let taskLimit = 4

    let summary: StatusSummary
    let hasData: Bool
    let isStale: Bool
    let connectivity: String
    let detail: String?
    let freshness: String?
    let workerLine: String
    let explanation: String
    let blockers: PopoverSection
    let running: PopoverSection
    let queued: PopoverSection
    let monitoring: [PopoverRepo]
    let paused: [PopoverRepo]
    let projectNote: String
    var controlsEnabled: Bool { summary.connected }
    var repoCount: Int { monitoring.count + paused.count }

    static func make(snapshot: DashboardSnapshot?, received: Date?, error: String?, now: Date = Date()) -> Self {
        let summary = StatusSummary.reduce(snapshot: snapshot, received: received, error: error, now: now)
        let firstError = error?.components(separatedBy: .newlines).first { !$0.trimmingCharacters(in: .whitespaces).isEmpty }
        guard let snapshot, let received else {
            return Self(summary: summary, hasData: false, isStale: false,
                        connectivity: summary.kind == .unavailable ? "Controller unavailable" : "Not connected",
                        detail: firstError, freshness: nil,
                        workerLine: "Workers: unknown",
                        explanation: "Status is unknown, not idle. Choose a controller in Settings; work continues without this app.",
                        blockers: .empty, running: .empty, queued: .empty, monitoring: [], paused: [],
                        projectNote: "Projects unavailable until the controller responds.")
        }
        let stale = summary.kind == .stale
        let tasks = snapshot.board.tasks
        let attention = tasks.filter { $0.group == .attention }
        let running = tasks.filter { $0.group == .running }
        let queued = tasks.filter { $0.group == .waiting }
        let repos = repoRows(snapshot: snapshot)
        let monitoring = repos.filter(\.monitoring), paused = repos.filter { !$0.monitoring }
        let workers = readiness(snapshot.board.workers)
        let age = ageText(from: received, now: now)
        return Self(
            summary: summary, hasData: true, isStale: stale,
            connectivity: stale ? "Connection lost" : "Connected to \(snapshot.controller)",
            detail: stale ? firstError : nil,
            freshness: stale ? "Last known data from \(age). It may have changed." : "Updated \(age)",
            workerLine: workers.line,
            explanation: explain(stale: stale, attention: attention, running: running, queued: queued,
                                 workers: workers, monitoring: monitoring.count, paused: paused.count),
            blockers: section(attention, snapshot: snapshot) { $0.reason },
            running: section(running, snapshot: snapshot) { "Running · \($0.worker.isEmpty ? "Unassigned" : $0.worker)" },
            queued: section(queued, snapshot: snapshot) { $0.reason },
            monitoring: monitoring, paused: paused,
            projectNote: repos.isEmpty ? "No projects configured." : "Counts are controller tasks. Pausing monitoring stops new issue pickup only.")
    }

    private struct Readiness {
        var ready = 0, total = 0, offline = 0, draining = 0, freeSlots = 0
        var line: String {
            if total == 0 { return "Workers: none registered" }
            var parts = ["\(ready) of \(total) ready"]
            if offline > 0 { parts.append("\(offline) offline") }
            if draining > 0 { parts.append("\(draining) not accepting new assignments") }
            return "Workers: " + parts.joined(separator: " · ")
        }
    }
    private static func readiness(_ workers: [WorkerRow]) -> Readiness {
        var value = Readiness()
        for worker in workers where !worker.revoked {
            value.total += 1
            if !worker.live { value.offline += 1 }
            else if worker.paused { value.draining += 1 }
            else { value.ready += 1; value.freeSlots += max(0, worker.maxParallel - worker.runningTasks.count) }
        }
        return value
    }
    private static func explain(stale: Bool, attention: [TaskRow], running: [TaskRow], queued: [TaskRow], workers: Readiness, monitoring: Int, paused: Int) -> String {
        if stale { return "The controller cannot be reached. Counts below are last known; workers and the controller keep running independently." }
        var parts: [String] = []
        if !attention.isEmpty { parts.append("\(attention.count) \(attention.count == 1 ? "task needs" : "tasks need") attention.") }
        if !running.isEmpty { parts.append("\(running.count) running.") }
        if !queued.isEmpty {
            if workers.ready == 0 { parts.append("\(queued.count) queued: no worker is ready.") }
            else if workers.freeSlots == 0 { parts.append("\(queued.count) queued: all ready workers are at capacity.") }
            else if let reason = queued.compactMap({ $0.waitingReason }).first(where: { !$0.isEmpty }) { parts.append("\(queued.count) queued: \(reason)") }
            else { parts.append("\(queued.count) queued: waiting for a worker whose tags match.") }
        }
        if parts.isEmpty {
            if monitoring == 0 && paused > 0 { parts.append("Idle: monitoring is paused for every repository, so no new issues are picked up.") }
            else if monitoring + paused == 0 { parts.append("Idle: no repositories are configured.") }
            else { parts.append("Idle: nothing running or queued.") }
            if workers.ready == 0 { parts.append("No worker is ready, so new work would wait.") }
        }
        return parts.joined(separator: " ")
    }
    private static func section(_ rows: [TaskRow], snapshot: DashboardSnapshot, detail: (TaskRow) -> String) -> PopoverSection {
        PopoverSection(items: rows.prefix(taskLimit).map {
            PopoverTask(id: $0.id, title: $0.title, detail: detail($0), url: $0.link(in: snapshot))
        }, total: rows.count)
    }
    private static func repoRows(snapshot: DashboardSnapshot) -> [PopoverRepo] {
        var counts: [String: (running: Int, queued: Int, attention: Int)] = [:]
        for task in snapshot.board.tasks {
            var value = counts[task.repo] ?? (0, 0, 0)
            switch task.group {
            case .running: value.running += 1
            case .waiting: value.queued += 1
            case .attention: value.attention += 1
            case .history: break
            }
            counts[task.repo] = value
        }
        return snapshot.projects.flatMap { project in
            project.repos.map { repo in
                let c = counts[repo.name] ?? (0, 0, 0)
                var text = (c.running + c.queued + c.attention == 0) ? "No active or queued tasks" : "\(c.running) running · \(c.queued) queued"
                if c.attention > 0 { text += " · \(c.attention) need attention" }
                return PopoverRepo(
                    name: repo.name, project: project.name, url: safeWebURL(repo.url), monitoring: repo.enabled,
                    stateLabel: repo.enabled ? "Monitoring" : "Monitoring paused", counts: text,
                    policies: "CI \(repo.ciRequired ? "required" : "optional") · Acceptance \(repo.acceptanceRequired ? "required" : "optional") · Auto-merge \(repo.autoMerge ? "on" : "off")",
                    semantics: repo.enabled
                        ? "Pause stops picking up new issues. Queued and running tasks and workers are not stopped."
                        : "No new issues are picked up. Queued and running tasks continue; worker availability is separate.")
            }
        }
    }
}
