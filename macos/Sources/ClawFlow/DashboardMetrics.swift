import Foundation

/// Delivery-rhythm figures from the retained snapshot only. Job outcomes, not delivery verdicts.
/// Values that have no evidence are nil and render as "—", never as zero.
struct DashboardMetrics: Equatable, Sendable {
    struct Day: Equatable, Sendable { let start: Date; let done: Int; let failed: Int }
    /// Jobs in scope; the header states this as the statistics range.
    let jobCount: Int
    let todayDone: Int?
    let todayFailed: Int?
    let waiting: Int
    /// Done / (done + failed) over retained jobs, 0...1.
    let successRate: Double?
    let medianImplementDuration: TimeInterval?
    let repairJobs: Int?
    /// Seven local calendar days ending today, oldest first.
    let days: [Day]
    var dayMax: Int { max(1, days.map { $0.done + $0.failed }.max() ?? 0) }

    static func make(tasks: [TaskRow], pipelines: [IssuePipeline], now: Date, calendar: Calendar = .current) -> Self {
        let today = calendar.startOfDay(for: now)
        let hasJobs = !tasks.isEmpty
        let todays = tasks.filter { $0.updatedAt >= today }
        let done = tasks.filter { $0.state == "done" }.count
        let failed = tasks.filter { $0.state == "failed" }.count
        let durations = tasks.compactMap { task -> TimeInterval? in
            guard task.kind == "implement", task.state == "done", let start = task.startedAt, let end = task.endedAt else { return nil }
            return end.timeIntervalSince(start)
        }.sorted()
        let days: [Day] = (0..<7).reversed().compactMap { offset in
            guard let start = calendar.date(byAdding: .day, value: -offset, to: today),
                  let end = calendar.date(byAdding: .day, value: 1, to: start) else { return nil }
            let rows = tasks.filter { $0.updatedAt >= start && $0.updatedAt < end }
            return Day(start: start, done: rows.filter { $0.state == "done" }.count, failed: rows.filter { $0.state == "failed" }.count)
        }
        return Self(
            jobCount: tasks.count,
            todayDone: hasJobs ? todays.filter { $0.state == "done" }.count : nil,
            todayFailed: hasJobs ? todays.filter { $0.state == "failed" }.count : nil,
            waiting: pipelines.filter { $0.group == .waiting }.count,
            successRate: done + failed > 0 ? Double(done) / Double(done + failed) : nil,
            medianImplementDuration: durations.isEmpty ? nil : durations[durations.count / 2],
            repairJobs: hasJobs ? tasks.filter { $0.kind == "repair" }.count : nil,
            days: days)
    }
}

/// Worker slot usage for the overview bar.
struct WorkerCapacity: Equatable, Sendable {
    struct Worker: Equatable, Sendable { let name: String; let running: Int; let slots: Int }
    let workers: [Worker]
    let live: Int
    let total: Int
    let running: Int
    /// Leases that are lost or awaiting recovery and still hold capacity.
    let held: Int
    /// Position of the held-lease mark, 0...1, or nil when nothing is held.
    var heldPosition: Double? { held > 0 && total > 0 ? min(Double(running + held) / Double(total), 0.99) : nil }

    static func make(_ board: WorkerBoard) -> Self {
        Self(workers: board.workers.map { Worker(name: $0.name, running: $0.runningTasks.count, slots: $0.maxParallel) },
             live: board.workers.filter(\.live).count,
             total: board.workers.reduce(0) { $0 + $1.maxParallel },
             running: board.workers.reduce(0) { $0 + $1.runningTasks.count },
             held: board.tasks.filter { $0.stage == "recovery_pending" || $0.stage == "lease_expired" }.count)
    }
}

extension DashboardSnapshot {
    /// Builds the per-filter cache: "" for all repositories, plus each repository with tasks or pipelines.
    func makeMetrics() -> [String: DashboardMetrics] {
        let now = board.generatedAt
        var result = ["": DashboardMetrics.make(tasks: board.tasks, pipelines: issuePipelines, now: now)]
        let repos = Set(board.tasks.map(\.repo)).union(issuePipelines.map(\.repo)).union(projects.flatMap(\.repos).map(\.name))
        for repo in repos {
            result[repo] = DashboardMetrics.make(tasks: board.tasks.filter { $0.repo == repo },
                                                 pipelines: issuePipelines.filter { $0.repo == repo }, now: now)
        }
        return result
    }
    /// Cached metrics for a repository filter ("" = all). Unknown filters compute an empty scope.
    func metrics(repo: String) -> DashboardMetrics {
        metricsByRepo[repo] ?? DashboardMetrics.make(tasks: [], pipelines: [], now: board.generatedAt)
    }
}
