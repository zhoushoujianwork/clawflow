import SwiftUI

enum Destination: String, CaseIterable, Identifiable {
    case overview, pipelines, workers, projects
    var id: String { rawValue }
    var title: String {
        switch self { case .overview: return "概览"; case .pipelines: return "Pipelines"; case .workers: return "Workers"; case .projects: return "Projects" }
    }
    /// Persisted values from older builds ("Issue Queue", "Pipelines", "Tasks" …) fall back to the overview.
    static func restore(_ raw: String) -> Destination { Destination(rawValue: raw) ?? .overview }
    func badge(in snapshot: DashboardSnapshot?) -> String? {
        guard let snapshot else { return nil }
        switch self {
        case .overview: return nil
        case .pipelines: return "\(snapshot.activePipelineCount)"
        case .workers: return "\(snapshot.board.workers.filter(\.live).count)/\(snapshot.board.workers.count)"
        case .projects: return "\(snapshot.uniqueProjects.flatMap(\.repos).filter(\.enabled).count)"
        }
    }
}

/// Layout constants shared by the pages and the trailing pane.
enum DashboardLayout {
    static let pane: CGFloat = 400
    static let gap: CGFloat = 28
    /// Below this list width the stage bar hides so the title keeps its room.
    static let stageBarMinList: CGFloat = 720
    static var paneFootprint: CGFloat { pane + gap * 2 + 1 }
}

struct DashboardView: View {
    @ObservedObject var model: DashboardModel
    @AppStorage("dashboard.destination") private var storedDestination = Destination.overview.rawValue
    @State private var search = ""
    @State private var repoFilter = ""
    @State private var showAll = false
    @State private var selected: IssuePipeline.Identity?
    @State private var job: String?
    @State private var autoSelected = false
    private var destination: Destination { Destination.restore(storedDestination) }
    private func go(_ next: Destination) { storedDestination = next.rawValue; job = nil }

    var body: some View {
        VStack(spacing: 0) {
            tabRow
            if let snapshot = model.snapshot {
                if destination != .workers { chipRow(snapshot) }
                messages
                GeometryReader { proxy in content(snapshot, width: proxy.size.width) }
            } else {
                messages
                ContentUnavailableView {
                    Label("连接 ClawFlow", systemImage: "network")
                } description: {
                    Text("在设置里选择 controller。关闭这个窗口不会停止任务和局域网 Worker。")
                } actions: { SettingsLink { Text("打开设置") }.buttonStyle(.borderedProminent) }
            }
        }
        .padding(.horizontal, 20).padding(.top, 12).padding(.bottom, 4)
        .foregroundStyle(DS.fg)
        .background(DS.panel)
        .frame(minWidth: 1040, minHeight: 680)
    }

    private var tabRow: some View {
        HStack(spacing: 4) {
            ForEach(Destination.allCases) { item in
                TabPill(title: item.title, count: item.badge(in: model.snapshot), selected: destination == item) { go(item) }
            }
            Spacer(minLength: 12)
            FreshnessText(summary: model.summary, received: model.received)
            HStack(spacing: 4) {
                Image(systemName: "magnifyingglass").foregroundStyle(DS.dim)
                TextField("搜索", text: $search).textFieldStyle(.plain).frame(width: 130)
            }.font(DS.sub).padding(.horizontal, 8).padding(.vertical, 4)
                .background(DS.raise, in: RoundedRectangle(cornerRadius: 8))
            Button { Task { await model.refresh() } } label: { Image(systemName: "arrow.clockwise") }
                .buttonStyle(.plain).foregroundStyle(DS.dim).keyboardShortcut("r").help("刷新").accessibilityLabel("刷新")
            SettingsLink { Image(systemName: "gearshape") }
                .buttonStyle(.plain).foregroundStyle(DS.dim).help("设置").accessibilityLabel("设置")
        }
        .padding(.bottom, 10)
        .overlay(alignment: .bottom) { DS.line.frame(height: 1) }
    }

