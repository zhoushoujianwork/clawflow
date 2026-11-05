import AppKit
import SwiftUI

/// Snapshot-only data for the menu-bar panel. It deliberately has no DashboardModel dependency,
/// so all states can be rendered and tested without starting controller or service polling.
struct MenuPanelPresentation {
    static let taskLimit = 4

    struct Tab: Identifiable {
        let id: String
        let title: String
        let fullName: String
        let enabled: Bool
        let running: Int
        let attention: Int
    }
    struct Capacity {
        let running: Int
        let slots: Int
        let online: Int
        let workers: Int
    }
    struct Today {
        let done: Int
        let failed: Int
        var total: Int { done + failed }
        var success: Int? { total > 0 ? Int((Double(done) / Double(total) * 100).rounded()) : nil }
    }
    struct Queue {
        let waiting: Int
        let running: Int
        let reason: String?
    }
    struct TaskItem: Identifiable {
        let row: TaskRow
        let url: URL?
        var id: String { row.id }
    }
    struct TaskSection {
        let items: [TaskItem]
        let total: Int
        var hidden: Int { max(0, total - items.count) }
    }
    struct RepositoryAction {
        let name: String
        let url: URL?
        let monitoring: Bool
    }
    struct Cost {}

    let scopeID: String
    let scopeTitle: String
    let tabs: [Tab]
    let hasSnapshot: Bool
    let connected: Bool
    let freshness: String
    let status: String
    let detail: String?
    let capacity: Capacity?
    let today: Today?
    let queue: Queue?
    let attention: TaskSection
    let running: TaskSection
    let repository: RepositoryAction?
    let cost: Cost?

    static func make(snapshot: DashboardSnapshot?, received: Date?, error: String?, now: Date, selectedScope: String) -> Self {
        let summary = StatusSummary.reduce(snapshot: snapshot, received: received, error: error, now: now)
        guard let snapshot, let received else {
            let unavailable = summary.kind == .unavailable
            return Self(scopeID: "", scopeTitle: "全部", tabs: [], hasSnapshot: false, connected: false,
                        freshness: unavailable ? "Controller 不可用" : "尚未连接",
                        status: "没有数据", detail: firstLine(error), capacity: nil, today: nil, queue: nil,
                        attention: TaskSection(items: [], total: 0), running: TaskSection(items: [], total: 0),
                        repository: nil, cost: nil)
        }

        let repos = uniqueRepositories(snapshot)
        let validScope = repos.contains { $0.name == selectedScope } ? selectedScope : ""
        let scopedTasks = snapshot.board.tasks.filter { validScope.isEmpty || $0.repo == validScope }
        let attentionRows = scopedTasks.filter { $0.group == .attention }
        let runningRows = scopedTasks.filter { $0.group == .running }
        let waitingRows = scopedTasks.filter { $0.group == .waiting }
        let stale = !summary.connected
        let tabs = repos.map { repo in
            let rows = snapshot.board.tasks.filter { $0.repo == repo.name }
            return Tab(id: repo.name, title: repo.shortName, fullName: repo.name, enabled: repo.enabled,
                       running: rows.filter { $0.group == .running }.count,
                       attention: rows.filter { $0.group == .attention }.count)
        }
        let all = Tab(id: "", title: "全部", fullName: "全部项目", enabled: true,
                      running: snapshot.board.tasks.filter { $0.group == .running }.count,
                      attention: snapshot.board.tasks.filter { $0.group == .attention }.count)
        let metrics = snapshot.metrics(repo: validScope)
        let today: Today? = if let done = metrics.todayDone, let failed = metrics.todayFailed {
            Today(done: done, failed: failed)
        } else { nil }
        let selectedRepo = validScope.isEmpty ? nil : repos.first { $0.name == validScope }
        let repository = selectedRepo.map { RepositoryAction(name: $0.name, url: safeWebURL($0.url), monitoring: $0.enabled) }
        let age = relativeAge(received, now: now)
        return Self(
            scopeID: validScope,
            scopeTitle: validScope.isEmpty ? "全部" : (selectedRepo?.shortName ?? validScope),
            tabs: [all] + tabs,
            hasSnapshot: true,
            connected: !stale,
            freshness: stale ? "最后更新 \(age)" : "刚刚更新",
            status: statusText(attention: attentionRows.count, running: runningRows.count, waiting: waitingRows.count, stale: stale),
            detail: stale ? firstLine(error) : nil,
            capacity: capacity(snapshot: snapshot, repo: selectedRepo, scopedTasks: scopedTasks),
            today: today,
            queue: Queue(waiting: waitingRows.count, running: runningRows.count,
                         reason: waitingReason(rows: waitingRows, workers: snapshot.board.workers, repo: selectedRepo)),
            attention: section(attentionRows, snapshot: snapshot),
            running: section(runningRows, snapshot: snapshot),
            repository: repository,
            cost: nil)
    }

