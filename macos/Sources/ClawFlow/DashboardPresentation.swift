import Foundation

/// How a queued issue reads in the dashboard. Raw scheduler states stay in
/// tooltips; people see `status` and `detail`.
enum QueueTone { case active, ready, blocked, waiting, neutral }

extension IssueQueueRow {
    var tone: QueueTone {
        switch state.split(separator: ":").first.map(String.init) {
        case "in_flight": return .active
        case "eligible": return .ready
        case "blocked": return .blocked
        case "parked", "waiting": return .waiting
        default: return .neutral
        }
    }

    /// The scheduler reason code after the first colon, without any coordination sentence.
    var reasonCode: String? {
        let parts = state.split(separator: ":", maxSplits: 1).map(String.init)
        guard parts.count == 2 else { return nil }
        if let range = parts[1].range(of: " — ") { return String(parts[1][..<range.lowerBound]) }
        return parts[1]
    }

    /// One short reason, or nil when the status already says everything.
    var detail: String? {
        let parts = state.split(separator: ":", maxSplits: 1).map(String.init)
        guard parts.count == 2 else { return nil }
        // Coordination reasons carry their own sentence after the code.
        if let range = parts[1].range(of: " — ") { return String(parts[1][range.upperBound...]) }
        switch parts[1] {
        case "no_verdict": return "PR open, awaiting acceptance"
        case "acceptance_stale": return "PR changed, acceptance out of date"
        case "dependency": return "Waiting for a dependency"
        case "max_in_flight": return "Repository slots full"
        case "area_busy": return "Overlaps an open PR"
        case "coordination checked": return nil
        default: return parts[1].replacingOccurrences(of: "_", with: " ").capitalizedFirst
        }
    }
}

extension String {
    var capitalizedFirst: String { prefix(1).uppercased() + dropFirst() }
}

/// The interface is English; SwiftUI's relative style would follow the system
/// language and mix both within one line.
func relativeAge(_ date: Date, now: Date) -> String {
    let minutes = Int(now.timeIntervalSince(date)) / 60
    if minutes < 1 { return "just now" }
    if minutes < 60 { return "\(minutes) min ago" }
    if minutes < 48 * 60 { return "\(minutes / 60) h \(minutes % 60) min ago" }
    return "\(minutes / (24 * 60)) days ago"
}

/// Labels the controller's throughput window from its `since` field instead of assuming 24 hours.
func throughputLabel(_ value: Throughput, generatedAt: Date) -> String {
    let counts = "\(value.done) done · \(value.failed) failed"
    let window = generatedAt.timeIntervalSince(value.since)
    let hours = (window / 3600).rounded()
    if hours >= 1, abs(window - hours * 3600) < 60 { return "Last \(Int(hours)) h: \(counts)" }
    let format = DateFormatter()
    format.locale = Locale(identifier: "en_US_POSIX")
    format.dateFormat = "HH:mm"
    return "Since \(format.string(from: value.since)): \(counts)"
}

extension TaskRow {
    /// The list and detail show the repository separately, so it is not repeated here.
    var displayTitle: String { subject?.isEmpty == false ? subject! : kind.capitalized }
    var repoShortName: String { repo.split(separator: "/").last.map(String.init) ?? repo }
    var reference: String { issue > 0 ? "#\(issue)" : pr > 0 ? "PR #\(pr)" : "" }
}

extension TaskGroup {
    var title: String {
        switch self {
        case .attention: return "Needs attention"
        case .running: return "Running"
        case .waiting: return "Waiting"
        case .history: return "Finished"
        }
    }
}

extension DashboardSnapshot {
    /// A repository can belong to several projects. Show it once, under the first.
    var uniqueProjects: [ProjectRow] {
        var seen = Set<String>()
        return projects.compactMap { project in
            let repos = project.repos.filter { seen.insert($0.name).inserted }
            return repos.isEmpty ? nil : ProjectRow(name: project.name, repos: repos)
        }
    }
    var activePipelineCount: Int { issuePipelines.filter { $0.group != .history }.count }
}

extension RepoRow {
    /// Why a repository has no rows, for the compact footer.
    var idleNote: String {
        if !enabled { return "Paused" }
        if queueUpdatedAt != nil { return "Nothing queued" }
        return issueQueue == nil ? "Update the controller to view its queue" : "Not evaluated yet"
    }
}
