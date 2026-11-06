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
        let rightText: String
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
        var summary: String { "运行 \(running) · 等待 \(waiting)" }
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
    struct Cost {
        struct RecentDay: Identifiable {
            let id: String
            let title: String
            let date: String
            let costUSD: Double?
            let tokens: Int64?

            var summary: String {
                guard costUSD != nil || tokens != nil else { return "—" }
                return "\(costUSD.map(menuCurrency) ?? "—") · \(tokens.map(menuTokens) ?? "—")"
            }
        }

        let todayUSD: Double?
        let todayTokens: Int64?
        let sevenDaysUSD: Double?
        let sevenDaysTokens: Int64?
        let thirtyDaysUSD: Double
        let thirtyDaysTokens: Int64?
        let daily: [DashboardUsageDay]
        let recent: [RecentDay]
        let mostUsedModel: String?
        let estimated: Bool
        var footer: String { estimated ? "根据 token 用量估算 · 不是账单" : "根据运行记录汇总 · 不是账单" }
    }

    let scopeID: String
    let scopeTitle: String
    let tabs: [Tab]
    let hasSnapshot: Bool
    let connected: Bool
    /// Last controller-recorded point used to freeze elapsed labels when disconnected.
    let frozenAt: Date
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
    var dimsLiveData: Bool { hasSnapshot && !connected }

    static func make(snapshot: DashboardSnapshot?, received: Date?, error: String?, now: Date, selectedScope: String) -> Self {
        let summary = StatusSummary.reduce(snapshot: snapshot, received: received, error: error, now: now)
        guard let snapshot, let received else {
            let unavailable = summary.kind == .unavailable
            return Self(scopeID: "", scopeTitle: "概览", tabs: [], hasSnapshot: false, connected: false, frozenAt: now,
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
        let all = Tab(id: "", title: "概览", fullName: "项目概览", enabled: true,
                      running: snapshot.board.tasks.filter { $0.group == .running }.count,
                      attention: snapshot.board.tasks.filter { $0.group == .attention }.count)
        let metrics = snapshot.metrics(repo: validScope)
        let today: Today? = if let done = metrics.todayDone, let failed = metrics.todayFailed {
            Today(done: done, failed: failed)
        } else { nil }
        let selectedRepo = validScope.isEmpty ? nil : repos.first { $0.name == validScope }
        let repository = selectedRepo.map { RepositoryAction(name: $0.name, url: safeWebURL($0.url), monitoring: $0.enabled) }
        let usageScope = snapshot.usage?.scope(repo: validScope)
        let cost = usageScope.flatMap { usage -> Cost? in
            guard let thirtyDaysUSD = usage.thirtyDays.costUsd else { return nil }
            return Cost(todayUSD: usage.today.costUsd, todayTokens: usage.today.totalTokens,
                        sevenDaysUSD: usage.sevenDays?.costUsd, sevenDaysTokens: usage.sevenDays?.totalTokens,
                        thirtyDaysUSD: thirtyDaysUSD, thirtyDaysTokens: usage.thirtyDays.totalTokens,
                        daily: usage.daily, recent: recentDays(usage.daily), mostUsedModel: usage.mostUsedModel,
                        estimated: usage.thirtyDays.costIsEstimate)
        }
        let age = relativeAge(received, now: now)
        return Self(
            scopeID: validScope,
            scopeTitle: validScope.isEmpty ? "概览" : (selectedRepo?.shortName ?? validScope),
            tabs: [all] + tabs,
            hasSnapshot: true,
            connected: !stale,
            frozenAt: snapshot.board.generatedAt,
            freshness: stale ? "最后更新 \(age)" : "刚刚更新",
            status: stale ? "连接中断" : selectedRepo?.enabled == false ? "已暂停监控" :
                statusText(attention: attentionRows.count, running: runningRows.count, waiting: waitingRows.count),
            detail: stale ? firstLine(error) : nil,
            capacity: capacity(snapshot: snapshot, repo: selectedRepo, scopedTasks: scopedTasks, stale: stale),
            today: today,
            queue: Queue(waiting: waitingRows.count, running: runningRows.count,
                         reason: waitingReason(rows: waitingRows, workers: snapshot.board.workers, repo: selectedRepo)),
            attention: section(attentionRows, snapshot: snapshot,
                               limit: validScope.isEmpty ? 3 : taskLimit),
            running: section(runningRows, snapshot: snapshot,
                             limit: validScope.isEmpty ? max(0, 3 - min(3, attentionRows.count)) : taskLimit),
            repository: repository,
            cost: cost)
    }

    private static func uniqueRepositories(_ snapshot: DashboardSnapshot) -> [RepoRow] {
        var seen = Set<String>()
        return snapshot.projects.flatMap(\.repos)
            .filter { seen.insert($0.name).inserted }
            .sorted { lhs, rhs in lhs.enabled != rhs.enabled ? lhs.enabled : lhs.name.localizedStandardCompare(rhs.name) == .orderedAscending }
    }
    private static func section(_ rows: [TaskRow], snapshot: DashboardSnapshot, limit: Int) -> TaskSection {
        TaskSection(items: rows.prefix(limit).map { TaskItem(row: $0, url: $0.link(in: snapshot)) }, total: rows.count)
    }
    private static func recentDays(_ days: [DashboardUsageDay]) -> [Cost.RecentDay] {
        guard let latest = days.map(\.date).max() else { return [] }
        let byDate = Dictionary(uniqueKeysWithValues: days.map { ($0.date, $0) })
        let titles = ["今天", "昨天", "2 天前", "3 天前"]
        return titles.enumerated().compactMap { offset, title in
            guard let key = shiftedDay(latest, by: -offset) else { return nil }
            let day = byDate[key]
            return Cost.RecentDay(id: key, title: title, date: chineseDay(key),
                                  costUSD: day?.costUsd, tokens: day?.totalTokens)
        }
    }
    private static func shiftedDay(_ value: String, by offset: Int) -> String? {
        let parts = value.split(separator: "-").compactMap { Int($0) }
        guard parts.count == 3 else { return nil }
        var calendar = Calendar(identifier: .gregorian)
        calendar.timeZone = TimeZone(secondsFromGMT: 0)!
        guard let date = calendar.date(from: DateComponents(year: parts[0], month: parts[1], day: parts[2])),
              let shifted = calendar.date(byAdding: .day, value: offset, to: date) else { return nil }
        let result = calendar.dateComponents([.year, .month, .day], from: shifted)
        guard let year = result.year, let month = result.month, let day = result.day else { return nil }
        return String(format: "%04d-%02d-%02d", year, month, day)
    }
    private static func chineseDay(_ value: String) -> String {
        let parts = value.split(separator: "-").compactMap { Int($0) }
        guard parts.count == 3 else { return value }
        return "\(parts[1])月\(parts[2])日"
    }
    private static func firstLine(_ text: String?) -> String? {
        text?.components(separatedBy: .newlines).first { !$0.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty }
    }
    private static func statusText(attention: Int, running: Int, waiting: Int) -> String {
        if attention > 0 { return "需要关注 \(attention)" }
        if running > 0 { return "运行中 \(running)" }
        if waiting > 0 { return "等待中 \(waiting)" }
        return "空闲"
    }
    private static func capacity(snapshot: DashboardSnapshot, repo: RepoRow?, scopedTasks: [TaskRow], stale: Bool) -> Capacity? {
        guard let repo else {
            return Capacity(running: snapshot.capacity.running, slots: snapshot.capacity.total,
                            online: snapshot.capacity.live, workers: snapshot.board.workers.count,
                            rightText: stale ? "最后已知" : "\(snapshot.capacity.live) 个 Worker 在线")
        }
        let workers = snapshot.board.workers.filter { worker in
            !worker.revoked && repo.workerTags.allSatisfy(worker.tags.contains)
        }
        let running = scopedTasks.filter { $0.group == .running }.count
        return Capacity(running: running, slots: workers.reduce(0) { $0 + max(0, $1.maxParallel) },
                        online: workers.filter(\.live).count, workers: workers.count,
                        rightText: stale ? "最后已知" : "\(workers.filter(\.live).count) 个 Worker 在线")
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
    @State private var panelBottom: CGFloat = 0
    @State private var viewportHeight: CGFloat = 0

    init(presentation: MenuPanelPresentation, offloadWarning: String? = nil, pendingText: String? = nil,
         actionError: String? = nil, actions: MenuPanelActions = .init(),
         localServiceControls: AnyView = AnyView(EmptyView()), maximumHeight: CGFloat? = nil,
         snapshotRendering: Bool = false, servicesInitiallyExpanded: Bool = false) {
        self.presentation = presentation
        self.offloadWarning = offloadWarning
        self.pendingText = pendingText
        self.actionError = actionError
        self.actions = actions
        self.localServiceControls = localServiceControls
        self.maximumHeight = maximumHeight
        self.snapshotRendering = snapshotRendering
        _servicesExpanded = State(initialValue: servicesInitiallyExpanded)
    }

    @ViewBuilder
    var body: some View {
        if snapshotRendering {
            panelContent.frame(width: 340).foregroundStyle(DS.fg).background(DS.panel)
        } else {
            ScrollView(.vertical) {
                panelContent.background {
                    GeometryReader { proxy in
                        Color.clear.preference(key: MenuPanelBottomKey.self,
                                               value: proxy.frame(in: .named("menu-panel-scroll")).maxY)
                    }
                }
            }
                .coordinateSpace(name: "menu-panel-scroll")
                .scrollIndicators(.hidden)
                .frame(width: 340)
                .frame(maxHeight: maximumHeight)
                .foregroundStyle(DS.fg)
                .background(DS.panel)
                .background {
                    GeometryReader { proxy in
                        Color.clear.preference(key: MenuPanelViewportKey.self, value: proxy.size.height)
                    }
                }
                .onPreferenceChange(MenuPanelBottomKey.self) { panelBottom = $0 }
                .onPreferenceChange(MenuPanelViewportKey.self) { viewportHeight = $0 }
                .overlay(alignment: .bottom) {
                    if panelBottom > viewportHeight + 6 {
                        LinearGradient(colors: [DS.panel.opacity(0), DS.panel], startPoint: .top, endPoint: .bottom)
                            .frame(height: 30)
                            .overlay(alignment: .bottom) {
                                Image(systemName: "chevron.down").font(.system(size: 11, weight: .semibold))
                                    .foregroundStyle(DS.dim).padding(.bottom, 4)
                            }
                            .allowsHitTesting(false)
                    }
                }
        }
    }

    private var panelContent: some View {
        VStack(alignment: .leading, spacing: 0) {
            scopeTabs
            header
            if let warning = offloadWarning { warningRow(warning) }
            if presentation.hasSnapshot {
                progressBlock
                if let cost = presentation.cost { MenuUsageSection(value: cost, dimmed: presentation.dimsLiveData) }
                taskSection("需要关注", symbol: "exclamationmark.triangle", section: presentation.attention, tone: DS.warn)
                taskSection("运行中", symbol: "arrow.triangle.2.circlepath", section: presentation.running, tone: DS.bar)
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
            snapshotScopeTabItems.padding(.horizontal, 2).padding(.bottom, 4)
        } else {
            ScrollViewReader { proxy in
                ScrollView(.horizontal) { scopeTabItems.padding(.horizontal, 2).padding(.bottom, 4) }
                    .scrollIndicators(.hidden)
                    .frame(height: 46)
                    .padding(.horizontal, -2)
                    .padding(.bottom, 4)
                    .onAppear { proxy.scrollTo(presentation.scopeID, anchor: .center) }
                    .onChange(of: presentation.scopeID) { _, scope in proxy.scrollTo(scope, anchor: .center) }
            }
        }
    }

    private var scopeTabItems: some View {
        HStack(spacing: 5) {
            ForEach(presentation.tabs) { tab in
                scopeTab(tab)
            }
        }
    }

    private var snapshotScopeTabItems: some View {
        let visible = presentation.tabs.count > 5 ? Array(presentation.tabs.suffix(4)) : presentation.tabs
        return HStack(spacing: 5) {
            if presentation.tabs.count > 5 {
                Text("‹ \(presentation.tabs.count - visible.count) 个")
                    .font(DS.tiny).foregroundStyle(DS.dim).frame(width: 43)
                    .accessibilityLabel("前面还有 \(presentation.tabs.count - visible.count) 个项目")
            }
            ForEach(visible) { tab in scopeTab(tab) }
        }
    }

    private func scopeTab(_ tab: MenuPanelPresentation.Tab) -> some View {
        MenuScopeTab(tab: tab, selected: tab.id == presentation.scopeID,
                     dimmed: presentation.dimsLiveData) { actions.selectScope(tab.id) }
            .id(tab.id)
    }

    private var header: some View {
        VStack(alignment: .leading, spacing: 4) {
            Button(action: actions.openDashboard) {
                HStack(alignment: .firstTextBaseline, spacing: 8) {
                    Text(presentation.scopeTitle).font(.system(size: 15, weight: .semibold)).foregroundStyle(DS.fg)
                    Spacer(minLength: 6)
                    Text(presentation.freshness)
                    Image(systemName: "chevron.right").font(.system(size: 10, weight: .semibold))
                }
                .font(DS.sub)
                .foregroundStyle(presentation.connected ? DS.dim : DS.warn)
                .contentShape(Rectangle())
            }
            .buttonStyle(.plain)
            HStack {
                Text(presentation.status)
                Spacer(minLength: 6)
                if let capacity = presentation.capacity { Text(capacity.rightText) }
            }
            .font(DS.sub).foregroundStyle(presentation.connected ? DS.dim : DS.warn)
            if let detail = presentation.detail { Text(detail).font(DS.small).foregroundStyle(DS.warn).lineLimit(2) }
        }
        .padding(.top, 4).padding(.bottom, 10)
        .overlay(alignment: .bottom) { DS.line.frame(height: 1) }
    }

    private func warningRow(_ warning: String) -> some View {
        HStack(alignment: .firstTextBaseline, spacing: 7) {
            Image(systemName: "exclamationmark.triangle.fill").foregroundStyle(DS.warn)
            Text(warning).font(DS.small).fixedSize(horizontal: false, vertical: true)
            Spacer(minLength: 4)
            Button("检查", action: actions.checkOffload).buttonStyle(.plain).foregroundStyle(DS.fg)
        }.padding(.vertical, 11)
    }

    private var progressBlock: some View {
        VStack(alignment: .leading, spacing: 9) {
            if let value = presentation.today {
                MenuCompactProgress(left: "今日  完成 \(value.done) · 失败 \(value.failed)",
                                    right: value.success.map { "成功率 \($0)%" } ?? "暂无结果",
                                    explanation: nil,
                                    segments: [(value.done, DS.bar), (value.failed, DS.bad)], total: value.total,
                                    dimmed: presentation.dimsLiveData)
            }
            if let value = presentation.capacity {
                MenuCompactProgress(left: "容量  \(value.running) / \(value.slots) 在跑",
                                    right: "剩余 \(max(0, value.slots - value.running))",
                                    explanation: nil,
                                    segments: [(value.running, DS.bar)], total: value.slots,
                                    dimmed: presentation.dimsLiveData)
            }
            if let value = presentation.queue {
                MenuCompactProgress(left: "队列  \(value.summary)", right: "",
                                    explanation: value.reason,
                                    segments: [(value.running, DS.bar), (value.waiting, DS.barSoft)],
                                    total: value.waiting + value.running, dimmed: presentation.dimsLiveData)
            }
        }
        .padding(.vertical, 10)
        .overlay(alignment: .bottom) { DS.line.frame(height: 1) }
    }

    @ViewBuilder private func taskSection(_ title: String, symbol: String, section: MenuPanelPresentation.TaskSection, tone: Color) -> some View {
        if section.total > 0 {
            VStack(alignment: .leading, spacing: 4) {
                HStack(spacing: 8) {
                    Image(systemName: symbol).frame(width: 18)
                    Text(title)
                }.font(.system(size: 13, weight: .semibold)).foregroundStyle(tone)
                ForEach(section.items) { item in
                    MenuTaskRow(item: item, connected: presentation.connected, frozenAt: presentation.frozenAt,
                                dimmed: presentation.dimsLiveData) {
                        if let url = item.url { actions.openURL(url) }
                    }
                }
                if section.hidden > 0 { Text("还有 \(section.hidden) 个").font(DS.small).foregroundStyle(DS.dim).padding(.top, 2) }
            }.compactMenuGroup()
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
            if servicesExpanded { localServiceControls.frame(maxWidth: .infinity, alignment: .leading).padding(.leading, 26).padding(.vertical, 8) }
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

private struct MenuUsageSection: View {
    let value: MenuPanelPresentation.Cost
    let dimmed: Bool

    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            LazyVGrid(columns: [GridItem(.flexible(), spacing: 16), GridItem(.flexible())], alignment: .leading, spacing: 8) {
                Fact("今日", value.todayUSD.map(menuCurrency) ?? "—")
                Fact("今日 token", value.todayTokens.map(menuTokens) ?? "—")
                Fact("近 7 天", value.sevenDaysUSD.map(menuCurrency) ?? "—")
                Fact("7 天 token", value.sevenDaysTokens.map(menuTokens) ?? "—")
                Fact("近 30 天", menuCurrency(value.thirtyDaysUSD))
                Fact("30 天 token", value.thirtyDaysTokens.map(menuTokens) ?? "—")
            }
            MenuUsageChart(days: value.daily)
            VStack(alignment: .leading, spacing: 7) {
                Text("最近").font(DS.small).foregroundStyle(DS.dim)
                ForEach(value.recent) { day in
                    HStack(alignment: .firstTextBaseline, spacing: 8) {
                        VStack(alignment: .leading, spacing: 1) {
                            Text(day.title).font(DS.small).fontWeight(.semibold)
                            Text(day.date).font(DS.tiny).foregroundStyle(DS.dim)
                        }
                        Spacer(minLength: 8)
                        Text(day.summary).font(DS.small).fontWeight(.semibold).monospacedDigit()
                    }
                }
            }
            if let model = value.mostUsedModel, !model.isEmpty {
                Text("最常用模型: \(model)").font(DS.small).foregroundStyle(DS.dim).lineLimit(1)
            }
            Text(value.footer)
                .font(DS.small).foregroundStyle(DS.dim)
        }
        .opacity(dimmed ? 0.5 : 1)
        .padding(.vertical, 10)
        .overlay(alignment: .bottom) { DS.line.frame(height: 1) }
    }
}

private struct MenuUsageChart: View {
    let days: [DashboardUsageDay]

    var body: some View {
        let maximum = days.compactMap(\.costUsd).max() ?? 0
        GeometryReader { proxy in
            let chartHeight = max(1, proxy.size.height - 15)
            ZStack(alignment: .bottom) {
                Rectangle().fill(DS.line).frame(height: 1)
                HStack(alignment: .bottom, spacing: 2) {
                    ForEach(Array(days.enumerated()), id: \.offset) { _, day in
                        let cost = max(0, day.costUsd ?? 0)
                        RoundedRectangle(cornerRadius: 1.5)
                            .fill(cost > 0 ? DS.bar : DS.bar.opacity(0.28))
                            .frame(maxWidth: .infinity)
                            .frame(height: cost > 0 && maximum > 0 ? max(2, chartHeight * cost / maximum) : 2)
                    }
                }
                .frame(height: chartHeight)
                .padding(.top, 15)
                Text(menuCurrency(maximum))
                    .font(DS.tiny).foregroundStyle(DS.dim).monospacedDigit()
                    .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topTrailing)
            }
        }
        .frame(height: 60)
        .accessibilityElement(children: .ignore)
        .accessibilityLabel("最近 30 天费用图表，单日最高 \(menuCurrency(maximumCost))")
    }

    private var maximumCost: Double { days.compactMap(\.costUsd).max() ?? 0 }
}

func menuCurrency(_ value: Double) -> String {
    value.rounded() == value
        ? String(format: "$%.0f", locale: Locale(identifier: "en_US_POSIX"), value)
        : String(format: "$%.2f", locale: Locale(identifier: "en_US_POSIX"), value)
}

func menuTokens(_ value: Int64) -> String {
    let amount = Double(value)
    for (threshold, suffix) in [(1_000_000_000.0, "B"), (1_000_000.0, "M"), (1_000.0, "K")] where amount >= threshold {
        let scaled = amount / threshold
        return scaled < 10 && scaled.rounded() != scaled ? String(format: "%.1f%@", scaled, suffix) : String(format: "%.0f%@", scaled, suffix)
    }
    return "\(value)"
}

private struct MenuPanelBottomKey: PreferenceKey {
    static var defaultValue: CGFloat = 0
    static func reduce(value: inout CGFloat, nextValue: () -> CGFloat) { value = nextValue() }
}

private struct MenuPanelViewportKey: PreferenceKey {
    static var defaultValue: CGFloat = 0
    static func reduce(value: inout CGFloat, nextValue: () -> CGFloat) { value = nextValue() }
}

private struct MenuScopeTab: View {
    let tab: MenuPanelPresentation.Tab
    let selected: Bool
    let dimmed: Bool
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
                .foregroundStyle(selected ? Color.white : tab.enabled ? DS.fg : DS.dim)
                GeometryReader { proxy in
                    let total = max(1, tab.running + tab.attention)
                    ZStack(alignment: .leading) {
                        TrackSegment(color: selected ? Color.white.opacity(0.32) : DS.track)
                        HStack(spacing: 1) {
                            if tab.running > 0 {
                                TrackSegment(color: selected ? Color.white.opacity(0.9) : DS.bar)
                                    .frame(width: proxy.size.width * CGFloat(tab.running) / CGFloat(total))
                            }
                            if tab.attention > 0 {
                                TrackSegment(color: DS.warn)
                                    .frame(width: proxy.size.width * CGFloat(tab.attention) / CGFloat(total))
                            }
                        }
                    }
                    .opacity(dimmed ? 0.5 : 1)
                }.frame(height: 4)
            }
            .padding(.horizontal, 7).padding(.vertical, 7)
            .frame(minWidth: 50, maxWidth: 76)
            .background(selected ? DS.tab : hovering ? DS.raise : Color.clear, in: RoundedRectangle(cornerRadius: 8))
            .contentShape(RoundedRectangle(cornerRadius: 8))
        }
        .buttonStyle(.plain).onHover { hovering = $0 }
        .help(tab.fullName + (tab.enabled ? "" : " · 已暂停监控"))
        .accessibilityLabel(tab.fullName).accessibilityAddTraits(selected ? .isSelected : [])
    }
}

