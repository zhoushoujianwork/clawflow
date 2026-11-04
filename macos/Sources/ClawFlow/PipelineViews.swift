import SwiftUI

/// The one tone-to-color mapping for pipelines, stages and jobs. Finished is neutral, never green.
enum PipelineTone {
    case neutral, muted, running, attention, failed
    var color: Color {
        switch self {
        case .neutral: return .secondary
        case .muted: return Color(nsColor: .tertiaryLabelColor)
        case .running: return .blue
        case .attention: return .orange
        case .failed: return .red
        }
    }
}
extension StageState {
    var tone: PipelineTone {
        switch self {
        case .finished, .waiting, .pending: return .neutral
        case .notReported: return .muted
        case .running: return .running
        case .attention: return .attention
        case .failed: return .failed
        }
    }
    var symbol: String {
        switch self {
        case .pending: return "circle"
        case .waiting: return "clock"
        case .running: return "arrow.triangle.2.circlepath.circle"
        case .finished: return "checkmark.circle"
        case .failed: return "xmark.circle.fill"
        case .attention: return "exclamationmark.circle.fill"
        case .notReported: return "circle.dashed"
        }
    }
}
extension IssuePipeline {
    var stageState: StageState {
        switch group {
        case .attention: return .attention
        case .running: return .running
        case .waiting: return .waiting
        case .history: return jobs.first?.state == "failed" ? .failed : .finished
        }
    }
}

struct StageGlyph: View {
    let state: StageState
    let connected: Bool
    var body: some View {
        Group {
            if state == .running && connected { ProgressView().controlSize(.mini) }
            else { Image(systemName: state.symbol).foregroundStyle(state.tone.color) }
        }.frame(width: 16, height: 16)
    }
}

private enum PipelineColumns {
    static let status: CGFloat = 130, graph: CGFloat = 250, worker: CGFloat = 100, age: CGFloat = 80
    static let spacing: CGFloat = 12, inset: CGFloat = 16
    static func showMeta(_ width: CGFloat) -> Bool { width >= 760 }
    static func showGraph(_ width: CGFloat) -> Bool { width >= 560 }
}

struct PipelinesView: View {
    @ObservedObject var model: DashboardModel
    let snapshot: DashboardSnapshot
    let search: String
    @Binding var projectFilter: String
    @State private var showAll = false
    @State private var selected: IssuePipeline.Identity?
    private var rows: [IssuePipeline] {
        let repos = snapshot.projects.first { $0.name == projectFilter }?.repos.map(\.name)
        return snapshot.issuePipelines.filter {
            (showAll || $0.group != .history) &&
            (projectFilter.isEmpty || repos?.contains($0.repo) == true) &&
            (search.isEmpty || "\($0.repo) \($0.reference) \($0.title)".localizedCaseInsensitiveContains(search))
        }
    }
    var body: some View {
        let rows = rows
        VStack(spacing: 0) {
            topBar(rows)
            Divider()
            HStack(spacing: 0) {
                GeometryReader { proxy in
                    PipelineList(rows: rows, selected: $selected, connected: model.summary.connected,
                                 frozenAt: snapshot.board.generatedAt, width: proxy.size.width)
                }.frame(minWidth: 380)
                if let selected {
                    Divider()
                    PipelinePane(model: model, snapshot: snapshot, identity: selected) { self.selected = nil }
                        .id(selected)
                        .frame(minWidth: 360, idealWidth: 420, maxWidth: 560)
                }
            }
        }
    }
    private func topBar(_ rows: [IssuePipeline]) -> some View {
        let attention = rows.filter { $0.group == .attention }.count
        let running = rows.filter { $0.group == .running }.count
        let waiting = rows.filter { $0.group == .waiting }.count
        return HStack(spacing: 12) {
            Picker("Pipelines", selection: $showAll) { Text("Active").tag(false); Text("All").tag(true) }
                .labelsHidden().pickerStyle(.segmented).frame(width: 140)
            Picker("Project", selection: $projectFilter) {
                Text("All projects").tag("")
                ForEach(snapshot.projects) { Text($0.name).tag($0.name) }
            }.labelsHidden().frame(maxWidth: 200)
            Spacer(minLength: 8)
            VStack(alignment: .trailing, spacing: 2) {
                Text("\(attention) needs attention · \(running) running · \(waiting) waiting")
                    .foregroundStyle(attention > 0 ? AnyShapeStyle(PipelineTone.attention.color) : AnyShapeStyle(.secondary))
                if let throughput = snapshot.board.throughput {
                    Text(throughputLabel(throughput, generatedAt: snapshot.board.generatedAt)).foregroundStyle(.secondary)
                }
            }.font(.caption).lineLimit(1)
        }.controlSize(.regular).padding(.horizontal, 20).frame(height: 56)
    }
}