    private static func uniqueRepositories(_ snapshot: DashboardSnapshot) -> [RepoRow] {
        var seen = Set<String>()
        return snapshot.projects.flatMap(\.repos)
            .filter { seen.insert($0.name).inserted }
            .sorted { lhs, rhs in lhs.enabled != rhs.enabled ? lhs.enabled : lhs.name.localizedStandardCompare(rhs.name) == .orderedAscending }
    }
    private static func section(_ rows: [TaskRow], snapshot: DashboardSnapshot) -> TaskSection {
        TaskSection(items: rows.prefix(taskLimit).map { TaskItem(row: $0, url: $0.link(in: snapshot)) }, total: rows.count)
    }
    private static func firstLine(_ text: String?) -> String? {
        text?.components(separatedBy: .newlines).first { !$0.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty }
    }
    private static func statusText(attention: Int, running: Int, waiting: Int, stale: Bool) -> String {
        if stale { return "连接中断" }
        if attention > 0 { return "需要关注 \(attention)" }
        if running > 0 { return "运行中 \(running)" }
        if waiting > 0 { return "等待中 \(waiting)" }
        return "空闲"
    }
    private static func capacity(snapshot: DashboardSnapshot, repo: RepoRow?, scopedTasks: [TaskRow]) -> Capacity? {
        guard let repo else {
            return Capacity(running: snapshot.capacity.running, slots: snapshot.capacity.total,
                            online: snapshot.capacity.live, workers: snapshot.board.workers.count)
        }
        let workers = snapshot.board.workers.filter { worker in
            !worker.revoked && repo.workerTags.allSatisfy(worker.tags.contains)
        }
        let running = scopedTasks.filter { $0.group == .running }.count
        return Capacity(running: running, slots: workers.reduce(0) { $0 + max(0, $1.maxParallel) },
                        online: workers.filter(\.live).count, workers: workers.count)
    }
    /// Preserve the old popover's useful explanation order: explicit reason, no eligible worker,
    /// full capacity, then a tag-matching fallback.
    private static func waitingReason(rows: [TaskRow], workers: [WorkerRow], repo: RepoRow?) -> String? {
        guard !rows.isEmpty else { return nil }
        if let reason = rows.compactMap(\.waitingReason).first(where: { !$0.isEmpty }) { return reason }
        let eligible = workers.filter { worker in
            worker.live && !worker.paused && !worker.revoked && (repo?.workerTags.allSatisfy(worker.tags.contains) ?? true)
        }
        if eligible.isEmpty { return "没有可接单的 Worker" }
        if eligible.allSatisfy({ $0.runningTasks.count >= $0.maxParallel }) { return "Worker 容量已满" }
        return "等待标签匹配的 Worker"
    }
}

struct MenuPanelActions {
    var selectScope: (String) -> Void = { _ in }
    var openURL: (URL) -> Void = { _ in }
    var openDashboard: () -> Void = {}
    var toggleMonitoring: (String, Bool) -> Void = { _, _ in }
    var checkOffload: () -> Void = {}
    var openSettings: () -> Void = {}
    var quit: () -> Void = {}
}

struct MenuPanelView: View {
    let presentation: MenuPanelPresentation
    var offloadWarning: String?
    var pendingText: String?
    var actionError: String?
    var actions: MenuPanelActions
    var localServiceControls: AnyView
    var maximumHeight: CGFloat?
    var snapshotRendering: Bool
    @State private var servicesExpanded = false

