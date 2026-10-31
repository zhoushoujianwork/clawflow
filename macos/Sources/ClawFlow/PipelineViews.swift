import SwiftUI

struct PipelinesView: View {
    @ObservedObject var model: DashboardModel
    let snapshot: DashboardSnapshot
    let search: String
    @Binding var projectFilter: String
    @State private var activeOnly = false
    @State private var selected: IssuePipeline.Identity?
    private var rows: [IssuePipeline] {
        let repos = snapshot.projects.first { $0.name == projectFilter }?.repos.map(\.name)
        return snapshot.issuePipelines.filter {
            (!activeOnly || $0.group != .history) &&
            (projectFilter.isEmpty || repos?.contains($0.repo) == true) &&
            (search.isEmpty || "\($0.repo) \($0.reference) \($0.title)".localizedCaseInsensitiveContains(search))
        }
    }
    var body: some View {
        VStack(spacing: 0) {
            HStack {
                Picker("Pipelines", selection: $activeOnly) { Text("All").tag(false); Text("Active jobs / queue").tag(true) }.pickerStyle(.segmented).frame(width: 250)
                Text("\(rows.count) pipelines").font(.caption).foregroundStyle(.secondary)
                Spacer()
                Picker("Project", selection: $projectFilter) {
                    Text("All projects").tag("")
                    ForEach(snapshot.projects) { Text($0.name).tag($0.name) }
                }.frame(maxWidth: 260)
            }.controlSize(.regular).padding(.horizontal, 20).frame(height: 56)
            Divider()
            Table(rows, selection: $selected) {
                TableColumn("Pipeline activity") { row in PipelineState(row: row, connected: model.summary.connected) }.width(155)
                TableColumn("Issue / Pipeline") { row in
                    VStack(alignment: .leading, spacing: 4) {
                        Text(row.title).fontWeight(.medium).lineLimit(1).help(row.title)
                        Text("\(row.repo) \(row.reference)" + (row.prs.isEmpty ? "" : " · PR " + row.prs.map { "#\($0)" }.joined(separator: ", ")))
                            .font(.caption).foregroundStyle(.secondary).lineLimit(1)
                    }.frame(minHeight: 56)
                }.width(min: 280, ideal: 440)
                TableColumn("Stages · execution evidence") { row in
                    HStack(spacing: 12) {
                        PipelineStageMark(title: "Check", job: nil, connected: model.summary.connected)
                        Image(systemName: "chevron.right").foregroundStyle(.tertiary)
                        PipelineStageMark(title: "Implement", job: row.jobs(kind: "implement").first, connected: model.summary.connected)
                        Image(systemName: "chevron.right").foregroundStyle(.tertiary)
                        PipelineStageMark(title: "PR review", job: row.jobs(kind: "acceptance").first, connected: model.summary.connected)
                    }.font(.caption)
                }.width(320)
                TableColumn("Jobs") { row in Text("\(row.jobs.count)").monospacedDigit() }.width(50)
            }.tableStyle(.bordered).alternatingRowBackgrounds(.disabled)
                .overlay { if rows.isEmpty { DashboardEmptyState(title: "No matching pipelines", symbol: "point.3.connected.trianglepath.dotted", detail: "Each known issue appears once; open it to see stages and jobs.") } }
        }
        .sheet(isPresented: Binding(get: { selected != nil }, set: { if !$0 { selected = nil } })) {
            if let row = snapshot.issuePipelines.first(where: { $0.id == selected }) {
                VStack(spacing: 0) {
                    DetailHeader(title: "Pipeline · \(row.reference)") { selected = nil }
                    PipelineDetail(model: model, snapshot: snapshot, pipeline: row)
                }.frame(width: 960, height: 660)
            } else { DetailHeader(title: "Pipeline unavailable") { selected = nil } }
        }
    }
}