struct PipelineList: View {
    let rows: [IssuePipeline]
    @Binding var selected: IssuePipeline.Identity?
    let connected: Bool
    let frozenAt: Date
    let width: CGFloat
    var body: some View {
        VStack(spacing: 0) {
            PipelineColumnHeader(width: width)
            Divider()
            List(selection: $selected) {
                ForEach([TaskGroup.attention, .running, .waiting, .history], id: \.rawValue) { group in
                    let members = rows.filter { $0.group == group }
                    if !members.isEmpty {
                        Section {
                            ForEach(members) { row in
                                PipelineRow(row: row, connected: connected, frozenAt: frozenAt, width: width)
                                    .tag(row.id)
                                    .listRowSeparator(.visible)
                                    .listRowInsets(EdgeInsets(top: 0, leading: PipelineColumns.inset, bottom: 0, trailing: PipelineColumns.inset))
                            }
                        } header: { SectionHeader(title: group.title, count: members.count) }
                    }
                }
            }
            .listStyle(.plain)
            .alternatingRowBackgrounds(.disabled)
            .onExitCommand { selected = nil }
            .overlay { if rows.isEmpty { DashboardEmptyState(title: "No matching pipelines", symbol: "point.3.connected.trianglepath.dotted", detail: "Each known issue appears once. Switch to All to include finished pipelines.") } }
        }
    }
}

struct PipelineColumnHeader: View {
    let width: CGFloat
    var body: some View {
        HStack(spacing: PipelineColumns.spacing) {
            Text("Status").frame(width: PipelineColumns.status, alignment: .leading)
            Text("Issue").frame(maxWidth: .infinity, alignment: .leading)
            if PipelineColumns.showGraph(width) { Text("Stages").frame(width: PipelineColumns.graph, alignment: .leading) }
            if PipelineColumns.showMeta(width) {
                Text("Worker").frame(width: PipelineColumns.worker, alignment: .leading)
                Text("Age").frame(width: PipelineColumns.age, alignment: .trailing)
            }
        }
        .font(.caption.weight(.medium)).foregroundStyle(.secondary)
        // List rows add their own inset; match it so header and cells align.
        .padding(.horizontal, PipelineColumns.inset + 8).frame(height: 28)
    }
}

struct PipelineRow: View {
    let row: IssuePipeline
    let connected: Bool
    let frozenAt: Date
    let width: CGFloat
    private var secondary: String {
        "\(row.repoShortName) \(row.reference)" + (row.prs.isEmpty ? "" : " · PR " + row.prs.map { "#\($0)" }.joined(separator: ", "))
    }
    var body: some View {
        HStack(spacing: PipelineColumns.spacing) {
            HStack(spacing: 6) {
                StageGlyph(state: row.stageState, connected: connected)
                Text(row.status).lineLimit(1)
            }.frame(width: PipelineColumns.status, alignment: .leading)
                .accessibilityElement(children: .combine)
            VStack(alignment: .leading, spacing: 2) {
                Text(row.title).fontWeight(.medium).lineLimit(1).help(row.title)
                HStack(spacing: 0) {
                    Text(secondary).foregroundStyle(.secondary).layoutPriority(1)
                    if let note = row.rowNote {
                        Text(" · " + note).foregroundStyle(row.group == .attention ? PipelineTone.attention.color : Color.secondary).truncationMode(.tail)
                    }
                }.font(.caption).lineLimit(1).help(row.reason)
            }.frame(maxWidth: .infinity, alignment: .leading)
            if PipelineColumns.showGraph(width) {
                StageGraph(stages: row.stages, connected: connected).frame(width: PipelineColumns.graph, alignment: .leading)
            }
            if PipelineColumns.showMeta(width) {
                Text(row.jobs.first.map { $0.worker.isEmpty ? "—" : $0.worker } ?? "—")
                    .foregroundStyle(.secondary).lineLimit(1).frame(width: PipelineColumns.worker, alignment: .leading)
                Group {
                    if let running = row.runningJob {
                        TaskElapsedText(row: running, connected: connected, frozenAt: frozenAt, unavailable: "—")
                    } else {
                        Text(row.jobs.isEmpty ? "—" : relativeAge(row.updatedAt, now: frozenAt))
                    }
                }.font(.caption.monospacedDigit()).foregroundStyle(.secondary).lineLimit(1)
                    .frame(width: PipelineColumns.age, alignment: .trailing)
            }
        }
        .frame(minHeight: 48)
        .contentShape(Rectangle())
    }
}