    init(presentation: MenuPanelPresentation, offloadWarning: String? = nil, pendingText: String? = nil,
         actionError: String? = nil, actions: MenuPanelActions = .init(),
         localServiceControls: AnyView = AnyView(EmptyView()), maximumHeight: CGFloat? = nil,
         snapshotRendering: Bool = false) {
        self.presentation = presentation
        self.offloadWarning = offloadWarning
        self.pendingText = pendingText
        self.actionError = actionError
        self.actions = actions
        self.localServiceControls = localServiceControls
        self.maximumHeight = maximumHeight
        self.snapshotRendering = snapshotRendering
    }

    @ViewBuilder
    var body: some View {
        if snapshotRendering {
            panelContent.frame(width: 340).foregroundStyle(DS.fg).background(DS.panel)
        } else {
            ScrollView(.vertical) { panelContent }
                .scrollIndicators(.hidden)
                .frame(width: 340)
                .frame(maxHeight: maximumHeight)
                .foregroundStyle(DS.fg)
                .background(DS.panel)
        }
    }

    private var panelContent: some View {
        VStack(alignment: .leading, spacing: 0) {
            scopeTabs
            header
            if let warning = offloadWarning { warningRow(warning) }
            if presentation.hasSnapshot {
                if let capacity = presentation.capacity { capacitySection(capacity) }
                if let today = presentation.today { todaySection(today) }
                if let queue = presentation.queue { queueSection(queue) }
                taskSection("需要关注", symbol: "exclamationmark.triangle", section: presentation.attention, tone: DS.warn)
                taskSection("运行中", symbol: "arrow.triangle.2.circlepath", section: presentation.running, tone: DS.tab)
            } else {
                Text("连接 Controller 后显示项目、任务和 Worker 状态。")
                    .font(DS.sub).foregroundStyle(DS.dim).fixedSize(horizontal: false, vertical: true).padding(.vertical, 18)
            }
            if let pendingText { Text(pendingText).font(DS.small).foregroundStyle(DS.dim).padding(.vertical, 6) }
            if let actionError { Text(actionError).font(DS.small).foregroundStyle(DS.warn).fixedSize(horizontal: false, vertical: true).padding(.vertical, 6) }
            actionRows
        }
        .padding(14)
        .frame(maxWidth: .infinity, alignment: .leading)
    }

    @ViewBuilder private var scopeTabs: some View {
        if snapshotRendering {
            scopeTabItems.padding(.horizontal, 2).padding(.bottom, 14)
        } else {
            ScrollView(.horizontal) { scopeTabItems.padding(.horizontal, 2).padding(.bottom, 4) }
                .scrollIndicators(.hidden)
                .fixedSize(horizontal: false, vertical: true)
                .padding(.horizontal, -2)
                .padding(.bottom, 10)
        }
    }

    private var scopeTabItems: some View {
        HStack(spacing: 5) {
            ForEach(presentation.tabs) { tab in
                MenuScopeTab(tab: tab, selected: tab.id == presentation.scopeID) { actions.selectScope(tab.id) }
            }
        }
    }

    private var header: some View {
        VStack(alignment: .leading, spacing: 5) {
            Text(presentation.scopeTitle).font(.system(size: 20, weight: .semibold))
            HStack { Text(presentation.freshness); Spacer(); Text(presentation.status) }
                .font(DS.sub).foregroundStyle(presentation.connected ? DS.dim : DS.warn)
            if let detail = presentation.detail { Text(detail).font(DS.small).foregroundStyle(DS.warn).lineLimit(2) }
        }
        .padding(.bottom, 14)
        .overlay(alignment: .bottom) { DS.line.frame(height: 1) }
        .accessibilityElement(children: .combine)
    }

    private func warningRow(_ warning: String) -> some View {
        HStack(alignment: .firstTextBaseline, spacing: 7) {
            Image(systemName: "exclamationmark.triangle.fill").foregroundStyle(DS.warn)
            Text(warning).font(DS.small).fixedSize(horizontal: false, vertical: true)
            Spacer(minLength: 4)
            Button("检查", action: actions.checkOffload).buttonStyle(.plain).foregroundStyle(DS.tab)
        }.padding(.vertical, 11)
    }

