import SwiftUI

extension StageState {
    var tone: PipelineTone {
        switch self {
        case .finished: return .finished
        case .waiting: return .waiting
        case .running: return .running
        case .attention: return .attention
        case .failed: return .failed
        case .pending: return .pending
        case .notReported: return .notReported
        }
    }
}

/// Segmented stage bar with tiny labels. The accessibility label carries every state in words.
struct StageBar: View {
    let stages: [PipelineStage]
    var width: CGFloat = 168
    var body: some View {
        VStack(spacing: 3) {
            HStack(spacing: 2) {
                ForEach(stages) { stage in
                    TrackSegment(color: stage.state.tone.color, dashed: stage.state == .notReported)
                        .help("\(stage.title)：\(stage.state.text)")
                }
            }.frame(height: 6)
            HStack(spacing: 2) {
                ForEach(stages) { stage in
                    Text(stage.title).font(DS.tiny).foregroundStyle(DS.faint).lineLimit(1).frame(maxWidth: .infinity)
                }
            }
        }
        .frame(width: width)
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(stages.map { "\($0.title)：\($0.state.text)" }.joined(separator: "，") + "。Job 状态，不代表交付结论。")
    }
}

struct PipelineRow: View {
    let row: IssuePipeline
    let selected: Bool
    let showStages: Bool
    let select: () -> Void
    private var secondary: String {
        ([row.repoShortName + " " + row.reference] + (row.prs.isEmpty ? [] : ["PR " + row.prs.map { "#\($0)" }.joined(separator: ", ")]))
            .joined(separator: " · ")
    }
    var body: some View {
        RowButton(selected: selected, action: select) {
            HStack(alignment: .center, spacing: 16) {
                VStack(alignment: .leading, spacing: 2) {
                    Text(row.title).fontWeight(.semibold).lineLimit(1).help(row.title)
                    (Text(secondary).foregroundStyle(DS.dim)
                     + (row.rowNote.map { Text(" · ") + Text($0).foregroundStyle(row.group == .attention ? DS.warn : DS.dim) } ?? Text("")))
                        .font(DS.small).lineLimit(1).truncationMode(.tail)
                }.frame(maxWidth: .infinity, alignment: .leading)
                if showStages { StageBar(stages: row.stages) }
            }
        }
        .accessibilityElement(children: .combine)
        .accessibilityLabel("\(row.title)，\(row.status)" + (row.rowNote.map { "，\($0)" } ?? ""))
    }
}

/// Rows grouped under dim labels; selection toggles, as in the mock.
struct PipelineSections: View {
    let groups: [(String, [IssuePipeline])]
    @Binding var selected: IssuePipeline.Identity?
    let showStages: Bool
    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            ForEach(Array(groups.enumerated()), id: \.offset) { index, group in
                DLabel(text: group.0, top: index == 0 ? 0 : 14)
                ForEach(group.1) { row in
                    PipelineRow(row: row, selected: selected == row.id, showStages: showStages) {
                        selected = selected == row.id ? nil : row.id
                    }
                }
            }
        }
    }
}

/// Up/down keys move the selection through the visible rows.
private struct KeyboardSelection: ViewModifier {
    let order: [IssuePipeline.Identity]
    @Binding var selected: IssuePipeline.Identity?
    func body(content: Content) -> some View {
        content.focusable().focusEffectDisabled()
            .onKeyPress(.downArrow) { move(1) }
            .onKeyPress(.upArrow) { move(-1) }
    }
    private func move(_ step: Int) -> KeyPress.Result {
        guard !order.isEmpty else { return .ignored }
        let index = selected.flatMap { order.firstIndex(of: $0) }.map { $0 + step } ?? (step > 0 ? 0 : order.count - 1)
        selected = order[min(max(index, 0), order.count - 1)]
        return .handled
    }
}