struct StageGraph: View {
    let stages: [PipelineStage]
    let connected: Bool
    var body: some View {
        HStack(alignment: .top, spacing: 0) {
            ForEach(Array(stages.enumerated()), id: \.element.id) { index, stage in
                if index > 0 {
                    Rectangle().fill(Color(nsColor: .separatorColor)).frame(width: 14, height: 1).padding(.top, 8)
                }
                VStack(spacing: 2) {
                    StageGlyph(state: stage.state, connected: connected)
                    Text(stage.title).font(.caption2).foregroundStyle(.secondary).lineLimit(1).fixedSize()
                }
                .help(stage.accessibilityLabel + (stage.detail.map { " \($0)" } ?? ""))
                .accessibilityElement(children: .ignore)
                .accessibilityLabel(stage.accessibilityLabel)
            }
        }
    }
}

/// Inline trailing detail. Looks the pipeline up by identity on each snapshot, so it survives refreshes and completion.
struct PipelinePane: View {
    @ObservedObject var model: DashboardModel
    let snapshot: DashboardSnapshot
    let identity: IssuePipeline.Identity
    let close: () -> Void
    @State private var job: String?
    var body: some View {
        let pipeline = snapshot.issuePipelines.first { $0.id == identity }
        VStack(spacing: 0) {
            HStack(spacing: 8) {
                if job != nil {
                    Button { job = nil } label: { Label("Back", systemImage: "chevron.left") }.buttonStyle(.borderless)
                }
                Text(job != nil ? "Job" : "Pipeline").font(.headline)
                Spacer()
                Button(action: close) { Image(systemName: "xmark") }
                    .buttonStyle(.borderless).keyboardShortcut(.cancelAction).help("Close details").accessibilityLabel("Close details")
            }.padding(.horizontal, 16).frame(height: 56)
            Divider()
            if let pipeline {
                if let job, let row = pipeline.jobs.first(where: { $0.id == job }) {
                    TaskDetail(model: model, snapshot: snapshot, row: row).id(row.id)
                } else {
                    PipelineDetail(model: model, snapshot: snapshot, pipeline: pipeline) { job = $0 }
                }
            } else {
                DashboardEmptyState(title: "Pipeline unavailable", symbol: "point.3.connected.trianglepath.dotted", detail: "This pipeline is no longer in the controller snapshot.")
                Button("Close", action: close).padding(.bottom, 24)
            }
        }
        .background(Color(nsColor: .windowBackgroundColor))
    }
}

