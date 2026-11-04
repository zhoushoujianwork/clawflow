import SwiftUI

enum Destination: String, CaseIterable, Identifiable {
    case pipelines = "Pipelines", workers = "Workers", projects = "Projects"
    var id: String { rawValue }
    var symbol: String { switch self { case .pipelines: return "point.3.connected.trianglepath.dotted"; case .workers: return "desktopcomputer"; case .projects: return "folder" } }
    /// Persisted values from older builds (for example "Issue Queue" or "Tasks") fall back to Pipelines.
    static func restore(_ raw: String) -> Destination { Destination(rawValue: raw) ?? .pipelines }
    func badge(in snapshot: DashboardSnapshot?) -> String? {
        guard let snapshot else { return nil }
        switch self {
        case .pipelines: return snapshot.activePipelineCount > 0 ? "\(snapshot.activePipelineCount)" : nil
        case .workers: return "\(snapshot.board.workers.filter(\.live).count)/\(snapshot.board.workers.count)"
        case .projects: return "\(snapshot.uniqueProjects.flatMap(\.repos).filter(\.enabled).count)"
        }
    }
}

struct DashboardView: View {
    @ObservedObject var model: DashboardModel
    @AppStorage("dashboard.destination") private var storedDestination = Destination.pipelines.rawValue
    @State private var search = ""
    @State private var projectFilter = ""
    private var destination: Destination { Destination.restore(storedDestination) }
    private var selection: Binding<Destination?> {
        Binding(get: { destination }, set: { if let next = $0 { storedDestination = next.rawValue } })
    }
    var body: some View {
        HStack(spacing: 0) {
            VStack(spacing: 8) {
                HStack(spacing: 8) {
                    Image(nsImage: BrandMark.menuBar)
                    Text("ClawFlow").font(.headline)
                    Spacer()
                }.padding(.horizontal, 16).padding(.top, 18).padding(.bottom, 8)
                List(Destination.allCases, selection: selection) { item in
                    Label(item.rawValue, systemImage: item.symbol)
                        .badge(item.badge(in: model.snapshot).map { Text($0) })
                        .padding(.vertical, 6).tag(item)
                }.listStyle(.sidebar)
                SettingsLink().controlSize(.regular).padding()
            }.frame(width: 200).background(.bar)
            Divider()
            VStack(spacing: 0) {
                HStack {
                    Label(model.summary.title, systemImage: model.summary.symbol)
                    Spacer()
                    Text(model.snapshot?.controller ?? "Connect in Settings").foregroundStyle(.secondary)
                }.font(.callout).padding(.horizontal, 18).padding(.vertical, 12).fixedSize(horizontal: false, vertical: true)
                Divider()
                if let error = model.error {
                    Text(error).font(.caption).foregroundStyle(.orange).textSelection(.enabled).padding(10)
                }
                if let error = model.actionError {
                    HStack { Text(error).foregroundStyle(.orange); Button("Dismiss") { model.actionError = nil } }.font(.caption).padding(10)
                }
                if let snapshot = model.snapshot {
                    switch destination {
                    case .pipelines: PipelinesView(model: model, snapshot: snapshot, search: search, projectFilter: $projectFilter)
                    case .workers: WorkersView(model: model, snapshot: snapshot, search: search)
                    case .projects: ProjectsView(model: model, snapshot: snapshot, search: search) { name in
                        projectFilter = name; storedDestination = Destination.pipelines.rawValue; search = ""
                    }
                    }
                } else {
                    ContentUnavailableView {
                        Label("Connect to ClawFlow", systemImage: "network")
                    } description: {
                        Text("Choose a controller in Settings. Tasks and LAN workers keep running when this app is closed.")
                    } actions: { SettingsLink().buttonStyle(.borderedProminent) }
                }
                Divider()
                HStack {
                    if let received = model.received { Text("Last received \(received, style: .relative) ago") }
                    else { Text("No status received") }
                    Spacer()
                    Text(model.snapshot.map { "Controller \($0.version)" } ?? "")
                }.font(.caption).foregroundStyle(.secondary).padding(8)
            }
        }
        .searchable(text: $search, prompt: "Search \(destination.rawValue.lowercased())")
        .toolbar {
            Button { Task { await model.refresh() } } label: { Label("Refresh", systemImage: "arrow.clockwise") }.keyboardShortcut("r")
        }
        .frame(minWidth: 1040, minHeight: 680)
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
    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 14) {
                HStack(spacing: 8) {
                    ActivityBadge(activity: row.activity)
                    TaskStateLabel(row: row, connected: model.summary.connected).font(.callout.weight(.medium))
                }
                Text(row.displayTitle).font(.headline).textSelection(.enabled).fixedSize(horizontal: false, vertical: true)
                Text(row.reason).font(.callout).textSelection(.enabled).fixedSize(horizontal: false, vertical: true)
                FactGrid {
                    Fact("Repository", "\(row.repo) \(row.reference)")
                    Fact("Worker", row.worker.isEmpty ? "Unassigned" : row.worker)
                    if row.group == .running || row.startedAt != nil {
                        Fact("Elapsed") { TaskElapsedText(row: row, connected: model.summary.connected, frozenAt: snapshot.board.generatedAt).monospacedDigit() }
                    }
                    Fact("Attempt", "\(row.attempt + 1)")
                    Fact("Last update", relativeAge(row.updatedAt, now: snapshot.board.generatedAt))
                    if !row.tags.isEmpty { Fact("Required tags", row.tags.joined(separator: ", ")) }
                }.font(.callout)
                HStack(spacing: 8) {
                    if let link = row.link(in: snapshot) { Link(row.issue > 0 ? "Open issue" : "Open PR", destination: link) }
                    if row.issue > 0, let pr = row.prLink(in: snapshot) { Link("Open PR #\(row.pr)", destination: pr) }
                }.buttonStyle(.bordered).controlSize(.regular)
                Divider()
                Label("Full Offload execution output is not available in this build.", systemImage: "terminal")
                    .font(.callout).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
                DisclosureGroup("Coordinator activity", isExpanded: $showEvents) {
                    VStack(alignment: .leading, spacing: 8) {
                        Text("Scheduling events · refreshes every 5 seconds while connected.").font(.caption).foregroundStyle(.secondary)
                        if loading { ProgressView().controlSize(.small) }
                        if let eventError { Text(eventError).foregroundStyle(.orange) }
                        ForEach(Array(events.enumerated()), id: \.offset) { _, event in
                            HStack(alignment: .top) { Text(event.at, style: .time).foregroundStyle(.secondary); Text(event.action).textSelection(.enabled) }.font(.caption.monospaced())
                        }
                        Button("Refresh activity") { Task { await loadEvents() } }.disabled(!model.summary.connected || loading)
                    }.frame(maxWidth: .infinity, alignment: .leading).padding(.top, 8)
                }.onChange(of: showEvents) { _, expanded in if expanded { Task { await loadEvents() } } }
                Text("Task ID: \(row.id)").font(.caption).foregroundStyle(.tertiary).textSelection(.enabled)
            }.padding(16).frame(maxWidth: .infinity, alignment: .leading)
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

struct ActivityBadge: View {
    let activity: TaskActivity
    var body: some View {
        Label(activity.title, systemImage: activity.symbol)
            .font(.caption.weight(.medium))
            .padding(.horizontal, 7).padding(.vertical, 3)
            .background(.quaternary, in: Capsule())
    }
}

/// Aligned facts for a narrow detail pane.
struct FactGrid<Content: View>: View {
    @ViewBuilder let content: Content
    var body: some View {
        Grid(alignment: .leading, horizontalSpacing: 12, verticalSpacing: 6) { content }
    }
}
struct Fact<Value: View>: View {
    let label: String
    @ViewBuilder let value: Value
    init(_ label: String, @ViewBuilder value: () -> Value) { self.label = label; self.value = value() }
    var body: some View {
        GridRow(alignment: .firstTextBaseline) {
            Text(label).foregroundStyle(.secondary).gridColumnAlignment(.trailing)
            value.textSelection(.enabled).frame(maxWidth: .infinity, alignment: .leading)
        }
    }
}
extension Fact where Value == Text {
    init(_ label: String, _ text: String) { self.init(label) { Text(text) } }
}

struct Pill: View {
    let text: String
    var tint: Color = .secondary
    var body: some View {
        Text(text).font(.caption).foregroundStyle(tint)
            .padding(.horizontal, 7).padding(.vertical, 2)
            .background(tint.opacity(0.14), in: Capsule())
            .fixedSize()
    }
}

/// One section header style for Pipelines, Workers and Projects.
struct SectionHeader: View {
    let title: String
    var count: Int?
    var body: some View {
        HStack(spacing: 6) {
            Text(title).font(.subheadline.weight(.semibold))
            if let count { Text("\(count)").font(.subheadline.monospacedDigit()).foregroundStyle(.secondary) }
            Spacer()
        }
    }
}

struct DashboardEmptyState: View {
    let title: String
    let symbol: String
    let detail: String
    var body: some View {
        VStack(spacing: 12) {
            Image(systemName: symbol).font(.system(size: 28)).foregroundStyle(.tertiary)
            Text(title).font(.headline)
            Text(detail).font(.callout).foregroundStyle(.secondary).multilineTextAlignment(.center)
        }.padding(24).frame(maxWidth: .infinity, maxHeight: .infinity)
    }
}

struct TaskStateLabel: View {
    let row: TaskRow
    let connected: Bool
    var body: some View {
        let state = IssuePipeline.jobState(row).0
        HStack(spacing: 6) {
            StageGlyph(state: state, connected: connected)
            Text(row.status).lineLimit(1)
        }.help(connected ? row.reason : "Last known status · connection unavailable")
    }
}

struct WorkersView: View {
    @ObservedObject var model: DashboardModel
    let snapshot: DashboardSnapshot
    let search: String
    @State private var selected: String?
    @State private var adding = false
    @State private var removing = false
    private var rows: [WorkerRow] { snapshot.board.workers.filter { search.isEmpty || "\($0.name) \($0.tags.joined(separator: " "))".localizedCaseInsensitiveContains(search) } }
    private var current: WorkerRow? { rows.first { $0.id == selected } ?? rows.first }
    private var selection: Binding<String?> { Binding(get: { current?.id }, set: { selected = $0 }) }
    private func availabilityTint(_ row: WorkerRow) -> Color { row.live && !row.paused ? .secondary : .orange }
    private func references(_ row: WorkerRow) -> String {
        snapshot.board.tasks.filter { $0.worker == row.name && $0.group != .history }.map { "\($0.repoShortName) \($0.reference)" }.joined(separator: ", ")
    }
    var body: some View {
        VStack(spacing: 0) {
            HStack {
                Text("\(snapshot.board.workers.filter(\.live).count) of \(snapshot.board.workers.count) online").foregroundStyle(.secondary)
                Spacer()
                Button("Add Worker") { adding = true }.disabled(!model.summary.connected || model.busy)
            }.controlSize(.regular).padding(.horizontal, 20).frame(height: 56)
            Divider()
            HStack(spacing: 0) {
                List(selection: selection) {
                    Section { ForEach(rows) { row in
                        VStack(alignment: .leading, spacing: 4) {
                            HStack {
                                Label(row.name, systemImage: row.live ? "desktopcomputer" : "wifi.slash").fontWeight(.medium).lineLimit(1)
                                Spacer()
                                Text("\(row.runningTasks.count)/\(row.maxParallel)").font(.caption.monospacedDigit()).foregroundStyle(.secondary)
                            }
                            HStack(spacing: 6) {
                                Pill(text: row.state, tint: availabilityTint(row))
                                Text(row.roles.joined(separator: ", ")).font(.caption).foregroundStyle(.secondary).lineLimit(1)
                            }
                        }.padding(.vertical, 4).frame(minHeight: 48).tag(row.id)
                    } } header: { SectionHeader(title: "Workers", count: rows.count) }
                }.frame(width: 320).frame(maxHeight: .infinity)
                    .overlay { if rows.isEmpty { DashboardEmptyState(title: "No workers", symbol: "desktopcomputer", detail: "Add a worker to run tasks.") } }
                Divider()
                if let row = current { detail(row) }
                else { Color.clear.frame(maxWidth: .infinity, maxHeight: .infinity) }
            }.frame(maxWidth: .infinity, maxHeight: .infinity)
        }.sheet(isPresented: $adding) { AddWorkerView(model: model) }
    }
    private func detail(_ row: WorkerRow) -> some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 16) {
                HStack(spacing: 8) {
                    Text(row.name).font(.title3.weight(.semibold)).textSelection(.enabled)
                    Pill(text: row.state, tint: availabilityTint(row))
                }
                FactGrid {
                    Fact("Capacity", "\(row.runningTasks.count)/\(row.maxParallel) running")
                    Fact("Roles", row.roles.isEmpty ? "—" : row.roles.joined(separator: ", "))
                    Fact("Tags", row.tags.isEmpty ? "No project tags" : row.tags.joined(separator: ", "))
                    Fact("Last contact", row.lastSeen.map { relativeAge($0, now: snapshot.board.generatedAt) } ?? "Unavailable")
                    let refs = references(row)
                    Fact("Running", refs.isEmpty ? "—" : refs)
                }.font(.callout)
                Divider()
                HStack {
                    Button(row.paused ? "Resume assignments" : "Stop new assignments") { Task { await model.perform(row.paused ? "resume" : "drain", name: row.name) } }
                        .disabled(!model.summary.connected || model.busy || row.revoked)
                    Button("Remove worker", role: .destructive) { removing = true }
                        .disabled(!model.summary.connected || model.busy || row.revoked || !row.paused || snapshot.board.tasks.contains { $0.worker == row.name && $0.state == "leased" })
                        .confirmationDialog("Revoke access for \(row.name)?", isPresented: $removing) {
                            Button("Remove worker", role: .destructive) { Task { await model.perform("remove", name: row.name) } }
                        } message: { Text("Historical tasks remain. This worker credential will no longer authenticate.") }
                }
                Text("Stopping assignments lets active tasks finish. Eligibility follows repository and worker tags; review tags before resuming a new worker.")
                    .font(.caption).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
            }.padding(20).frame(maxWidth: 640, alignment: .leading).frame(maxWidth: .infinity, alignment: .leading)
        }
    }
}

