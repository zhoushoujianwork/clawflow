import Foundation

// Chinese presentation for the dashboard window. The menu-bar popover keeps its own English strings.

/// How a queued issue reads in the dashboard. Raw scheduler states never reach the interface.
extension IssueQueueRow {
    /// The scheduler reason code after the first colon, without any coordination sentence.
    var reasonCode: String? {
        let parts = state.split(separator: ":", maxSplits: 1).map(String.init)
        guard parts.count == 2 else { return nil }
        let code = parts[1].range(of: " — ").map { String(parts[1][..<$0.lowerBound]) } ?? parts[1]
        // Some codes carry an argument, such as "area_busy:PR #622".
        return code.split(separator: ":", maxSplits: 1).first.map(String.init)
    }

    /// One short reason, or nil when the status already says everything or the code is unknown.
    var detail: String? {
        let parts = state.split(separator: ":", maxSplits: 1).map(String.init)
        guard parts.count == 2 else { return nil }
        // Coordination reasons carry the controller's own sentence after the code; it stays as received.
        if let range = parts[1].range(of: " — ") { return String(parts[1][range.upperBound...]) }
        let argument = parts[1].split(separator: ":", maxSplits: 1).dropFirst().first.map(String.init)
        switch reasonCode {
        case "no_verdict": return "PR 已开，等待验收"
        case "acceptance_stale": return "PR 已变更，验收已过期"
        case "dependency": return "等待依赖"
        case "max_in_flight": return "仓库名额已满"
        case "area_busy": return argument.map { "和 \($0) 改动范围重叠" } ?? "和打开的 PR 改动范围重叠"
        case "author_not_admitted": return argument.map { "作者 \($0) 没有写权限，未启动" } ?? "作者没有写权限，未启动"
        case "author_check_failed": return "作者权限查询失败，本轮未启动"
        default: return nil
        }
    }

    var statusZh: String {
        if state.hasPrefix("waiting:needs-confirmation") || state.hasPrefix("waiting:coordination_needs_confirmation") { return "需要确认" }
        if state.hasPrefix("closed:superseded") { return "已被取代" }
        if state.hasPrefix("closed:already-delivered") { return "已交付" }
        if state.hasPrefix("eligible:coordination checked") { return "协调已检查" }
        switch state.split(separator: ":").first.map(String.init) {
        case "in_flight": return "进行中"
        case "eligible": return "就绪，等待名额"
        case "blocked": return "已阻塞"
        case "parked": return "已搁置"
        case "waiting": return "等待"
        default: return "状态未知"
        }
    }
}

/// Relative age in Chinese, measured against the snapshot time.
func relativeAge(_ date: Date?, now: Date) -> String {
    guard let date else { return "—" }
    let minutes = Int(now.timeIntervalSince(date)) / 60
    if minutes < 1 { return "刚刚" }
    if minutes < 60 { return "\(minutes) 分钟前" }
    if minutes < 2880 { return "\(minutes / 60) 小时前" }
    return "\(minutes / 1440) 天前"
}

/// Duration such as "2 小时 5 分", "3 分 12 秒" or "40 秒".
func durationText(_ interval: TimeInterval) -> String {
    let s = max(0, Int(interval))
    let h = s / 3600, m = (s / 60) % 60
    if h > 0 { return "\(h) 小时 \(m) 分" }
    if m > 0 { return "\(m) 分 \(s % 60) 秒" }
    return "\(s) 秒"
}

/// Labels the controller's throughput window from its `since` field instead of assuming a fixed window.
func throughputLabel(_ value: Throughput, generatedAt: Date) -> String {
    let counts = "完成 \(value.done)、失败 \(value.failed)"
    let window = generatedAt.timeIntervalSince(value.since)
    let hours = (window / 3600).rounded()
    if hours >= 1, abs(window - hours * 3600) < 60 { return "近 \(Int(hours)) 小时\(counts)" }
    let format = DateFormatter()
    format.locale = Locale(identifier: "zh_CN")
    format.dateFormat = "HH:mm"
    return "自 \(format.string(from: value.since)) 起\(counts)"
}