struct OverviewPage: View {
    @ObservedObject var model: DashboardModel
    let snapshot: DashboardSnapshot
    let rows: [IssuePipeline]
    let repoFilter: String
    @Binding var selected: IssuePipeline.Identity?
    let showStages: Bool
    var body: some View {
        let active = rows.filter { $0.group == .attention || $0.group == .running }
        let waiting = rows.filter { $0.group == .waiting }
        let recent = Array(rows.filter { $0.group == .history }.prefix(4))
        ScrollView {
            VStack(alignment: .leading, spacing: 0) {
                head
                if repoFilter.isEmpty { CapacitySection(capacity: snapshot.capacity) }
                DSection {
                    if active.isEmpty { Text("现在没有需要你处理的事。").font(DS.sub).foregroundStyle(DS.dim) }
                    PipelineSections(groups: [("需要处理和运行中", active), ("等待 \(waiting.count)", waiting), ("最近结束", recent)].filter { !$0.1.isEmpty },
                                     selected: $selected, showStages: showStages)
                        .padding(.top, active.isEmpty ? 14 : 0)
                }
                MetricsSection(metrics: snapshot.metrics(repo: repoFilter))
            }
        }
        .scrollIndicators(.automatic)
        .contentMargins(.horizontal, 10, for: .scrollContent).padding(.horizontal, -10)
        .modifier(KeyboardSelection(order: (active + waiting + recent).map(\.id), selected: $selected))
    }
    private var head: some View {
        let attention = rows.filter { $0.group == .attention }.count
        return DSection {
            HStack(alignment: .firstTextBaseline) {
                Text(repoFilter.isEmpty ? "ClawFlow" : (repoFilter.split(separator: "/").last.map(String.init) ?? repoFilter)).font(DS.big)
                Spacer()
                Text("Controller \(snapshot.version)").font(DS.sub).foregroundStyle(DS.dim)
            }
            HStack(spacing: 0) {
                if attention > 0 { Text("\(attention) 条需要处理").foregroundStyle(DS.warn) } else { Text("没有需要处理的事") }
                if let throughput = snapshot.board.throughput { Text(" · " + throughputLabel(throughput, generatedAt: snapshot.board.generatedAt)) }
            }.font(DS.sub).foregroundStyle(DS.dim).padding(.top, 2)
        }
    }
}

struct PipelinesPage: View {
    @ObservedObject var model: DashboardModel
    let snapshot: DashboardSnapshot
    let rows: [IssuePipeline]
    @Binding var showAll: Bool
    @Binding var selected: IssuePipeline.Identity?
    let showStages: Bool
    var body: some View {
        let visible = rows.filter { showAll || $0.group != .history }
        let groups = [TaskGroup.attention, .running, .waiting, .history].compactMap { group -> (String, [IssuePipeline])? in
            let members = visible.filter { $0.group == group }
            return members.isEmpty ? nil : ("\(group.title) \(members.count)", members)
        }
        ScrollView {
            VStack(alignment: .leading, spacing: 0) {
                DSection {
                    HStack {
                        Chip(title: "进行中", selected: !showAll) { showAll = false }
                        Chip(title: "全部", selected: showAll) { showAll = true }
                        Spacer()
                        Text("\(visible.count) 条 Pipeline").font(DS.sub).foregroundStyle(DS.dim)
                    }
                }
                DSection(rule: false) {
                    if groups.isEmpty { DashboardEmptyState(text: "这里没有 Pipeline") }
                    PipelineSections(groups: groups, selected: $selected, showStages: showStages)
                }
            }
        }
        .contentMargins(.horizontal, 10, for: .scrollContent).padding(.horizontal, -10)
        .modifier(KeyboardSelection(order: groups.flatMap { $0.1.map(\.id) }, selected: $selected))
    }
}

/// Worker slots as one segmented bar with a tick between workers and a red mark for held leases.
struct CapacitySection: View {
    let capacity: WorkerCapacity
    var body: some View {
        DSection {
            DHeader(title: Text("Worker 槽位 ") + Text("\(capacity.running)/\(capacity.total)").monospacedDigit() + Text(" 在跑")) {
                Text("\(capacity.live)/\(capacity.workers.count) 在线")
            }
            GeometryReader { proxy in
                HStack(spacing: 2) {
                    ForEach(Array(capacity.workers.enumerated()), id: \.offset) { index, worker in
                        if index > 0 { DS.fg.frame(width: 2).padding(.vertical, -1).padding(.horizontal, 2) }
                        ForEach(0..<max(worker.slots, 0), id: \.self) { slot in
                            TrackSegment(color: slot < worker.running ? DS.bar : DS.track)
                        }
                    }
                }
                .overlay(alignment: .leading) {
                    if let position = capacity.heldPosition {
                        DS.bad.frame(width: 4).padding(.vertical, -1).offset(x: proxy.size.width * position - 2)
                    }
                }
            }
            .frame(height: 7).padding(.top, 9).padding(.bottom, 7)
            .accessibilityElement(children: .ignore)
            .accessibilityLabel("Worker 槽位：\(capacity.total) 个中 \(capacity.running) 个在跑" + (capacity.held > 0 ? "，\(capacity.held) 个租约挂起" : ""))
            (Text(capacity.workers.map { "\($0.name) \($0.running)/\($0.slots)" }.joined(separator: " · "))
             + (capacity.held > 0 ? Text(" · ") + Text("\(capacity.held) 个租约挂起，没有释放").foregroundStyle(DS.bad) : Text("")))
                .font(DS.sub).foregroundStyle(DS.dim).monospacedDigit()
        }
    }
}