struct PipelineDetail: View {
    @ObservedObject var model: DashboardModel
    let snapshot: DashboardSnapshot
    let pipeline: IssuePipeline
    let open: (String) -> Void
    private var issueLink: URL? {
        guard pipeline.issue > 0, let repo = snapshot.repository(pipeline.repo), let base = safeWebURL(repo.url) else { return nil }
        return base.appendingPathComponent(repo.platform == "gitlab" ? "-/issues/\(pipeline.issue)" : "issues/\(pipeline.issue)")
    }
    private func prLink(_ pr: Int) -> URL? {
        guard let repo = snapshot.repository(pipeline.repo), let base = safeWebURL(repo.url) else { return nil }
        return base.appendingPathComponent(repo.platform == "gitlab" ? "-/merge_requests/\(pr)" : "pull/\(pr)")
    }
    private var reason: String {
        pipeline.findings.isEmpty ? pipeline.reason : "Watchdog (observe only): " + pipeline.reason.replacingOccurrences(of: "Watchdog: ", with: "")
    }
    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 14) {
                Text(pipeline.title).font(.title3.weight(.semibold)).textSelection(.enabled).fixedSize(horizontal: false, vertical: true)
                HStack(spacing: 8) {
                    Text("\(pipeline.repo) \(pipeline.reference)").foregroundStyle(.secondary).lineLimit(1).textSelection(.enabled)
                    Spacer(minLength: 4)
                }
                HStack(spacing: 8) {
                    if let issueLink { Link("Open issue", destination: issueLink) }
                    ForEach(pipeline.prs, id: \.self) { pr in if let url = prLink(pr) { Link("Open PR #\(pr)", destination: url) } }
                }.buttonStyle(.bordered).controlSize(.small)
                VStack(alignment: .leading, spacing: 4) {
                    HStack(spacing: 6) {
                        StageGlyph(state: pipeline.stageState, connected: model.summary.connected)
                        Text(pipeline.status).fontWeight(.medium)
                    }
                    Text(reason).font(.callout)
                        .foregroundStyle(pipeline.group == .attention ? AnyShapeStyle(PipelineTone.attention.color) : AnyShapeStyle(.secondary))
                        .textSelection(.enabled).fixedSize(horizontal: false, vertical: true)
                }
                Divider()
                VStack(alignment: .leading, spacing: 0) {
                    ForEach(Array(pipeline.stages.enumerated()), id: \.element.id) { index, stage in
                        stageView(stage, last: index == pipeline.stages.count - 1)
                    }
                }
                Text("CI and merge status are not reported in this snapshot. Earlier attempts may be unavailable.")
                    .font(.caption).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
            }.padding(16).frame(maxWidth: .infinity, alignment: .leading)
        }
    }
    private func stageView(_ stage: PipelineStage, last: Bool) -> some View {
        HStack(alignment: .top, spacing: 10) {
            VStack(spacing: 0) {
                StageGlyph(state: stage.state, connected: model.summary.connected)
                if !last { Rectangle().fill(Color(nsColor: .separatorColor)).frame(width: 1).frame(maxHeight: .infinity) }
            }.frame(width: 16)
            VStack(alignment: .leading, spacing: 6) {
                HStack(spacing: 6) {
                    Text(stage.title).fontWeight(.medium)
                    Text(stage.state.text.capitalizedFirst).font(.caption).foregroundStyle(stage.state.tone.color)
                }
                if let detail = stage.detail { Text(detail).font(.caption).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true) }
                ForEach(stage.jobs) { job in jobButton(job) }
            }.padding(.bottom, 14)
            Spacer(minLength: 0)
        }
        .accessibilityElement(children: .contain)
        .accessibilityLabel(stage.accessibilityLabel)
        .help(stage.accessibilityLabel)
    }
    private func jobButton(_ job: TaskRow) -> some View {
        Button { open(job.id) } label: {
            HStack(spacing: 8) {
                Image(systemName: job.activity.symbol).foregroundStyle(.secondary)
                Text(job.activity.title).lineLimit(1)
                Text("Attempt \(job.attempt + 1)").monospacedDigit().foregroundStyle(.secondary)
                Text(job.worker.isEmpty ? "Unassigned" : job.worker).foregroundStyle(.secondary).lineLimit(1)
                Spacer(minLength: 4)
                TaskElapsedText(row: job, connected: model.summary.connected, frozenAt: snapshot.board.generatedAt, unavailable: "—")
                    .monospacedDigit().foregroundStyle(.secondary)
                Image(systemName: "chevron.right").foregroundStyle(.tertiary)
            }.font(.caption).padding(.horizontal, 8).padding(.vertical, 6)
                .frame(maxWidth: .infinity, alignment: .leading)
                .background(.quaternary.opacity(0.5), in: RoundedRectangle(cornerRadius: 6))
                .contentShape(Rectangle())
        }.buttonStyle(.plain).help("Open this job's execution details")
    }
}