struct PipelineDetail: View {
    @ObservedObject var model: DashboardModel
    let snapshot: DashboardSnapshot
    let pipeline: IssuePipeline
    @State private var selectedJob: String?
    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 20) {
                Text(pipeline.title).font(.title3.weight(.semibold)).textSelection(.enabled)
                Text(pipeline.repo + " " + pipeline.reference).foregroundStyle(.secondary)
                PipelineState(row: pipeline, connected: model.summary.connected)
                Text(pipeline.reason).font(.callout).foregroundStyle(.secondary)
                Text("Execution evidence only · CI, acceptance verdict and merge status are not available in this snapshot.").font(.caption).foregroundStyle(.secondary)
                stage("Check", kind: nil)
                stage("Implementation", kind: "implement")
                stage("PR review", kind: "acceptance")
                if !pipeline.jobs(kind: "repair").isEmpty { stage("Repair / verification", kind: "repair") }
                if !pipeline.jobs(kind: "reply").isEmpty { stage("Issue reply", kind: "reply") }
                Text("Only jobs present in the controller snapshot are shown. Earlier attempts may be unavailable.").font(.caption).foregroundStyle(.secondary)
            }.padding(20).frame(maxWidth: .infinity, alignment: .leading)
        }
        .sheet(isPresented: Binding(get: { selectedJob != nil }, set: { if !$0 { selectedJob = nil } })) {
            if let job = pipeline.jobs.first(where: { $0.id == selectedJob }) {
                VStack(spacing: 0) {
                    DetailHeader(title: "Job · \(job.activity.title) · attempt \(job.attempt + 1)") { selectedJob = nil }
                    TaskDetail(model: model, snapshot: snapshot, row: job).id(job.id)
                }.frame(width: 760, height: 620)
            } else { DetailHeader(title: "Job unavailable") { selectedJob = nil } }
        }
    }
    private func stage(_ title: String, kind: String?) -> some View {
        let jobs = kind.map { pipeline.jobs(kind: $0) } ?? []
        return VStack(alignment: .leading, spacing: 10) {
            HStack { Text(title).font(.headline); Spacer(); Text("\(jobs.count) jobs").font(.caption).foregroundStyle(.secondary) }
            if jobs.isEmpty { Text("Not reported").font(.callout).foregroundStyle(.secondary) }
            ForEach(jobs) { job in
                Button { selectedJob = job.id } label: {
                    HStack(spacing: 16) {
                        TaskStateLabel(row: job, connected: model.summary.connected).frame(width: 155, alignment: .leading)
                        Text("Attempt \(job.attempt + 1)").monospacedDigit()
                        Text(job.worker.isEmpty ? "Unassigned" : job.worker).foregroundStyle(.secondary)
                        Spacer()
                        Text(job.durationAvailable ? job.elapsed(at: model.summary.connected ? model.now : snapshot.board.generatedAt) : "—").monospacedDigit()
                        Image(systemName: "chevron.right").foregroundStyle(.secondary)
                    }.font(.callout).padding(12).frame(maxWidth: .infinity, alignment: .leading)
                        .background(.quaternary, in: RoundedRectangle(cornerRadius: 6))
                }.buttonStyle(.plain).help("Open this job's execution details")
            }
            Divider()
        }
    }
}

struct PipelineState: View {
    let row: IssuePipeline
    let connected: Bool
    var body: some View {
        HStack(spacing: 8) {
            if row.group == .running && connected { ProgressView().controlSize(.small).frame(width: 16) }
            else { Image(systemName: row.symbol).foregroundStyle(row.group == .attention ? Color.orange : Color.secondary) }
            Text(row.status).lineLimit(1)
        }.help(row.reason)
    }
}

struct PipelineStageMark: View {
    let title: String
    let job: TaskRow?
    let connected: Bool
    var body: some View {
        VStack(spacing: 5) {
            if let job {
                if job.group == .running && connected { ProgressView().controlSize(.mini).frame(height: 14) }
                else { Image(systemName: job.symbol).foregroundStyle(job.state == "done" ? Color.green : job.state == "failed" || job.group == .attention ? Color.orange : Color.secondary).frame(height: 14) }
            } else { Image(systemName: "questionmark.circle").foregroundStyle(.tertiary).frame(height: 14) }
            Text(title).foregroundStyle(.secondary)
        }.help(job.map { "\(title): \($0.status). This is job execution status, not a delivery verdict." } ?? "\(title): not reported")
    }
}