private struct MenuCompactProgress: View {
    let left: String
    let right: String
    let explanation: String?
    let segments: [(Int, Color)]
    let total: Int
    let dimmed: Bool
    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            HStack(alignment: .firstTextBaseline, spacing: 8) {
                Text(left).foregroundStyle(DS.fg).fontWeight(.semibold)
                Spacer(minLength: 4)
                Text(right).foregroundStyle(DS.dim).lineLimit(1).minimumScaleFactor(0.75)
            }.font(DS.small).monospacedDigit()
            GeometryReader { proxy in
                ZStack {
                    HStack(spacing: 1) {
                        if total > 0 {
                            ForEach(Array(segments.enumerated()), id: \.offset) { _, segment in
                                if segment.0 > 0 { TrackSegment(color: segment.1).frame(width: proxy.size.width * CGFloat(segment.0) / CGFloat(total)) }
                            }
                            let remainder = max(0, total - segments.reduce(0) { $0 + $1.0 })
                            if remainder > 0 { TrackSegment(color: DS.track) }
                        } else { TrackSegment(color: DS.track) }
                    }
                    HStack {
                        Spacer(); Rectangle().fill(DS.panel).frame(width: 1); Spacer()
                        Rectangle().fill(DS.panel).frame(width: 1); Spacer()
                    }.opacity(total > 0 ? 0.8 : 0)
                }
            }.frame(height: 6).opacity(dimmed ? 0.5 : 1)
            if let explanation, !explanation.isEmpty {
                Text(explanation).font(DS.small).foregroundStyle(DS.dim).lineLimit(1)
            }
        }
    }
}