extension TaskRow {
    var displayTitle: String { subject?.isEmpty == false ? subject! : kindZh }
    var repoShortName: String { repo.split(separator: "/").last.map(String.init) ?? repo }
    var reference: String { issue > 0 ? "#\(issue)" : pr > 0 ? "PR #\(pr)" : "" }
    var kindZh: String {
        switch kind { case "implement": return "实现"; case "repair": return "修复"; case "acceptance": return "验收"; case "reply": return "回复"; default: return "未知类型" }
    }
    var statusZh: String {
        switch stage {
        case "lease_expired": return "连接丢失"
        case "recovery_pending": return "需要恢复"
        case "running": return "运行中"
        case "finalizing": return "正在确认结果"
        case "pending": return "等待"
        case "done": return "已结束"
        case "failed": return "失败"
        case "cancelled": return "已取消"
        default: return "状态未知"
        }
    }
    /// Controller-provided text stays as received; everything else is Chinese.
    var reasonZh: String {
        if let error = applicationError, !error.isEmpty { return error }
        if let reason = waitingReason, !reason.isEmpty { return reason }
        if stage == "lease_expired" { return "Worker 租约已过期，由 controller 负责收尾，不要重复执行。" }
        if state == "done" { return "这次执行已结束。CI、验收和合并是单独的交付关卡。" }
        if state == "pending" { return "等待可用的 Worker 和名额。" }
        return statusZh
    }
    /// Explains more than the status does; nil when the reason would only repeat it.
    var meaningfulReasonZh: String? { reasonZh == statusZh ? nil : reasonZh }
    func durationZh(at now: Date) -> String {
        guard durationAvailable, let startedAt else { return "—" }
        return durationText((endedAt ?? now).timeIntervalSince(startedAt))
    }
}

extension WorkerRow {
    var stateZh: String { revoked ? "已移除" : !live ? "离线" : paused ? "已暂停派发" : "就绪" }
}

extension PendingAction {
    var textZh: String {
        switch action {
        case "disable": return "正在暂停监控 \(name)…"
        case "enable": return "正在恢复监控 \(name)…"
        case "drain": return "正在停止向 \(name) 派发…"
        case "resume": return "正在恢复向 \(name) 派发…"
        case "remove": return "正在移除 \(name)…"
        default: return "正在处理 \(name)…"
        }
    }
}

extension StatusSummary {
    /// Connection text for the window; never claims a stale snapshot is connected.
    var freshnessZh: String {
        switch kind {
        case .unknown: return "未连接"
        case .unavailable: return "Controller 不可用"
        case .stale: return "连接中断 · 显示最后已知数据"
        default: return "已连接"
        }
    }
}

extension Finding {
    /// Rule-based Chinese text such as "恢复挂起 3 小时"; unknown rules fall back to the controller's reason.
    var text: String {
        guard let ageSec else { return reason }
        let minutes = ageSec / 60
        let age = minutes >= 120 ? "\(minutes / 60) 小时" : "\(minutes) 分钟"
        switch rule {
        case "recovery_stalled": return "恢复挂起 \(age)"
        case "finalizing_stalled": return "结果确认停滞 \(age)"
        case "result_stuck": return "结果写回失败 \(age)"
        case "dispatch_stalled": return "等待派发 \(age)"
        case "no_progress": return "\(age)无进展"
        default: return reason
        }
    }
    /// Watchdog threshold for the rule, in minutes.
    var thresholdMinutes: Int {
        switch rule { case "finalizing_stalled", "result_stuck", "dispatch_stalled": return 10; default: return 30 }
    }
    /// Share of the elapsed time that was within the threshold, 0...1. Nil without an age.
    var withinThresholdShare: Double? {
        guard let ageSec, ageSec > 0 else { return nil }
        return min(Double(thresholdMinutes * 60) / Double(ageSec), 1)
    }
}

extension TaskGroup {
    var title: String {
        switch self { case .attention: return "需要处理"; case .running: return "运行中"; case .waiting: return "等待"; case .history: return "已结束" }
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
    /// Repositories offered as filter chips: enabled ones and any with an active pipeline.
    var filterRepos: [RepoRow] {
        let active = Set(issuePipelines.filter { $0.group != .history }.map(\.repo))
        return uniqueProjects.flatMap(\.repos).filter { $0.enabled || active.contains($0.name) }
    }
}

extension RepoRow {
    var shortName: String { name.split(separator: "/").last.map(String.init) ?? name }
    /// Why a repository has no rows.
    var idleNote: String {
        if !enabled { return "已暂停" }
        if queueUpdatedAt != nil { return "没有排队" }
        return issueQueue == nil ? "需要更新 controller 才能查看队列" : "未评估"
    }
}
