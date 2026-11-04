import SwiftUI

enum Destination: String, CaseIterable, Identifiable {
    case tasks = "Tasks", issues = "Issue Queue", workers = "Workers", projects = "Projects"
    var id: String { rawValue }
    var symbol: String { switch self { case .issues: return "list.number"; case .tasks: return "checklist"; case .workers: return "desktopcomputer"; case .projects: return "folder" } }
    func badge(in snapshot: DashboardSnapshot?) -> String? {
        guard let snapshot else { return nil }
        switch self {
        case .tasks:
            let counts = snapshot.activeTaskCounts
            let total = counts.attention + counts.running + counts.waiting
            return total > 0 ? "\(total)" : nil
        case .issues: return snapshot.openIssueCount > 0 ? "\(snapshot.openIssueCount)" : nil
        case .workers: return "\(snapshot.board.workers.filter(\.live).count)/\(snapshot.board.workers.count)"
        case .projects: return "\(snapshot.uniqueProjects.flatMap(\.repos).filter(\.enabled).count)"
        }
    }
}

struct DashboardSidebar: View {
    let snapshot: DashboardSnapshot?
    @Binding var destination: Destination?
    var body: some View {
        VStack(spacing: 8) {
            HStack(spacing: 8) {
                Image(nsImage: BrandMark.menuBar)
                Text("ClawFlow").font(.headline)
                Spacer()
            }.padding(.horizontal, 16).padding(.top, 18).padding(.bottom, 8)
            List(Destination.allCases, selection: $destination) { item in
                Label(item.rawValue, systemImage: item.symbol)
                    .badge(item.badge(in: snapshot).map { Text($0) })
                    .tag(item)
            }
            SettingsLink().padding()
        }.frame(width: 190).background(.bar)
    }
}

/// Attention, running and waiting at a glance. Zero counts stay out of the way.
struct StatusCounts: View {
    let snapshot: DashboardSnapshot
    var body: some View {
        let counts = snapshot.activeTaskCounts
        HStack(spacing: 12) {
            if counts.attention > 0 { Text("\(counts.attention) need attention").foregroundStyle(.orange) }
            Text("\(counts.running) running").foregroundStyle(.secondary)
            Text("\(counts.waiting) waiting").foregroundStyle(.secondary)
        }
    }
}