    private func chipRow(_ snapshot: DashboardSnapshot) -> some View {
        var repos = snapshot.filterRepos
        if !repoFilter.isEmpty, !repos.contains(where: { $0.name == repoFilter }), let extra = snapshot.repository(repoFilter) { repos.append(extra) }
        return FlowLayout(spacing: 4) {
            Chip(title: "全部仓库", selected: repoFilter.isEmpty) { repoFilter = "" }
            ForEach(repos) { repo in
                Chip(title: repo.shortName, selected: repoFilter == repo.name) { repoFilter = repo.name }.help(repo.name)
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(.vertical, 9)
        .overlay(alignment: .bottom) { DS.line.frame(height: 1) }
    }

    @ViewBuilder private var messages: some View {
        if let error = model.error {
            Text(error).font(DS.small).foregroundStyle(DS.warn).textSelection(.enabled).padding(.vertical, 6)
                .frame(maxWidth: .infinity, alignment: .leading)
        }
        if let pending = model.pendingAction {
            HStack(spacing: 6) { ProgressView().controlSize(.small); Text(pending.textZh) }
                .font(DS.small).foregroundStyle(DS.dim).padding(.vertical, 6).frame(maxWidth: .infinity, alignment: .leading)
        }
        if let error = model.actionError {
            HStack {
                Text(error).foregroundStyle(DS.warn).textSelection(.enabled)
                Button("关闭") { model.actionError = nil }.buttonStyle(DButtonStyle())
            }.font(DS.small).padding(.vertical, 6).frame(maxWidth: .infinity, alignment: .leading)
        }
    }

    private func matches(_ row: IssuePipeline) -> Bool {
        (repoFilter.isEmpty || row.repo == repoFilter) &&
        (search.isEmpty || "\(row.repo) \(row.reference) \(row.title)".localizedCaseInsensitiveContains(search))
    }

    @ViewBuilder private func content(_ snapshot: DashboardSnapshot, width: CGFloat) -> some View {
        let rows = snapshot.issuePipelines.filter(matches)
        let showsPane = (destination == .overview || destination == .pipelines) && selected != nil
        let listWidth = showsPane ? width - DashboardLayout.paneFootprint : width
        let selection = Binding<IssuePipeline.Identity?>(get: { selected }, set: { selected = $0; job = nil })
        HStack(alignment: .top, spacing: 0) {
            Group {
                switch destination {
                case .overview:
                    OverviewPage(model: model, snapshot: snapshot, rows: rows, repoFilter: repoFilter,
                                 selected: selection, showStages: listWidth >= DashboardLayout.stageBarMinList)
                case .pipelines:
                    PipelinesPage(model: model, snapshot: snapshot, rows: rows, showAll: $showAll,
                                  selected: selection, showStages: listWidth >= DashboardLayout.stageBarMinList)
                case .workers:
                    WorkersPage(model: model, snapshot: snapshot, search: search)
                case .projects:
                    ProjectsPage(model: model, snapshot: snapshot, search: search, repoFilter: repoFilter) { name in
                        repoFilter = name; search = ""; go(.pipelines)
                    }
                }
            }.frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .top)
            if showsPane, let identity = selected {
                HStack(spacing: 0) {
                    DS.line.frame(width: 1)
                    PipelinePane(model: model, snapshot: snapshot, identity: identity, job: $job) { selected = nil; job = nil }
                        .padding(.leading, DashboardLayout.gap)
                }
                .frame(width: DashboardLayout.pane + DashboardLayout.gap + 1)
                .padding(.leading, DashboardLayout.gap)
            }
        }
        .onExitCommand { selected = nil; job = nil }
        .onAppear { autoSelect(snapshot, width: width) }
        .onChange(of: width) { _, next in autoSelect(snapshot, width: next) }
    }

    /// Opens the first attention pipeline once, only when the window fits the pane next to a full list.
    private func autoSelect(_ snapshot: DashboardSnapshot, width: CGFloat) {
        guard !autoSelected, selected == nil else { return }
        guard width >= DashboardLayout.paneFootprint + DashboardLayout.stageBarMinList else { return }
        autoSelected = true
        selected = snapshot.issuePipelines.first { $0.group == .attention }?.id
    }
}

/// "已连接 · 12 秒前"; stale and disconnected states use their own wording and stop the dot.
struct FreshnessText: View {
    let summary: StatusSummary
    let received: Date?
    var body: some View {
        HStack(spacing: 6) {
            Circle().fill(summary.connected ? DS.bar : DS.faint).frame(width: 7, height: 7)
            if summary.connected, let received {
                TimelineView(.periodic(from: .now, by: 1)) { context in
                    let seconds = max(0, Int(context.date.timeIntervalSince(received)))
                    Text("已连接 · " + (seconds < 60 ? "\(seconds) 秒前" : relativeAge(received, now: context.date)))
                }
            } else {
                Text(summary.freshnessZh)
            }
        }
        .font(DS.small).foregroundStyle(summary.connected ? DS.dim : DS.warn)
        .monospacedDigit().lineLimit(1)
        .accessibilityElement(children: .combine)
    }
}

/// Wraps chips onto several lines, like the mock's flex-wrap row.
struct FlowLayout: Layout {
    var spacing: CGFloat = 4
    func sizeThatFits(proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) -> CGSize {
        let rows = arrange(width: proposal.width ?? .infinity, subviews: subviews)
        return CGSize(width: proposal.width ?? rows.map(\.width).max() ?? 0, height: rows.reduce(0) { $0 + $1.height } + spacing * CGFloat(max(0, rows.count - 1)))
    }
    func placeSubviews(in bounds: CGRect, proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) {
        var y = bounds.minY
        for row in arrange(width: bounds.width, subviews: subviews) {
            var x = bounds.minX
            for index in row.items {
                let size = subviews[index].sizeThatFits(.unspecified)
                subviews[index].place(at: CGPoint(x: x, y: y), proposal: ProposedViewSize(size))
                x += size.width + spacing
            }
            y += row.height + spacing
        }
    }
    private struct Row { var items: [Int] = []; var width: CGFloat = 0; var height: CGFloat = 0 }
    private func arrange(width: CGFloat, subviews: Subviews) -> [Row] {
        var rows = [Row()]
        for index in subviews.indices {
            let size = subviews[index].sizeThatFits(.unspecified)
            if !rows[rows.count - 1].items.isEmpty, rows[rows.count - 1].width + spacing + size.width > width { rows.append(Row()) }
            var row = rows[rows.count - 1]
            row.width += (row.items.isEmpty ? 0 : spacing) + size.width
            row.height = max(row.height, size.height)
            row.items.append(index)
            rows[rows.count - 1] = row
        }
        return rows
    }
}

struct TaskDetail: View {
    @ObservedObject var model: DashboardModel
    let snapshot: DashboardSnapshot
    let row: TaskRow
    @State private var showEvents = true
    @State private var events: [TaskEvent] = []
    @State private var eventError: String?
    @State private var loading = false
    private var state: StageState { IssuePipeline.jobState(row).0 }
    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            DSection {
                Text(row.displayTitle).font(DS.h2).textSelection(.enabled).fixedSize(horizontal: false, vertical: true)
                Text("\(row.kindZh) · \(row.statusZh)").font(DS.sub).foregroundStyle(state.tone.textColor).padding(.top, 3)
            }
            DSection {
                Grid(alignment: .leading, horizontalSpacing: 18, verticalSpacing: 12) {
                    GridRow { Fact("类型", row.kindZh); Fact("Worker", row.worker.isEmpty ? "未分配" : row.worker) }
                    GridRow {
                        Fact("耗时") { TaskElapsedText(row: row, connected: model.summary.connected, frozenAt: snapshot.board.generatedAt) }
                        Fact("第几次", "\(row.attempt + 1)")
                    }
                    GridRow {
                        Fact("最近更新", relativeAge(row.updatedAt, now: snapshot.board.generatedAt))
                        Fact("需要标签", row.tags.isEmpty ? "—" : row.tags.joined(separator: ", "))
                    }
                }
                FlowLayout(spacing: 6) {
                    if let link = row.link(in: snapshot) { Link(destination: link) { Text(row.issue > 0 ? "打开 Issue" : "打开 PR") }.buttonStyle(DButtonStyle()) }
                    if row.issue > 0, let pr = row.prLink(in: snapshot) { Link(destination: pr) { Text("PR #\(row.pr)") }.buttonStyle(DButtonStyle()) }
                }.padding(.top, 10)
            }
            if let reason = row.meaningfulReasonZh {
                DSection {
                    DLabel(text: "原因", top: 0)
                    Text(reason).font(DS.sub).foregroundStyle(DS.dim).textSelection(.enabled).fixedSize(horizontal: false, vertical: true)
                }
            }
            DSection(rule: false) {
                DLabel(text: "执行日志", top: 0)
                JobLogView(model: model, row: row)
                DisclosureGroup("调度事件", isExpanded: $showEvents) {
                    VStack(alignment: .leading, spacing: 6) {
                        Text("连接时每 5 秒刷新。").font(DS.small).foregroundStyle(DS.dim)
                        if loading { ProgressView().controlSize(.small) }
                        if let eventError { Text(eventError).font(DS.small).foregroundStyle(DS.warn).textSelection(.enabled) }
                        ForEach(Array(events.enumerated()), id: \.offset) { _, event in
                            HStack(alignment: .top) {
                                Text(event.at, format: .dateTime.hour().minute().second()).foregroundStyle(DS.dim)
                                Text(event.action).textSelection(.enabled)
                            }.font(.caption.monospaced())
                        }
                        Button("刷新事件") { Task { await loadEvents() } }.buttonStyle(DButtonStyle()).disabled(!model.summary.connected || loading)
                    }.frame(maxWidth: .infinity, alignment: .leading).padding(.top, 6)
                }
                .font(DS.sub).padding(.top, 8)
                .onChange(of: showEvents) { _, expanded in if expanded { Task { await loadEvents() } } }
                Text("Task ID：\(row.id)").font(DS.small).foregroundStyle(DS.faint).textSelection(.enabled).padding(.top, 10)
            }
        }
        .task(id: row.id) {
            while !Task.isCancelled {
                if showEvents { await loadEvents() }
                do { try await Task.sleep(for: .seconds(5)) } catch { break }
            }
        }
    }
    private func loadEvents() async {
        guard model.summary.connected, !loading else { return }
        loading = true; eventError = nil; defer { loading = false }
        let connection = model.connection
        do {
            let next = try decoder().decode([TaskEvent].self, from: await model.request(["events", row.id]))
            guard !Task.isCancelled, connection == model.connection else { return }
            events = next
        } catch {
            if !Task.isCancelled, connection == model.connection { eventError = error.localizedDescription }
        }
    }
}

