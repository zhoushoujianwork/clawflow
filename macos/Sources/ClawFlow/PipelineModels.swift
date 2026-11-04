import Foundation

/// Evidence-backed state of one pipeline stage. `finished` means the job ended, never that delivery passed.
enum StageState: Sendable, Equatable {
    case pending, waiting, running, finished, failed, attention, notReported
    var text: String {
        switch self {
        case .pending: return "not started"
        case .waiting: return "waiting"
        case .running: return "running"
        case .finished: return "finished"
        case .failed: return "failed"
        case .attention: return "needs attention"
        case .notReported: return "not reported"
        }
    }
}

struct PipelineStage: Identifiable, Sendable {
    let id: String
    let title: String
    let state: StageState
    let detail: String?
    /// Jobs of this stage, newest first.
    let jobs: [TaskRow]
    var accessibilityLabel: String { "\(title): \(state.text). Job status, not a delivery verdict." }
}

/// One issue is one pipeline; jobs are evidence attached to it, not delivery verdicts.
struct IssuePipeline: Identifiable, Sendable {
    struct Identity: Hashable, Sendable { let repo: String; let issue: Int; let unlinkedPR: Int }
    let id: Identity
    let jobs: [TaskRow]
    let queue: IssueQueueRow?
    let acceptanceRequired: Bool
    /// Watchdog findings for this pipeline's jobs. Observe only.
    let findings: [Finding]
    /// Derived once when the snapshot is decoded so list rows never recompute it.
    let stages: [PipelineStage]
    init(id: Identity, jobs: [TaskRow], queue: IssueQueueRow?, acceptanceRequired: Bool = false, findings: [Finding] = []) {
        self.id = id; self.jobs = jobs; self.queue = queue
        self.acceptanceRequired = acceptanceRequired; self.findings = findings
        stages = Self.stages(jobs: jobs, queue: queue, acceptanceRequired: acceptanceRequired)
    }
    var repo: String { id.repo }
    var issue: Int { id.issue }
    var repoShortName: String { repo.split(separator: "/").last.map(String.init) ?? repo }
    var reference: String { issue > 0 ? "#\(issue)" : "PR #\(id.unlinkedPR) · issue unavailable" }
    var title: String {
        if let title = queue?.title, !title.isEmpty { return title }
        return jobs.first { !($0.subject ?? "").isEmpty }?.subject ?? reference
    }
    var prs: [Int] { Array(Set(jobs.map(\.pr).filter { $0 > 0 } + (id.unlinkedPR > 0 ? [id.unlinkedPR] : []))).sorted() }
    var group: TaskGroup {
        if !findings.isEmpty || jobs.contains(where: { $0.group == .attention }) || queue?.state.hasPrefix("blocked") == true { return .attention }
        if jobs.contains(where: { $0.group == .running }) { return .running }
        if jobs.contains(where: { $0.group == .waiting }) || queue != nil { return .waiting }
        return .history
    }
    var status: String {
        switch group {
        case .attention: return "Needs attention"
        case .running: return "Running"
        case .waiting: return "Waiting"
        case .history: return jobs.first?.state == "failed" ? "Latest job failed" : "Finished"
        }
    }
    var symbol: String {
        switch group { case .attention: return "exclamationmark.triangle"; case .running: return "arrow.triangle.2.circlepath"; case .waiting: return "clock"; case .history: return "checkmark.circle" }
    }
    var updatedAt: Date { jobs.first?.updatedAt ?? .distantPast }
    /// The running job, when any; its elapsed time is the pipeline's age.
    var runningJob: TaskRow? { jobs.first { $0.group == .running } }
    func jobs(kind: String) -> [TaskRow] { jobs.filter { $0.kind == kind } }
    /// Readable queue reason; raw scheduler codes never reach the interface.
    private var queueReason: String? { queue.map { $0.detail ?? $0.status } }
    /// Why the row is not moving, for the list. Nil when the status column already says it all.
    var rowNote: String? {
        switch group {
        case .attention: return reason
        case .waiting: return reason == "Waiting" ? nil : reason
        case .running, .history: return nil
        }
    }
    var reason: String {
        if let finding = findings.first { return finding.reason }
        switch group {
        case .attention:
            return jobs.first { $0.group == .attention }?.reason ?? queueReason ?? "Needs attention"
        case .running, .waiting:
            return jobs.first { $0.group == group }?.reason ?? queueReason ?? "Waiting"
        case .history:
            return "Job execution finished. CI, acceptance verdict and merge are not reported by this snapshot."
        }
    }