struct MetricsSection: View {
    let metrics: DashboardMetrics
    private func count(_ value: Int?) -> String { value.map(String.init) ?? "—" }
    var body: some View {
        DSection(rule: false) {
            DHeader(title: Text("交付节奏")) { Text("快照保留的 \(metrics.jobCount) 个 Job") }
            Grid(alignment: .leading, horizontalSpacing: 18, verticalSpacing: 12) {
                GridRow {
                    metric("今日完成", count(metrics.todayDone), large: true)
                    metric("今日失败", count(metrics.todayFailed), large: true, bad: (metrics.todayFailed ?? 0) > 0)
                    metric("等待中", "\(metrics.waiting)", large: true)
                }
                GridRow {
                    metric("Job 成功率", metrics.successRate.map { "\(Int(($0 * 100).rounded()))%" } ?? "—")
                    metric("实现耗时中位数", metrics.medianImplementDuration.map(durationText) ?? "—")
                    metric("修复 Job", count(metrics.repairJobs))
                }
            }.padding(.top, 10)
            HStack {
                HStack(spacing: 12) {
                    legend(DS.bar, "完成"); legend(DS.bad, "失败")
                }
                Spacer()
                Text("单日最多 \(metrics.dayMax)")
            }.font(.system(size: 12)).foregroundStyle(DS.dim).padding(.top, 14).padding(.bottom, 2)
            DayChart(days: metrics.days, dayMax: metrics.dayMax)
        }
    }
    private func metric(_ label: String, _ value: String, large: Bool = false, bad: Bool = false) -> some View {
        VStack(alignment: .leading, spacing: 2) {
            Text(label).font(DS.small).foregroundStyle(DS.dim)
            Text(value).font(.system(size: large ? 21 : 16, weight: .semibold)).monospacedDigit()
                .foregroundStyle(bad ? DS.bad : DS.fg).lineLimit(1)
        }.frame(maxWidth: .infinity, alignment: .leading)
            .accessibilityElement(children: .combine)
    }
    private func legend(_ color: Color, _ text: String) -> some View {
        HStack(spacing: 4) { RoundedRectangle(cornerRadius: 2).fill(color).frame(width: 8, height: 8); Text(text) }
    }
}

/// Seven thin stacked columns: failed on top of done, a flat track line on empty days.
struct DayChart: View {
    let days: [DashboardMetrics.Day]
    let dayMax: Int
    private static let label: DateFormatter = { let f = DateFormatter(); f.dateFormat = "M/d"; return f }()
    var body: some View {
        VStack(spacing: 4) {
            HStack(alignment: .bottom, spacing: 6) {
                ForEach(days, id: \.start) { day in
                    GeometryReader { proxy in
                        let barWidth = min(18, proxy.size.width * 0.6)
                        VStack(spacing: 1) {
                            Spacer(minLength: 0)
                            if day.done + day.failed == 0 {
                                DS.track.frame(width: barWidth, height: 2)
                            } else {
                                if day.failed > 0 { DS.bad.frame(width: barWidth, height: proxy.size.height * CGFloat(day.failed) / CGFloat(dayMax)) }
                                if day.done > 0 {
                                    UnevenRoundedRectangle(topLeadingRadius: day.failed > 0 ? 0 : 2, topTrailingRadius: day.failed > 0 ? 0 : 2)
                                        .fill(DS.bar).frame(width: barWidth, height: proxy.size.height * CGFloat(day.done) / CGFloat(dayMax))
                                }
                            }
                        }.frame(maxWidth: .infinity)
                    }
                    .help("\(Self.label.string(from: day.start))：完成 \(day.done)，失败 \(day.failed)")
                }
            }.frame(height: 72).padding(.top, 6)
            HStack(spacing: 6) {
                ForEach(days, id: \.start) { day in
                    Text(Self.label.string(from: day.start)).font(.system(size: 11)).foregroundStyle(DS.faint).frame(maxWidth: .infinity)
                }
            }
        }
        .accessibilityElement(children: .ignore)
        .accessibilityLabel("近 7 天每天结束的 Job 数：" + days.map { "\(Self.label.string(from: $0.start)) 完成 \($0.done) 失败 \($0.failed)" }.joined(separator: "，"))
    }
}