struct ProjectsView: View {
    @ObservedObject var model: DashboardModel
    let snapshot: DashboardSnapshot
    let search: String
    let showPipelines: (String) -> Void
    private var activeByRepo: [String: Int] {
        snapshot.issuePipelines.filter { $0.group != .history }.reduce(into: [:]) { $0[$1.repo, default: 0] += 1 }
    }
    private var projects: [ProjectRow] {
        snapshot.uniqueProjects.filter { search.isEmpty || $0.name.localizedCaseInsensitiveContains(search) || $0.repos.contains { $0.name.localizedCaseInsensitiveContains(search) } }
    }
    var body: some View {
        let active = activeByRepo
        let idle = projects.flatMap(\.repos).filter { (active[$0.name] ?? 0) == 0 && $0.queueError == nil }
        VStack(spacing: 0) {
            HStack {
                Text("\(snapshot.uniqueProjects.flatMap(\.repos).filter(\.enabled).count) repositories monitored").foregroundStyle(.secondary)
                Spacer()
            }.padding(.horizontal, 20).frame(height: 56)
            Divider()
            List {
                ForEach(projects) { project in
                    let busy = project.repos.filter { (active[$0.name] ?? 0) > 0 || $0.queueError != nil }
                    if !busy.isEmpty {
                        Section {
                            ForEach(busy) { repo in line(repo, active: active[repo.name] ?? 0) }
                        } header: {
                            HStack {
                                SectionHeader(title: project.name)
                                Button("View pipelines") { showPipelines(project.name) }.buttonStyle(.plain).foregroundStyle(.secondary).font(.caption)
                            }
                        }
                    }
                }
                if !idle.isEmpty {
                    Section {
                        ForEach(idle) { repo in line(repo, active: 0) }
                    } header: { SectionHeader(title: "Nothing active", count: idle.count) }
                }
            }.overlay { if projects.isEmpty { DashboardEmptyState(title: "No matching projects", symbol: "folder", detail: "Enable a repository with the clawflow CLI.") } }
        }
    }
    private func line(_ repo: RepoRow, active: Int) -> some View {
        VStack(alignment: .leading, spacing: 3) {
            HStack(spacing: 8) {
                Text(repo.name).fontWeight(.medium).foregroundStyle(repo.enabled ? .primary : .secondary).lineLimit(1).help(repo.name)
                Spacer(minLength: 8)
                Text(active > 0 ? "\(active) active" : repo.idleNote).font(.caption).foregroundStyle(.secondary).lineLimit(1)
                if let skipped = repo.skippedIssues, skipped > 0 { Text("· \(skipped) skipped").font(.caption).foregroundStyle(.secondary) }
                Pill(text: "CI \(repo.ciRequired ? "required" : "optional")", tint: repo.ciRequired ? .blue : .secondary)
                Pill(text: "Acceptance \(repo.acceptanceRequired ? "required" : "optional")", tint: repo.acceptanceRequired ? .blue : .secondary)
                Pill(text: "Auto-merge \(repo.autoMerge ? "on" : "off")", tint: repo.autoMerge ? .blue : .secondary)
                Button(repo.enabled ? "Pause" : "Resume") { Task { await model.perform(repo.enabled ? "disable" : "enable", name: repo.name) } }
                    .disabled(!model.summary.connected || model.busy)
            }
            if let error = repo.queueError { Text(error).font(.caption).foregroundStyle(.orange).lineLimit(2).textSelection(.enabled) }
            if !repo.workerTags.isEmpty { Text("Worker tags: " + repo.workerTags.joined(separator: ", ")).font(.caption).foregroundStyle(.secondary).lineLimit(1) }
        }.padding(.vertical, 4)
    }
}
