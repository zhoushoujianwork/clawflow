import Foundation

/// One issue is one pipeline; jobs are evidence attached to it, not delivery verdicts.
struct IssuePipeline: Identifiable {
    struct Identity: Hashable { let repo: String; let issue: Int; let unlinkedPR: Int }
    let id: Identity
    let jobs: [TaskRow]
    let queue: IssueQueueRow?
    var repo: String { id.repo }
    var issue: Int { id.issue }
    var reference: String { issue > 0 ? "#\(issue)" : "PR #\(id.unlinkedPR) · issue unavailable" }
    var title: String {
        if let title = queue?.title, !title.isEmpty { return title }
        return jobs.first { !($0.subject ?? "").isEmpty }?.subject ?? reference
    }
    var prs: [Int] { Array(Set(jobs.map(\.pr).filter { $0 > 0 } + (id.unlinkedPR > 0 ? [id.unlinkedPR] : []))).sorted() }
    var group: TaskGroup {
        if jobs.contains(where: { $0.group == .attention }) || queue?.state.hasPrefix("blocked") == true { return .attention }
        if jobs.contains(where: { $0.group == .running }) { return .running }
        if jobs.contains(where: { $0.group == .waiting }) || queue != nil { return .waiting }
        return .history
    }
    var status: String {
        switch group {
        case .attention: return "Needs attention"
        case .running: return "Running"
        case .waiting: return queue?.status ?? "Waiting"
        case .history: return jobs.first?.state == "failed" ? "Latest job failed" : "Jobs finished"
        }
    }
    var symbol: String {
        switch group { case .attention: return "exclamationmark.triangle"; case .running: return "arrow.triangle.2.circlepath"; case .waiting: return "hourglass"; case .history: return "circle.dashed" }
    }
    var updatedAt: Date { jobs.first?.updatedAt ?? .distantPast }
    func jobs(kind: String) -> [TaskRow] { jobs.filter { $0.kind == kind } }
    var reason: String {
        if group != .history { return jobs.first { $0.group == group }?.reason ?? queue?.state ?? "Waiting" }
        return "Job execution finished. CI, acceptance verdict and merge are not reported by this snapshot."
    }
}

extension DashboardSnapshot {
    var issuePipelines: [IssuePipeline] {
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
        return grouped.map { key, jobs in
            IssuePipeline(id: key, jobs: jobs.sorted { $0.updatedAt == $1.updatedAt ? $0.id < $1.id : $0.updatedAt > $1.updatedAt }, queue: queued[key])
        }.sorted {
            if $0.group != $1.group { return $0.group.rawValue < $1.group.rawValue }
            if $0.updatedAt != $1.updatedAt { return $0.updatedAt > $1.updatedAt }
            if $0.repo != $1.repo { return $0.repo < $1.repo }
            return $0.issue == $1.issue ? $0.id.unlinkedPR < $1.id.unlinkedPR : $0.issue < $1.issue
        }
    }
}