    private func capacitySection(_ value: MenuPanelPresentation.Capacity) -> some View {
        MenuProgressSection(title: "Worker 容量", left: "\(value.running) / \(value.slots) 在跑",
                            right: "\(value.online) 个 Worker 在线",
                            segments: [(value.running, DS.bar)], total: value.slots)
    }
    private func todaySection(_ value: MenuPanelPresentation.Today) -> some View {
        MenuProgressSection(title: "今日任务", left: "完成 \(value.done) · 失败 \(value.failed)",
                            right: value.success.map { "成功率 \($0)%" } ?? "暂无结果",
                            segments: [(value.done, DS.bar), (value.failed, DS.bad)], total: value.total)
    }
    private func queueSection(_ value: MenuPanelPresentation.Queue) -> some View {
        MenuProgressSection(title: "队列", left: "等待 \(value.waiting) · 运行 \(value.running)",
                            right: value.reason ?? "无等待原因",
                            segments: [(value.running, DS.tab), (value.waiting, DS.barSoft)],
                            total: value.waiting + value.running)
    }

    @ViewBuilder private func taskSection(_ title: String, symbol: String, section: MenuPanelPresentation.TaskSection, tone: Color) -> some View {
        if section.total > 0 {
            VStack(alignment: .leading, spacing: 4) {
                Label(title, systemImage: symbol).font(.system(size: 13, weight: .semibold)).foregroundStyle(tone).padding(.bottom, 3)
                ForEach(section.items) { item in
                    MenuTaskRow(item: item, connected: presentation.connected) {
                        if let url = item.url { actions.openURL(url) }
                    }
                }
                if section.hidden > 0 { Text("还有 \(section.hidden) 个").font(DS.small).foregroundStyle(DS.dim).padding(.top, 2) }
            }.menuGroup()
        }
    }

    private var actionRows: some View {
        VStack(spacing: 0) {
            MenuActionRow(symbol: "rectangle.grid.2x2", title: "打开看板", shortcut: "⌘D", action: actions.openDashboard)
                .keyboardShortcut("d")
            if let repo = presentation.repository {
                MenuActionRow(symbol: repo.monitoring ? "pause.circle" : "play.circle",
                              title: repo.monitoring ? "暂停监控" : "恢复监控", shortcut: nil) {
                    actions.toggleMonitoring(repo.name, repo.monitoring)
                }
                .disabled(!presentation.connected)
                .help("只影响这个仓库的新 Issue 监控；运行中和排队的任务会继续。")
                if let url = repo.url {
                    MenuActionRow(symbol: "safari", title: "在 GitHub 打开", shortcut: nil) { actions.openURL(url) }
                }
            }
            MenuActionRow(symbol: "desktopcomputer", title: "本机服务", shortcut: nil) { servicesExpanded.toggle() }
            if servicesExpanded { localServiceControls.padding(.leading, 24).padding(.vertical, 8) }
            DS.line.frame(height: 1).padding(.vertical, 7)
            MenuActionRow(symbol: "gearshape", title: "设置…", shortcut: "⌘,") { actions.openSettings() }
                .keyboardShortcut(",")
            MenuActionRow(symbol: "power", title: "退出", shortcut: "⌘Q") { actions.quit() }
                .keyboardShortcut("q")
                .help("退出 App 不会停止 Controller 或 Worker。")
        }
        .padding(.top, 7)
    }
}

private struct MenuScopeTab: View {
    let tab: MenuPanelPresentation.Tab
    let selected: Bool
    let action: () -> Void
    @State private var hovering = false
    var body: some View {
        Button(action: action) {
            VStack(spacing: 5) {
                HStack(spacing: 4) {
                    Image(systemName: tab.id.isEmpty ? "square.grid.2x2" : "folder")
                        .font(.system(size: 10))
                    Text(tab.title).font(.system(size: 11.5, weight: .medium)).lineLimit(1)
                }
                GeometryReader { proxy in
                    let total = max(1, tab.running + tab.attention)
                    HStack(spacing: 1) {
                        if tab.running > 0 { TrackSegment(color: DS.tab).frame(width: proxy.size.width * CGFloat(tab.running) / CGFloat(total)) }
                        if tab.attention > 0 { TrackSegment(color: DS.warn).frame(width: proxy.size.width * CGFloat(tab.attention) / CGFloat(total)) }
                        if tab.running + tab.attention == 0 { TrackSegment(color: DS.track) }
                    }
                }.frame(height: 4)
            }
            .padding(.horizontal, 7).padding(.vertical, 7)
            .frame(minWidth: 50, maxWidth: 76)
            .background(selected ? DS.tab : hovering ? DS.raise : Color.clear, in: RoundedRectangle(cornerRadius: 8))
            .foregroundStyle(selected ? Color.white : tab.enabled ? DS.fg : DS.dim)
            .contentShape(RoundedRectangle(cornerRadius: 8))
        }
        .buttonStyle(.plain).onHover { hovering = $0 }
        .help(tab.fullName + (tab.enabled ? "" : " · 已暂停监控"))
        .accessibilityLabel(tab.fullName).accessibilityAddTraits(selected ? .isSelected : [])
    }
}