    /// Pure stage derivation. Never draws CI or merge, which this snapshot does not report.
    static func stages(jobs: [TaskRow], queue: IssueQueueRow?, acceptanceRequired: Bool) -> [PipelineStage] {
        let implement = jobs.filter { $0.kind == "implement" }
        let repair = jobs.filter { $0.kind == "repair" }
        let review = jobs.filter { $0.kind == "acceptance" }
        // Later work proves the earlier stages happened even when their jobs left the snapshot.
        let laterWork = !repair.isEmpty || !review.isEmpty || jobs.contains { $0.pr > 0 }
        var result = [admit(queue: queue, hasDevelopmentJob: !implement.isEmpty || laterWork)]
        let (implementState, implementDetail) = implement.first.map(jobState)
            ?? (laterWork ? (.notReported, "Job is no longer in this snapshot") : (.pending, nil))
        result.append(PipelineStage(id: "implement", title: "Implement", state: implementState, detail: implementDetail, jobs: implement))
        if let newest = repair.first {
            let (state, detail) = jobState(newest)
            result.append(PipelineStage(id: "repair", title: "Repair ×\(repair.count)", state: state, detail: detail, jobs: repair))
        }
        // The scheduler's verdict is about the current head, so it outranks an older finished review job.
        let reviewOutstanding = ["no_verdict", "acceptance_stale"].contains(queue?.reasonCode ?? "")
        if let newest = review.first, !reviewOutstanding || newest.group == .running || newest.group == .attention {
            let (state, detail) = jobState(newest)
            result.append(PipelineStage(id: "review", title: "Review", state: state, detail: detail, jobs: review))
        } else if reviewOutstanding {
            result.append(PipelineStage(id: "review", title: "Review", state: .waiting, detail: queue?.detail, jobs: review))
        } else if acceptanceRequired {
            result.append(PipelineStage(id: "review", title: "Review", state: .pending, detail: nil, jobs: []))
        }
        return result
    }
    private static func admit(queue: IssueQueueRow?, hasDevelopmentJob: Bool) -> PipelineStage {
        let state = queue?.state ?? ""
        let readable = queue.map { $0.detail ?? $0.status }
        func stage(_ value: StageState, _ detail: String?) -> PipelineStage {
            PipelineStage(id: "admit", title: "Admit", state: value, detail: detail, jobs: [])
        }
        if state.hasPrefix("waiting:coordination") || state.hasPrefix("waiting:needs-confirmation") || state.hasPrefix("waiting:dependency") {
            return stage(.waiting, readable)
        }
        if state.hasPrefix("blocked") { return stage(.attention, readable) }
        if state.hasPrefix("eligible:coordination checked") { return stage(.finished, "Coordination checked") }
        if state.hasPrefix("eligible") {
            return stage(.waiting, queue?.reasonCode == "max_in_flight" ? queue?.detail : "Waiting for a slot")
        }
        if hasDevelopmentJob || state.hasPrefix("in_flight") { return stage(.finished, nil) }
        return stage(.notReported, readable)
    }
    /// Maps one job to its stage state. A done job is `finished`, not passed.
    static func jobState(_ job: TaskRow) -> (StageState, String?) {
        if job.group == .attention { return (.attention, job.reason) }
        if job.group == .running { return (.running, job.status) }
        if job.state == "pending" { return (.waiting, job.reason) }
        switch job.state {
        case "done": return (.finished, nil)
        case "failed": return (.failed, job.reason)
        case "cancelled": return (.pending, "Cancelled")
        default: return (.notReported, nil)
        }
    }
}

extension DashboardSnapshot {
    func makeIssuePipelines() -> [IssuePipeline] {
        struct PRKey: Hashable { let repo: String; let pr: Int }
        var issuesByPR: [PRKey: Set<Int>] = [:]
        for job in board.tasks where job.issue > 0 && job.pr > 0 {
            issuesByPR[PRKey(repo: job.repo, pr: job.pr), default: []].insert(job.issue)
        }
        var grouped: [IssuePipeline.Identity: [TaskRow]] = [:]
        var queued: [IssuePipeline.Identity: IssueQueueRow] = [:]
        for membership in projectRepositories {
            for entry in membership.repo.issueQueue ?? [] {
                let key = IssuePipeline.Identity(repo: membership.repo.name, issue: entry.number, unlinkedPR: 0)
                queued[key] = entry
                if grouped[key] == nil { grouped[key] = [] }
            }
        }
        for job in board.tasks {
            var issue = job.issue
            if issue == 0, job.pr > 0,
               let candidates = issuesByPR[PRKey(repo: job.repo, pr: job.pr)], candidates.count == 1 {
                issue = candidates.first!
            }
            let key = IssuePipeline.Identity(repo: job.repo, issue: issue, unlinkedPR: issue > 0 ? 0 : job.pr)
            grouped[key, default: []].append(job)
        }
        var findingsByTask: [String: [Finding]] = [:]
        for finding in board.findings ?? [] { findingsByTask[finding.taskId, default: []].append(finding) }
        var acceptance: [String: Bool] = [:]
        for repo in projects.flatMap(\.repos) where acceptance[repo.name] == nil { acceptance[repo.name] = repo.acceptanceRequired }
        return grouped.map { key, jobs in
            let sorted = jobs.sorted { $0.updatedAt == $1.updatedAt ? $0.id < $1.id : $0.updatedAt > $1.updatedAt }
            return IssuePipeline(id: key, jobs: sorted, queue: queued[key], acceptanceRequired: acceptance[key.repo] ?? false,
                                 findings: sorted.flatMap { findingsByTask[$0.id] ?? [] })
        }.sorted {
            if $0.group != $1.group { return $0.group.rawValue < $1.group.rawValue }
            if $0.updatedAt != $1.updatedAt { return $0.updatedAt > $1.updatedAt }
            if $0.repo != $1.repo { return $0.repo < $1.repo }
            return $0.issue == $1.issue ? $0.id.unlinkedPR < $1.id.unlinkedPR : $0.issue < $1.issue
        }
    }
}