struct WorkersPage: View {
    @ObservedObject var model: DashboardModel
    let snapshot: DashboardSnapshot
    let search: String
    @State private var adding = false
    @State private var removing: String?
    private var rows: [WorkerRow] { snapshot.board.workers.filter { search.isEmpty || "\($0.name) \($0.tags.joined(separator: " "))".localizedCaseInsensitiveContains(search) } }
    private var canAct: Bool { model.summary.connected && !model.busy }
    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 0) {
                DSection {
                    HStack {
                        Text("\(snapshot.board.workers.filter(\.live).count)/\(snapshot.board.workers.count) 在线").font(DS.sub).foregroundStyle(DS.dim)
                        Spacer()
                        Button("添加 Worker") { adding = true }.buttonStyle(DButtonStyle()).disabled(!canAct)
                    }
                }
                if rows.isEmpty { DashboardEmptyState(text: "没有 Worker") }
                ForEach(rows) { worker in card(worker) }
                Text("停止新派发会让正在跑的任务继续完成。能否派发取决于仓库和 Worker 的标签；恢复新 Worker 前先检查标签。")
                    .font(DS.small).foregroundStyle(DS.dim).fixedSize(horizontal: false, vertical: true).padding(.top, 12)
            }
        }
        .sheet(isPresented: $adding) { AddWorkerView(model: model) }
    }
    private func card(_ w: WorkerRow) -> some View {
        let active = snapshot.board.tasks.filter { $0.worker == w.name && $0.group != .history }
        let leased = snapshot.board.tasks.contains { $0.worker == w.name && $0.state == "leased" }
        return DSection {
            DHeader(title: Text(w.name)) { Text(w.stateZh).foregroundStyle(w.live && !w.paused ? DS.dim : DS.warn) }
            HStack(spacing: 2) {
                ForEach(0..<max(w.maxParallel, 0), id: \.self) { slot in TrackSegment(color: slot < w.runningTasks.count ? DS.bar : DS.track) }
            }
            .frame(height: 7).padding(.top, 9).padding(.bottom, 7)
            .accessibilityElement(children: .ignore)
            .accessibilityLabel("\(w.name) 槽位：\(w.maxParallel) 个中 \(w.runningTasks.count) 个在跑")
            HStack {
                Text("运行中 \(w.runningTasks.count)/\(w.maxParallel)" + (w.roles.isEmpty ? "" : " · " + w.roles.joined(separator: "、")))
                Spacer()
                Text("最近联系 \(relativeAge(w.lastSeen, now: snapshot.board.generatedAt))")
            }.font(DS.sub).foregroundStyle(DS.dim).monospacedDigit()
            if !w.tags.isEmpty { Text("标签：" + w.tags.joined(separator: ", ")).font(DS.small).foregroundStyle(DS.dim).padding(.top, 2) }
            if !active.isEmpty {
                Text("在跑：" + active.map { "\($0.repoShortName) \($0.reference)" }.joined(separator: ", ")).font(DS.small).foregroundStyle(DS.dim).padding(.top, 2)
            }
            HStack(spacing: 6) {
                Button(w.paused ? "恢复派发" : "停止新派发") { Task { await model.perform(w.paused ? "resume" : "drain", name: w.name) } }
                    .buttonStyle(DButtonStyle()).disabled(!canAct || w.revoked)
                Button("移除") { removing = w.name }
                    .buttonStyle(DButtonStyle(role: .destructive)).disabled(!canAct || w.revoked || !w.paused || leased)
                    .help(w.paused ? "撤销这个 Worker 的凭证" : "先停止新派发，等任务结束后再移除")
            }.padding(.top, 10)
            .confirmationDialog("撤销 \(w.name) 的访问？", isPresented: Binding(get: { removing == w.name }, set: { if !$0 { removing = nil } })) {
                Button("移除 Worker", role: .destructive) { Task { await model.perform("remove", name: w.name) } }
            } message: { Text("历史任务会保留。这个 Worker 的凭证将不能再认证。") }
        }
    }
}