private struct MenuProgressSection: View {
    let title: String
    let left: String
    let right: String
    let segments: [(Int, Color)]
    let total: Int
    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            Text(title).font(.system(size: 13, weight: .semibold))
            GeometryReader { proxy in
                HStack(spacing: 1) {
                    if total > 0 {
                        ForEach(Array(segments.enumerated()), id: \.offset) { _, segment in
                            if segment.0 > 0 { TrackSegment(color: segment.1).frame(width: proxy.size.width * CGFloat(segment.0) / CGFloat(total)) }
                        }
                        let remainder = max(0, total - segments.reduce(0) { $0 + $1.0 })
                        if remainder > 0 { TrackSegment(color: DS.track) }
                    } else { TrackSegment(color: DS.track) }
                }
            }.frame(height: 6)
            HStack(alignment: .firstTextBaseline, spacing: 8) {
                Text(left).foregroundStyle(DS.fg)
                Spacer(minLength: 4)
                Text(right).foregroundStyle(DS.dim).lineLimit(1).minimumScaleFactor(0.75)
            }.font(DS.small).monospacedDigit()
        }.menuGroup()
    }
}

private struct MenuTaskRow: View {
    let item: MenuPanelPresentation.TaskItem
    let connected: Bool
    let action: () -> Void
    @State private var hovering = false
    var body: some View {
        Button(action: action) {
            VStack(alignment: .leading, spacing: 3) {
                Text("\(item.row.reference) \(item.row.displayTitle)").lineLimit(1)
                HStack(spacing: 5) {
                    Text(item.row.kindZh + " · " + item.row.statusZh)
                    Spacer()
                    TaskElapsedText(row: item.row, connected: connected, frozenAt: item.row.updatedAt)
                }.font(DS.small).foregroundStyle(DS.dim).monospacedDigit()
                if item.row.group == .attention, let reason = item.row.meaningfulReasonZh {
                    Text(reason).font(DS.small).foregroundStyle(DS.warn).lineLimit(2)
                }
            }
            .padding(.horizontal, 7).padding(.vertical, 6)
            .frame(maxWidth: .infinity, alignment: .leading)
            .background(hovering ? DS.tab.opacity(0.14) : Color.clear, in: RoundedRectangle(cornerRadius: 7))
            .contentShape(RoundedRectangle(cornerRadius: 7))
        }
        .buttonStyle(.plain).onHover { hovering = $0 }
        .disabled(item.url == nil)
        .accessibilityLabel("\(item.row.reference) \(item.row.displayTitle)，\(item.row.statusZh)")
    }
}

private struct MenuActionRow: View {
    let symbol: String
    let title: String
    let shortcut: String?
    let action: () -> Void
    @State private var hovering = false
    var body: some View {
        Button(action: action) {
            HStack(spacing: 10) {
                Image(systemName: symbol).frame(width: 17)
                Text(title)
                Spacer()
                if let shortcut { Text(shortcut).foregroundStyle(DS.dim) }
            }
            .font(.system(size: 13))
            .padding(.horizontal, 8).padding(.vertical, 7)
            .frame(maxWidth: .infinity, alignment: .leading)
            .background(hovering ? DS.tab.opacity(0.14) : Color.clear, in: RoundedRectangle(cornerRadius: 7))
            .contentShape(RoundedRectangle(cornerRadius: 7))
        }
        .buttonStyle(.plain).onHover { hovering = $0 }
        .accessibilityLabel(title)
    }
}

private extension View {
    func menuGroup() -> some View {
        padding(.vertical, 14).overlay(alignment: .bottom) { DS.line.frame(height: 1) }
    }
}