/// Inline trailing detail. Looks the pipeline up by identity on every snapshot, so it survives refreshes and completion.
struct PipelinePane: View {
    @ObservedObject var model: DashboardModel
    let snapshot: DashboardSnapshot
    let identity: IssuePipeline.Identity
    @Binding var job: String?
    let close: () -> Void
    var body: some View {
        let pipeline = snapshot.issuePipelines.first { $0.id == identity }
        ScrollView {
            if let pipeline {
                if let job, let row = pipeline.jobs.first(where: { $0.id == job }) {
                    VStack(alignment: .leading, spacing: 0) {
                        DSection {
                            HStack {
                                Button { self.job = nil } label: { Text("‹ 返回 Pipeline") }.buttonStyle(.plain).font(DS.sub).foregroundStyle(DS.dim)
                                Spacer()
                                CloseButton(action: close)
                            }
                        }
                        TaskDetail(model: model, snapshot: snapshot, row: row).id(row.id)
                    }
                } else {
                    PipelineDetail(model: model, snapshot: snapshot, pipeline: pipeline, close: close) { job = $0 }
                }
            } else {
                DSection(rule: false) {
                    HStack { Text("Pipeline").font(DS.sub).foregroundStyle(DS.dim); Spacer(); CloseButton(action: close) }
                    Text("Pipeline 不可用").font(DS.h2).padding(.top, 4)
                    Text("快照里已经没有这条 Pipeline。").font(DS.sub).foregroundStyle(DS.dim).padding(.top, 3)
                    Button("关闭", action: close).buttonStyle(DButtonStyle()).padding(.top, 10)
                }
            }
        }
    }
}

struct CloseButton: View {
    let action: () -> Void
    var body: some View {
        Button(action: action) { Text("×").font(.system(size: 16)).foregroundStyle(DS.dim).padding(.horizontal, 6).padding(.vertical, 2) }
            .buttonStyle(.plain).keyboardShortcut(.cancelAction).help("关闭详情").accessibilityLabel("关闭详情")
    }
}