private struct MenuTaskRow: View {
    let item: MenuPanelPresentation.TaskItem
    let connected: Bool
    let frozenAt: Date
    let dimmed: Bool
    let action: () -> Void
    @State private var hovering = false
    var body: some View {
        Button(action: action) {
            VStack(alignment: .leading, spacing: 2) {
                Text("\(item.row.reference) \(item.row.displayTitle)").lineLimit(1)
                HStack(spacing: 5) {
                    Text(item.row.kindZh + " · " + item.row.statusZh)
                    Spacer()
                    TaskElapsedText(row: item.row, connected: connected, frozenAt: frozenAt)
                }.font(DS.small).foregroundStyle(DS.dim).monospacedDigit()
                if item.row.group == .attention, let reason = item.row.meaningfulReasonZh {
                    Text(reason).font(DS.small).foregroundStyle(DS.warn).lineLimit(1)
                }
            }
            .padding(.horizontal, 8).padding(.vertical, 3)
            .frame(maxWidth: .infinity, alignment: .leading)
            .background(hovering ? DS.tab.opacity(0.14) : Color.clear, in: RoundedRectangle(cornerRadius: 7))
            .contentShape(RoundedRectangle(cornerRadius: 7))
        }
        .buttonStyle(.plain).onHover { hovering = $0 }
        .padding(.horizontal, -8)
        .opacity(dimmed ? 0.5 : 1)
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
            HStack(spacing: 8) {
                Image(systemName: symbol).frame(width: 18)
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
        .padding(.horizontal, -8)
        .accessibilityLabel(title)
    }
}

private extension View {
    func compactMenuGroup() -> some View {
        padding(.vertical, 7).overlay(alignment: .bottom) { DS.line.frame(height: 1) }
    }
}