struct ProjectsPage: View {
    @ObservedObject var model: DashboardModel
    let snapshot: DashboardSnapshot
    let search: String
    let repoFilter: String
    let showPipelines: (String) -> Void
    var body: some View {
        let active = snapshot.issuePipelines.filter { $0.group != .history }.reduce(into: [String: Int]()) { $0[$1.repo, default: 0] += 1 }
        let repos = snapshot.uniqueProjects.flatMap(\.repos).filter {
            (repoFilter.isEmpty || $0.name == repoFilter) && (search.isEmpty || $0.name.localizedCaseInsensitiveContains(search))
        }
        let on = repos.filter { (active[$0.name] ?? 0) > 0 || $0.queueError != nil }
        let off = repos.filter { repo in !on.contains { $0.id == repo.id } }
        ScrollView {
            DSection(rule: false) {
                DLabel(text: "有活动的仓库 \(on.count)", top: 0)
                if on.isEmpty { Text("没有").font(DS.sub).foregroundStyle(DS.dim) }
                ForEach(on) { repo in line(repo, active: active[repo.name] ?? 0) }
                if !off.isEmpty {
                    DLabel(text: "其他仓库 \(off.count)")
                    ForEach(off) { repo in line(repo, active: 0) }
                }
            }
        }
    }
    private func line(_ repo: RepoRow, active: Int) -> some View {
        HStack(alignment: .center, spacing: 10) {
            RowButton(action: { showPipelines(repo.name) }) {
                Grid(alignment: .leading, horizontalSpacing: 16, verticalSpacing: 2) {
                    GridRow {
                        Text(repo.name).fontWeight(.semibold).foregroundStyle(repo.enabled ? DS.fg : DS.dim).lineLimit(1).frame(maxWidth: .infinity, alignment: .leading)
                        Text(active > 0 ? "\(active) 条进行中" : repo.idleNote).fontWeight(.semibold).gridColumnAlignment(.trailing)
                    }
                    GridRow {
                        (Text((repo.skippedIssues ?? 0) > 0 ? "\(repo.skippedIssues!) 条跳过 · " : "")
                         + (repo.queueError.map { Text($0).foregroundStyle(DS.warn) + Text(" · ") } ?? Text(""))
                         + Text(repo.queueUpdatedAt.map { "队列 " + relativeAge($0, now: snapshot.board.generatedAt) } ?? "未评估"))
                            .font(DS.small).foregroundStyle(DS.dim).lineLimit(1).help(repo.queueError ?? "")
                        HStack(spacing: 4) {
                            Pill(text: "CI", on: repo.ciRequired); Pill(text: "验收", on: repo.acceptanceRequired); Pill(text: "自动合并", on: repo.autoMerge)
                        }
                    }
                }
            }
            .help("在 Pipelines 里查看 \(repo.name)")
            Button(repo.enabled ? "暂停" : "恢复") { Task { await model.perform(repo.enabled ? "disable" : "enable", name: repo.name) } }
                .buttonStyle(DButtonStyle()).disabled(!model.summary.connected || model.busy)
                .help(repo.enabled ? "暂停只是不再接新 Issue，排队和运行中的任务不会停止" : "恢复监控这个仓库")
        }
    }
}