struct PipelineDetail: View {
    @ObservedObject var model: DashboardModel
    let snapshot: DashboardSnapshot
    let pipeline: IssuePipeline
    let close: () -> Void
    let open: (String) -> Void
    private var now: Date { snapshot.board.generatedAt }
    private var issueLink: URL? {
        guard pipeline.issue > 0, let repo = snapshot.repository(pipeline.repo), let base = safeWebURL(repo.url) else { return nil }
        return base.appendingPathComponent(repo.platform == "gitlab" ? "-/issues/\(pipeline.issue)" : "issues/\(pipeline.issue)")
    }
    private func prLink(_ pr: Int) -> URL? {
        guard let repo = snapshot.repository(pipeline.repo), let base = safeWebURL(repo.url) else { return nil }
        return base.appendingPathComponent(repo.platform == "gitlab" ? "-/merge_requests/\(pr)" : "pull/\(pr)")
    }
    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            DSection {
                HStack { Text("Pipeline").font(DS.sub).foregroundStyle(DS.dim); Spacer(); CloseButton(action: close) }
                Text(pipeline.title).font(DS.h2).lineSpacing(2).textSelection(.enabled).fixedSize(horizontal: false, vertical: true).padding(.top, 4)
                Text("\(pipeline.repo) \(pipeline.reference)" + (pipeline.prs.isEmpty ? "" : " · PR " + pipeline.prs.map { "#\($0)" }.joined(separator: ", ")))
                    .font(DS.sub).foregroundStyle(DS.dim).textSelection(.enabled).padding(.top, 3)
                FlowLayout(spacing: 6) {
                    if let issueLink { Link(destination: issueLink) { Text("打开 Issue") }.buttonStyle(DButtonStyle()) }
                    ForEach(pipeline.prs, id: \.self) { pr in
                        if let url = prLink(pr) { Link(destination: url) { Text("PR #\(pr)") }.buttonStyle(DButtonStyle()) }
                    }
                }.padding(.top, 10)
            }
            if let finding = pipeline.findings.first {
                watchdog(finding)
            } else if let note = pipeline.rowNote {
                DSection {
                    Text(pipeline.status).font(DS.h2).foregroundStyle(pipeline.group == .attention ? DS.warn : DS.fg)
                    Text(note).font(DS.sub).foregroundStyle(DS.dim).textSelection(.enabled).fixedSize(horizontal: false, vertical: true).padding(.top, 4)
                }
            }
            DSection(rule: false) {
                DLabel(text: "阶段", trailing: "\(pipeline.jobs.count) 个 Job", top: 0)
                ForEach(pipeline.stages) { stage in stageView(stage) }
                Text("这里只显示 Job 的执行结果。CI 和合并状态快照里没有上报，已结束不代表已通过。")
                    .font(DS.small).foregroundStyle(DS.dim).fixedSize(horizontal: false, vertical: true).padding(.top, 6)
            }
        }
    }
    private func watchdog(_ finding: Finding) -> some View {
        DSection {
            DHeader(title: Text(finding.text).foregroundStyle(DS.warn)) { Text("Watchdog · 只观察") }
            if let share = finding.withinThresholdShare {
                GeometryReader { proxy in
                    HStack(spacing: 2) {
                        TrackSegment(color: DS.warn).frame(width: max(0, (proxy.size.width - 2) * share))
                        if share < 1 { TrackSegment(color: DS.bad) }
                    }
                }
                .frame(height: 7).padding(.top, 9).padding(.bottom, 7)
                .accessibilityElement(children: .ignore)
                .accessibilityLabel("已超过 \(finding.thresholdMinutes) 分钟阈值：\(finding.text)")
                .help("橙色为 \(finding.thresholdMinutes) 分钟阈值内，红色为超出部分")
            }
            if let reason = pipeline.jobs.first(where: { $0.id == finding.taskId })?.waitingReason, !reason.isEmpty {
                Text(reason).font(DS.sub).foregroundStyle(DS.dim).textSelection(.enabled).fixedSize(horizontal: false, vertical: true)
            }
        }
    }
    private func stageView(_ stage: PipelineStage) -> some View {
        VStack(alignment: .leading, spacing: 0) {
            HStack(alignment: .firstTextBaseline) {
                Text(stage.title).font(.system(size: 14, weight: .semibold))
                Spacer()
                Text(stage.state.text).font(DS.small).foregroundStyle(stage.state.tone.textColor)
            }
            if let detail = stage.detail {
                Text(detail).font(DS.sub).foregroundStyle(DS.dim).fixedSize(horizontal: false, vertical: true)
            }
            ForEach(stage.jobs) { job in jobRow(job) }
        }
        .padding(.vertical, 9)
        .accessibilityElement(children: .contain)
        .accessibilityLabel(stage.accessibilityLabel)
    }
    private func jobRow(_ job: TaskRow) -> some View {
        let state = IssuePipeline.jobState(job).0
        return RowButton(action: { open(job.id) }) {
            Grid(alignment: .leading, horizontalSpacing: 12, verticalSpacing: 2) {
                GridRow {
                    Text("第 \(job.attempt + 1) 次").fontWeight(.medium).frame(maxWidth: .infinity, alignment: .leading)
                    TaskElapsedText(row: job, connected: model.summary.connected, frozenAt: now).fontWeight(.semibold).gridColumnAlignment(.trailing)
                }
                GridRow {
                    Text("\(job.worker.isEmpty ? "未分配" : job.worker) · \(relativeAge(job.updatedAt, now: now))")
                        .font(DS.small).foregroundStyle(DS.dim).lineLimit(1)
                    Text(state.text).font(DS.small).foregroundStyle(state.tone.textColor)
                }
            }.font(.system(size: 13)).monospacedDigit()
        }
        .help("查看这次 Job 的执行详情")
        .accessibilityLabel("第 \(job.attempt + 1) 次 \(job.kindZh)，\(state.text)，\(job.worker.isEmpty ? "未分配" : job.worker)")
    }
}