struct DashboardView: View {
    @ObservedObject var model: DashboardModel
    @State private var destination: Destination? = .tasks
    @State private var search = ""
    @State private var projectFilter = ""
    var body: some View {
        HStack(spacing: 0) {
            DashboardSidebar(snapshot: model.snapshot, destination: $destination)
            Divider()
            VStack(spacing: 0) {
                HStack {
                    Label(model.summary.title, systemImage: model.summary.symbol)
                    if let snapshot = model.snapshot, model.summary.connected { StatusCounts(snapshot: snapshot) }
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
                    switch destination ?? .tasks {
                    case .issues: IssueQueueView(snapshot: snapshot, search: search, projectFilter: $projectFilter)
                    case .tasks: TasksView(model: model, snapshot: snapshot, search: search, projectFilter: $projectFilter)
                    case .workers: WorkersView(model: model, snapshot: snapshot, search: search)
                    case .projects: ProjectsView(model: model, snapshot: snapshot, search: search) { name in
                        projectFilter = name; destination = .issues; search = ""
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
                    if let received = model.received { Text("Last received \(relativeAge(received, now: model.now))") }
                    else { Text("No status received") }
                    Spacer()
                    Text(model.snapshot.map { "Controller \($0.version)" } ?? "")
                }.font(.caption).foregroundStyle(.secondary).padding(8)
            }
        }
        .searchable(text: $search, prompt: "Search \((destination ?? .tasks).rawValue.lowercased())")
        .toolbar {
            Button { Task { await model.refresh() } } label: { Label("Refresh", systemImage: "arrow.clockwise") }.keyboardShortcut("r")
        }
        .frame(minWidth: 860, minHeight: 520)
    }
}

struct ProjectPicker: View {
    let snapshot: DashboardSnapshot
    @Binding var selection: String
    var body: some View {
        Picker("Project", selection: $selection) {
            Text("All projects").tag("")
            ForEach(snapshot.projects) { Text($0.name).tag($0.name) }
        }.labelsHidden().frame(maxWidth: 200)
    }
}

/// Aligned facts for a detail pane.
struct FactGrid<Content: View>: View {
    @ViewBuilder let content: Content
    var body: some View {
        Grid(alignment: .leading, horizontalSpacing: 16, verticalSpacing: 8) { content }
    }
}
struct Fact<Value: View>: View {
    let label: String
    @ViewBuilder let value: Value
    init(_ label: String, @ViewBuilder value: () -> Value) { self.label = label; self.value = value() }
    var body: some View {
        GridRow {
            Text(label).foregroundStyle(.secondary).gridColumnAlignment(.trailing)
            value.textSelection(.enabled)
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

struct TasksView: View {
    @ObservedObject var model: DashboardModel
    let snapshot: DashboardSnapshot
    let search: String
    @Binding var projectFilter: String
    @State private var history = false
    @State private var selected: String?
    var rows: [TaskRow] {
        let repos = snapshot.projects.first { $0.name == projectFilter }?.repos.map(\.name)
        return snapshot.board.tasks.filter {
            ($0.group == .history) == history && (projectFilter.isEmpty || repos?.contains($0.repo) == true) &&
            (search.isEmpty || "\($0.title) \($0.worker) \($0.status)".localizedCaseInsensitiveContains(search))
        }.sorted { a, b in
            if history { return a.updatedAt > b.updatedAt }
            if a.group != b.group { return a.group.rawValue < b.group.rawValue }
            return a.updatedAt > b.updatedAt
        }
    }
    /// Opening the page shows the most urgent task instead of an empty pane.
    private var current: TaskRow? { rows.first { $0.id == selected } ?? rows.first }
    private var selection: Binding<String?> { Binding(get: { current?.id }, set: { selected = $0 }) }
    private var groups: [TaskGroup] { history ? [.history] : [.attention, .running, .waiting] }
    var body: some View {
        VStack(spacing: 0) {
            HStack {
                Picker("Tasks", selection: $history) { Text("Active").tag(false); Text("History").tag(true) }.labelsHidden().pickerStyle(.segmented).frame(width: 170)
                Spacer()
                ProjectPicker(snapshot: snapshot, selection: $projectFilter)
            }.padding(.horizontal, 18).padding(.vertical, 10).fixedSize(horizontal: false, vertical: true)
            Divider()
            HStack(spacing: 0) {
                List(selection: selection) {
                    ForEach(groups, id: \.rawValue) { group in
                        let members = rows.filter { $0.group == group }
                        if !members.isEmpty {
                            Section("\(group.title) · \(members.count)") {
                                ForEach(members) { row in
                                    TaskListRow(row: row, connected: model.summary.connected, now: model.summary.connected ? model.now : snapshot.board.generatedAt).tag(row.id)
                                }
                            }
                        }
                    }
                }.frame(width: 350).frame(maxHeight: .infinity)
                    .overlay { if rows.isEmpty { ContentUnavailableView(history ? "No finished tasks" : "Nothing active", systemImage: "checklist") } }
                Divider()
                if let row = current {
                    TaskDetail(model: model, snapshot: snapshot, row: row).id(row.id).frame(maxWidth: .infinity)
                } else {
                    Color.clear.frame(maxWidth: .infinity, maxHeight: .infinity)
                }
            }.frame(maxWidth: .infinity, maxHeight: .infinity)
        }
    }
}

struct TaskListRow: View {
    let row: TaskRow
    let connected: Bool
    let now: Date
    var body: some View {
        HStack(alignment: .top, spacing: 8) {
            if row.group == .running && connected {
                ProgressView().controlSize(.small).frame(width: 18).accessibilityLabel("Task running")
            } else {
                Image(systemName: row.symbol).frame(width: 18).foregroundStyle(row.group == .attention ? .orange : .secondary)
            }
            VStack(alignment: .leading, spacing: 3) {
                Text(row.displayTitle).lineLimit(2)
                HStack(spacing: 4) {
                    Text("\(row.repoShortName) \(row.reference)")
                    Text("·")
                    Text(row.status)
                    if row.group == .running { Text("·"); Text(row.elapsed(at: now)).monospacedDigit() }
                }.font(.caption).foregroundStyle(.secondary).lineLimit(1)
            }
        }.padding(.vertical, 3)
    }
}

struct TaskDetail: View {
    @ObservedObject var model: DashboardModel
    let snapshot: DashboardSnapshot
    let row: TaskRow
    @State private var showEvents = false
    @State private var events: [TaskEvent] = []
    @State private var eventError: String?
    @State private var loading = false
    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 16) {
                VStack(alignment: .leading, spacing: 8) {
                    HStack(spacing: 8) {
                        ActivityBadge(activity: row.activity)
                        Label(row.status, systemImage: row.symbol).font(.callout.weight(.medium))
                            .foregroundStyle(row.group == .attention ? .orange : .primary)
                    }
                    Text(row.displayTitle).font(.title2.weight(.semibold)).textSelection(.enabled).fixedSize(horizontal: false, vertical: true)
                }
                Text(row.reason).textSelection(.enabled).fixedSize(horizontal: false, vertical: true)
                    .padding(10).frame(maxWidth: .infinity, alignment: .leading)
                    .background((row.group == .attention ? Color.orange : Color.secondary).opacity(0.1), in: RoundedRectangle(cornerRadius: 8))
                FactGrid {
                    Fact("Repository", "\(row.repo) \(row.reference)")
                    Fact("Worker", row.worker.isEmpty ? "Unassigned" : row.worker)
                    if row.group == .running || row.startedAt != nil {
                        Fact("Elapsed", row.elapsed(at: model.summary.connected ? model.now : snapshot.board.generatedAt))
                    }
                    Fact("Attempt", "\(row.attempt + 1)")
                    Fact("Last update", relativeAge(row.updatedAt, now: model.summary.connected ? model.now : snapshot.board.generatedAt))
                    if !row.tags.isEmpty { Fact("Required tags", row.tags.joined(separator: ", ")) }
                }
                HStack {
                    if let link = row.link(in: snapshot) { Link(row.issue > 0 ? "Open issue" : "Open pull request", destination: link).buttonStyle(.bordered) }
                    if row.issue > 0, let pr = row.prLink(in: snapshot) { Link("Open PR #\(row.pr)", destination: pr).buttonStyle(.bordered) }
                }
                Divider()
                DisclosureGroup("Activity log", isExpanded: $showEvents) {
                    VStack(alignment: .leading, spacing: 8) {
                        Text("Coordinator events, newest first. Execution logs remain on the worker.").font(.caption).foregroundStyle(.secondary)
                        if loading { ProgressView() }
                        if let eventError { Text(eventError).foregroundStyle(.orange) }
                        ForEach(Array(events.enumerated()), id: \.offset) { _, event in
                            HStack(alignment: .top) { Text(event.at, style: .time).foregroundStyle(.secondary); Text(event.action).textSelection(.enabled) }.font(.caption.monospaced())
                        }
                        Button("Refresh activity") { Task { await loadEvents() } }.disabled(!model.summary.connected || loading)
                    }.padding(.top, 8)
                }.onChange(of: showEvents) { _, expanded in if expanded { Task { await loadEvents() } } }
                Text("Task ID: \(row.id)").font(.caption).foregroundStyle(.tertiary).textSelection(.enabled)
            }.padding(20).frame(maxWidth: 640, alignment: .leading).frame(maxWidth: .infinity, alignment: .leading)
        }
    }
    private func loadEvents() async {
        guard model.summary.connected, !loading else { return }
        loading = true; eventError = nil; defer { loading = false }
        do { events = try decoder().decode([TaskEvent].self, from: await model.request(["events", row.id])) }
        catch { eventError = error.localizedDescription }
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
    var body: some View {
        VStack(spacing: 0) {
            HStack {
                Text("\(snapshot.board.workers.filter(\.live).count) of \(snapshot.board.workers.count) online").foregroundStyle(.secondary)
                Spacer()
                Button("Add Worker") { adding = true }.disabled(!model.summary.connected || model.busy)
            }.padding(.horizontal, 18).padding(.vertical, 10).fixedSize(horizontal: false, vertical: true)
            Divider()
            HStack(spacing: 0) {
                List(rows, selection: selection) { row in
                    VStack(alignment: .leading, spacing: 5) {
                        HStack {
                            Label(row.name, systemImage: row.live ? "desktopcomputer" : "wifi.slash")
                            Spacer()
                            Text("\(row.runningTasks.count)/\(row.maxParallel)").font(.caption.monospacedDigit()).foregroundStyle(.secondary)
                        }
                        Text(row.state).font(.caption).foregroundStyle(row.live && !row.paused ? Color.secondary : Color.orange)
                    }.padding(.vertical, 3).tag(row.id)
                }.frame(width: 300).frame(maxHeight: .infinity)
                Divider()
                if let row = current {
                    ScrollView {
                        VStack(alignment: .leading, spacing: 16) {
                            VStack(alignment: .leading, spacing: 4) {
                                Text(row.name).font(.title2.weight(.semibold))
                                Text(row.state).foregroundStyle(.secondary)
                            }
                            FactGrid {
                                Fact("Capacity", "\(row.runningTasks.count) of \(row.maxParallel) in use")
                                Fact("Roles", row.roles.joined(separator: ", "))
                                Fact("Tags", row.tags.isEmpty ? "No project tags" : row.tags.joined(separator: ", "))
                                if let seen = row.lastSeen { Fact("Last contact", relativeAge(seen, now: model.summary.connected ? model.now : snapshot.board.generatedAt)) }
                            }
                            let tasks = snapshot.board.tasks.filter { $0.worker == row.name && $0.group != .history }
                            if !tasks.isEmpty {
                                VStack(alignment: .leading, spacing: 6) {
                                    Text("Current tasks").font(.headline)
                                    ForEach(tasks) { task in
                                        Text("\(task.displayTitle) · \(task.status)").font(.callout)
                                    }
                                }
                            }
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
                            Text("Stopping assignments lets active tasks finish. Eligibility follows repository and worker tags; review tags before resuming a new worker.").font(.caption).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
                        }.padding(20).frame(maxWidth: 640, alignment: .leading).frame(maxWidth: .infinity, alignment: .leading)
                    }
                } else { ContentUnavailableView("No workers", systemImage: "desktopcomputer").frame(maxWidth: .infinity, maxHeight: .infinity) }
            }.frame(maxWidth: .infinity, maxHeight: .infinity)
        }.sheet(isPresented: $adding) { AddWorkerView(model: model) }
    }
}

struct ProjectsView: View {
    @ObservedObject var model: DashboardModel
    let snapshot: DashboardSnapshot
    let search: String
    let showTasks: (String) -> Void
    var body: some View {
        List {
            ForEach(snapshot.uniqueProjects.filter { search.isEmpty || $0.name.localizedCaseInsensitiveContains(search) || $0.repos.contains { $0.name.localizedCaseInsensitiveContains(search) } }) { project in
                Section {
                    ForEach(project.repos) { repo in
                        HStack(spacing: 10) {
                            VStack(alignment: .leading, spacing: 4) {
                                Text(repo.name).fontWeight(.medium).foregroundStyle(repo.enabled ? .primary : .secondary).lineLimit(1)
                                if !repo.workerTags.isEmpty { Text(repo.workerTags.joined(separator: ", ")).font(.caption).foregroundStyle(.secondary).lineLimit(1) }
                            }
                            Spacer()
                            Pill(text: "CI \(repo.ciRequired ? "required" : "optional")", tint: repo.ciRequired ? .blue : .secondary)
                            Pill(text: "Acceptance \(repo.acceptanceRequired ? "required" : "optional")", tint: repo.acceptanceRequired ? .blue : .secondary)
                            Pill(text: "Auto-merge \(repo.autoMerge ? "on" : "off")", tint: repo.autoMerge ? .green : .secondary)
                            Button(repo.enabled ? "Pause" : "Resume") { Task { await model.perform(repo.enabled ? "disable" : "enable", name: repo.name) } }
                                .disabled(!model.summary.connected || model.busy)
                        }.padding(.vertical, 3)
                    }
                } header: {
                    HStack { Text(project.name); Spacer(); Button("View issue queue") { showTasks(project.name) }.buttonStyle(.plain).foregroundStyle(.secondary) }
                }
            }
        }
    }
}

extension QueueTone {
    var color: Color {
        switch self {
        case .active: return .blue
        case .ready: return .green
        case .blocked: return .orange
        case .waiting, .neutral: return .secondary
        }
    }
}

struct IssueQueueView: View {
    let snapshot: DashboardSnapshot
    let search: String
    @Binding var projectFilter: String
    private var repos: [RepoRow] {
        snapshot.uniqueProjects.filter { projectFilter.isEmpty || $0.name == projectFilter }.flatMap(\.repos)
    }
    private func issues(_ repo: RepoRow) -> [IssueQueueRow] {
        (repo.issueQueue ?? []).filter { search.isEmpty || "\($0.key) \($0.title ?? "") \($0.status) \($0.detail ?? "")".localizedCaseInsensitiveContains(search) }
    }
    var body: some View {
        let populated = repos.filter { !issues($0).isEmpty || $0.queueError != nil }
        let idle = repos.filter { repo in !populated.contains { $0.id == repo.id } }
        VStack(spacing: 0) {
            HStack {
                Text("\(populated.reduce(0) { $0 + issues($1).count }) open · scheduler order per repository").foregroundStyle(.secondary)
                Spacer()
                ProjectPicker(snapshot: snapshot, selection: $projectFilter)
            }.padding(.horizontal, 18).padding(.vertical, 10).fixedSize(horizontal: false, vertical: true)
            Divider()
            List {
                ForEach(populated) { repo in
                    Section {
                        if let error = repo.queueError { Text(error).foregroundStyle(.orange) }
                        ForEach(issues(repo)) { issue in IssueQueueLine(issue: issue, repo: repo) }
                    } header: {
                        HStack {
                            Text(repo.name)
                            Spacer()
                            if let updated = repo.queueUpdatedAt { Text("Updated \(relativeAge(updated, now: snapshot.board.generatedAt))").fontWeight(.regular) }
                            if let skipped = repo.skippedIssues, skipped > 0 { Text("· \(skipped) skipped").fontWeight(.regular) }
                        }
                    }
                }
                if !idle.isEmpty {
                    Section("Other repositories") {
                        ForEach(idle) { repo in
                            HStack {
                                Text(repo.name).foregroundStyle(.secondary).lineLimit(1)
                                Spacer()
                                Text(repo.idleNote).font(.caption).foregroundStyle(.secondary)
                            }
                        }
                    }
                }
            }.overlay { if populated.isEmpty && idle.isEmpty { ContentUnavailableView("No matching issues", systemImage: "list.number") } }
        }
    }
}

struct IssueQueueLine: View {
    let issue: IssueQueueRow
    let repo: RepoRow
    private var link: URL? {
        safeWebURL(repo.url)?.appendingPathComponent(repo.platform == "gitlab" ? "-/issues/\(issue.number)" : "issues/\(issue.number)")
    }
    var body: some View {
        HStack(spacing: 10) {
            Text("\(issue.position)").font(.callout.monospacedDigit()).foregroundStyle(.tertiary).frame(width: 22, alignment: .trailing)
            Pill(text: issue.tier, tint: issue.tier == "P0" || issue.tier == "P1" ? .orange : .secondary)
            Text("#\(issue.number)").font(.callout.monospacedDigit()).foregroundStyle(.secondary)
            Text(issue.title ?? "").lineLimit(1).truncationMode(.tail)
            Spacer(minLength: 12)
            if let detail = issue.detail { Text(detail).font(.caption).foregroundStyle(.secondary).lineLimit(1).truncationMode(.tail).frame(maxWidth: 260, alignment: .trailing) }
            Pill(text: issue.status, tint: issue.tone.color)
            if let link { Link(destination: link) { Image(systemName: "arrow.up.right.square") }.foregroundStyle(.secondary).help("Open issue") }
        }.padding(.vertical, 2).help(issue.state)
    }
}
