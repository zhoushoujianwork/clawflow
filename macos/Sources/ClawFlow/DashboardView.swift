import SwiftUI

enum Destination: String, CaseIterable, Identifiable {
    case issues = "Issue Queue", tasks = "Tasks", workers = "Workers", projects = "Projects"
    var id: String { rawValue }
    var symbol: String { switch self { case .issues: return "list.number"; case .tasks: return "checklist"; case .workers: return "desktopcomputer"; case .projects: return "folder" } }
}

struct DashboardView: View {
    @ObservedObject var model: DashboardModel
    @State private var destination: Destination? = .issues
    @State private var search = ""
    @State private var projectFilter = ""
    var body: some View {
        HStack(spacing: 0) {
            VStack(spacing: 8) {
                HStack(spacing: 8) {
                    Image(nsImage: BrandMark.menuBar)
                    Text("ClawFlow").font(.headline)
                    Spacer()
                }.padding(.horizontal, 16).padding(.top, 18).padding(.bottom, 8)
                List(Destination.allCases, selection: $destination) { item in
                    Label(item.rawValue, systemImage: item.symbol).padding(.vertical, 6).tag(item)
                }.listStyle(.sidebar)
                SettingsLink().controlSize(.regular).padding()
            }.frame(width: 200).background(.bar)
            Divider()
            VStack(spacing: 0) {
                HStack {
                    Label(model.summary.title, systemImage: model.summary.symbol)
                    if let snapshot = model.snapshot, model.summary.connected {
                        Text("\(snapshot.board.tasks.filter { $0.group == .running }.count) running · \(snapshot.board.tasks.filter { $0.group == .waiting }.count) waiting").foregroundStyle(.secondary)
                    }
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
                    if let received = model.received { Text("Last received \(received, style: .relative) ago") }
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
        .frame(minWidth: 1100, minHeight: 680)
    }
}

struct TasksView: View {
    @ObservedObject var model: DashboardModel
    let snapshot: DashboardSnapshot
    let search: String
    @Binding var projectFilter: String
    @State private var history = false
    @State private var activityFilter = ""
    @State private var selected: String?
    var rows: [TaskRow] {
        let repos = snapshot.projects.first { $0.name == projectFilter }?.repos.map(\.name)
        return snapshot.board.tasks.filter {
            ($0.group == .history) == history && (activityFilter.isEmpty || $0.kind == activityFilter) && (projectFilter.isEmpty || repos?.contains($0.repo) == true) &&
            (search.isEmpty || "\($0.title) \($0.worker) \($0.status)".localizedCaseInsensitiveContains(search))
        }.sorted { a, b in
            if history { return a.updatedAt > b.updatedAt }
            if a.group != b.group { return a.group.rawValue < b.group.rawValue }
            return a.updatedAt > b.updatedAt
        }
    }
    var body: some View {
        VStack(spacing: 0) {
            HStack {
                Picker("Tasks", selection: $history) { Text("Active").tag(false); Text("History").tag(true) }.pickerStyle(.segmented).frame(width: 180)
                Text("\(rows.count) shown").font(.caption).foregroundStyle(.secondary)
                Spacer()
                Picker("Stage", selection: $activityFilter) {
                    Text("All stages").tag("")
                    Text("Implementation").tag("implement")
                    Text("PR review").tag("acceptance")
                    Text("PR repair").tag("repair")
                    Text("Issue reply").tag("reply")
                }.frame(width: 200)
                Picker("Project", selection: $projectFilter) {
                    Text("All projects").tag("")
                    ForEach(snapshot.projects) { Text($0.name).tag($0.name) }
                }.frame(maxWidth: 260)
            }.controlSize(.regular).padding(.horizontal, 20).frame(height: 56)
            Divider()
            HStack(spacing: 0) {
                Table(rows, selection: $selected) {
                    TableColumn("Status") { row in TaskStateLabel(row: row, connected: model.summary.connected) }.width(155)
                    TableColumn("Job") { row in
                        VStack(alignment: .leading, spacing: 4) {
                            Text(row.title).fontWeight(.medium).lineLimit(1).help(row.title)
                            Text((row.pr > 0 ? "PR #\(row.pr) · " : "") + row.reason).font(.caption).foregroundStyle(.secondary).lineLimit(1)
                        }.frame(minHeight: 48).help(row.reason)
                    }.width(min: 240, ideal: 340)
                    TableColumn("Stage") { row in ActivityBadge(activity: row.activity) }.width(120)
                    TableColumn("Runner") { row in Text(row.worker.isEmpty ? "Unassigned" : row.worker).foregroundStyle(.secondary).lineLimit(1) }.width(min: 110, ideal: 140)
                    TableColumn("Duration") { row in
                        Text(row.startedAt == nil ? "—" : row.elapsed(at: model.summary.connected ? model.now : snapshot.board.generatedAt))
                            .font(.caption.monospacedDigit()).foregroundStyle(.secondary)
                            .help(row.startedAt == nil ? "Duration unavailable" : "Duration of this attempt")
                    }.width(100)
                }
                .overlay {
                    if rows.isEmpty { DashboardEmptyState(title: "No matching tasks", symbol: "checklist", detail: history ? "Completed attempts appear here." : "Running and queued jobs appear here when assigned.") }
                }
            }.frame(maxWidth: .infinity, maxHeight: .infinity)
        }
        .sheet(isPresented: Binding(get: { snapshot.board.tasks.contains { $0.id == selected } }, set: { if !$0 { selected = nil } })) {
            if let row = snapshot.board.tasks.first(where: { $0.id == selected }) {
                VStack(spacing: 0) {
                    DetailHeader(title: "Job details · attempt \(row.attempt + 1)") { selected = nil }
                    TaskDetail(model: model, snapshot: snapshot, row: row).id(row.id)
                }.frame(width: 760, height: 620)
            }
        }
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
            VStack(alignment: .leading, spacing: 16) {
                ActivityBadge(activity: row.activity)
                Label(row.status, systemImage: row.symbol).font(.headline)
                Text(row.title).font(.title3).textSelection(.enabled)
                Text(row.reason).textSelection(.enabled)
                LabeledContent("Worker", value: row.worker.isEmpty ? "Unassigned" : row.worker)
                if row.group == .running || row.startedAt != nil {
                    LabeledContent("Elapsed", value: row.elapsed(at: model.summary.connected ? model.now : snapshot.board.generatedAt))
                }
                LabeledContent("Attempt", value: "\(row.attempt + 1)")
                LabeledContent("Last update") { Text(row.updatedAt, style: .relative) }
                if !row.tags.isEmpty { LabeledContent("Required tags", value: row.tags.joined(separator: ", ")) }
                if let link = row.link(in: snapshot) { Link(row.issue > 0 ? "Open issue" : "Open pull request", destination: link).buttonStyle(.bordered) }
                if row.issue > 0, let pr = row.prLink(in: snapshot) { Link("Open PR #\(row.pr)", destination: pr).buttonStyle(.bordered) }
                Divider()
                DisclosureGroup("Coordinator activity", isExpanded: $showEvents) {
                    VStack(alignment: .leading, spacing: 8) {
                        Text("Scheduling events · refreshes every 5 seconds while connected.").font(.caption).foregroundStyle(.secondary)
                        if loading { ProgressView() }
                        if let eventError { Text(eventError).foregroundStyle(.orange) }
                        ForEach(Array(events.enumerated()), id: \.offset) { _, event in
                            HStack(alignment: .top) { Text(event.at, style: .time).foregroundStyle(.secondary); Text(event.action).textSelection(.enabled) }.font(.caption.monospaced())
                        }
                        Button("Refresh activity") { Task { await loadEvents() } }.disabled(!model.summary.connected || loading)
                    }.padding(.top, 8)
                }.onChange(of: showEvents) { _, expanded in if expanded { Task { await loadEvents() } } }
                Text("Full Offload execution output is not available in this build.").font(.caption).foregroundStyle(.secondary)
                Text("Task ID: \(row.id)").font(.caption).foregroundStyle(.tertiary).textSelection(.enabled)
            }.padding(20).frame(maxWidth: .infinity, alignment: .leading)
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

struct WorkersView: View {
    @ObservedObject var model: DashboardModel
    let snapshot: DashboardSnapshot
    let search: String
    @State private var selected: String?
    @State private var adding = false
    @State private var removing = false
    var body: some View {
        VStack(spacing: 0) {
            HStack {
                Text("\(snapshot.board.workers.filter(\.live).count) online").foregroundStyle(.secondary)
                Spacer()
                Button("Add Worker") { adding = true }.disabled(!model.summary.connected || model.busy)
            }.controlSize(.regular).padding(.horizontal, 20).frame(height: 56)
            Divider()
            Table(snapshot.board.workers.filter { search.isEmpty || "\($0.name) \($0.tags.joined(separator: " "))".localizedCaseInsensitiveContains(search) }, selection: $selected) {
                TableColumn("Status") { row in
                    Label(row.state, systemImage: row.live ? (row.paused ? "pause.circle" : "checkmark.circle") : "wifi.slash")
                        .foregroundStyle(row.live && !row.paused ? Color.green : Color.secondary)
                }.width(150)
                TableColumn("Runner") { row in Text(row.name).fontWeight(.medium).frame(minHeight: 48) }.width(min: 200, ideal: 320)
                TableColumn("Capacity") { row in Text("\(row.runningTasks.count) / \(row.maxParallel) active").monospacedDigit() }.width(120)
                TableColumn("Roles") { row in Text(row.roles.joined(separator: ", ")).foregroundStyle(.secondary) }.width(min: 140, ideal: 200)
                TableColumn("Tags") { row in Text(row.tags.isEmpty ? "—" : row.tags.joined(separator: ", ")).foregroundStyle(.secondary) }.width(min: 100, ideal: 160)
            }
        }
        .sheet(isPresented: Binding(get: { selected != nil }, set: { if !$0 { selected = nil } })) {
            VStack(spacing: 0) {
                DetailHeader(title: "Runner details") { selected = nil }
                if let row = snapshot.board.workers.first(where: { $0.id == selected }) {
                    ScrollView {
                        VStack(alignment: .leading, spacing: 16) {
                            Text(row.name).font(.title2)
                            Text(row.state).foregroundStyle(.secondary)
                            LabeledContent("Roles", value: row.roles.joined(separator: ", "))
                            LabeledContent("Tags", value: row.tags.isEmpty ? "No project tags" : row.tags.joined(separator: ", "))
                            LabeledContent("Capacity", value: "\(row.runningTasks.count) / \(row.maxParallel)")
                            if let seen = row.lastSeen { LabeledContent("Last contact") { Text(seen, style: .relative) } }
                            Text("Assignment eligibility follows repository and worker tags. Review tags before resuming a new worker.").font(.callout).foregroundStyle(.secondary)
                            Button(row.paused ? "Resume assignments" : "Stop new assignments") { Task { await model.perform(row.paused ? "resume" : "drain", name: row.name) } }
                                .disabled(!model.summary.connected || model.busy || row.revoked)
                            Text("Stopping assignments lets active tasks finish.").font(.caption).foregroundStyle(.secondary)
                            Divider()
                            ForEach(snapshot.board.tasks.filter { $0.worker == row.name && $0.group != .history }) { task in
                                Text("\(task.title) · \(task.status)").font(.callout)
                            }
                            Button("Remove worker", role: .destructive) { removing = true }
                                .disabled(!model.summary.connected || model.busy || row.revoked || !row.paused || snapshot.board.tasks.contains { $0.worker == row.name && $0.state == "leased" })
                                .confirmationDialog("Revoke access for \(row.name)?", isPresented: $removing) {
                                    Button("Remove worker", role: .destructive) { Task { await model.perform("remove", name: row.name) } }
                                } message: { Text("Historical tasks remain. This worker credential will no longer authenticate.") }
                        }.padding(20).frame(maxWidth: .infinity, alignment: .leading)
                    }.frame(minWidth: 300)
                } else { DashboardEmptyState(title: "Runner unavailable", symbol: "desktopcomputer", detail: "Close this view and refresh the runner list.") }
            }.frame(width: 660, height: 600)
        }.sheet(isPresented: $adding) { AddWorkerView(model: model) }
    }
}

struct ProjectsView: View {
    @ObservedObject var model: DashboardModel
    let snapshot: DashboardSnapshot
    let search: String
    let showTasks: (String) -> Void
    var body: some View {
        VStack(spacing: 0) {
            HStack {
                Text("\(snapshot.projects.count) projects").foregroundStyle(.secondary)
                Spacer()
                Text("Monitoring · CI · Acceptance · Merge").font(.caption).foregroundStyle(.secondary)
            }.padding(.horizontal, 20).frame(height: 56)
            Divider()
            List {
                ForEach(snapshot.projects.filter { search.isEmpty || $0.name.localizedCaseInsensitiveContains(search) || $0.repos.contains { $0.name.localizedCaseInsensitiveContains(search) } }) { project in
                    Section {
                        ForEach(project.repos) { repo in
                            VStack(alignment: .leading, spacing: 10) {
                                HStack {
                                    Text(repo.name).font(.headline)
                                    Spacer()
                                    Text(repo.enabled ? "Monitoring" : "Paused").foregroundStyle(.secondary).frame(width: 100, alignment: .leading)
                                    Button(repo.enabled ? "Pause" : "Resume") { Task { await model.perform(repo.enabled ? "disable" : "enable", name: repo.name) } }
                                        .frame(width: 80).controlSize(.regular)
                                        .disabled(!model.summary.connected || model.busy)
                                }
                                Text("CI \(repo.ciRequired ? "required" : "optional") · Acceptance \(repo.acceptanceRequired ? "required" : "optional") · Auto-merge \(repo.autoMerge ? "on" : "off")").font(.caption).foregroundStyle(.secondary)
                                if !repo.workerTags.isEmpty { Text("Worker tags: " + repo.workerTags.joined(separator: ", ")).font(.caption) }
                            }.padding(.vertical, 8)
                        }
                    } header: {
                        HStack { Text(project.name); Spacer(); Button("View issue queue") { showTasks(project.name) }.buttonStyle(.plain) }
                    }
                }
            }
        }
    }
}

struct IssueQueueView: View {
    let snapshot: DashboardSnapshot
    let search: String
    @Binding var projectFilter: String
    var body: some View {
        VStack(spacing: 0) {
            HStack {
                Text("Open issues · scheduler order per repository").font(.callout).foregroundStyle(.secondary)
                Spacer()
                Picker("Project", selection: $projectFilter) {
                    Text("All projects").tag("")
                    ForEach(snapshot.projects) { Text($0.name).tag($0.name) }
                }.frame(maxWidth: 240)
            }.controlSize(.regular).padding(.horizontal, 20).frame(height: 56)
            Divider()
            List {
                ForEach(snapshot.projects.filter { projectFilter.isEmpty || $0.name == projectFilter }) { project in
                    ForEach(project.repos) { repo in
                        Section {
                            if let error = repo.queueError { Text(error).foregroundStyle(.orange) }
                            if let updated = repo.queueUpdatedAt {
                                Text("Last evaluated \(updated.formatted()) · \(repo.skippedIssues ?? 0) explicitly skipped")
                                    .font(.caption).foregroundStyle(.secondary)
                            } else if repo.queueError == nil {
                                Text(repo.issueQueue == nil ? "Update the controller to view issue queues." : "Not evaluated yet.").foregroundStyle(.secondary)
                            }
                            ForEach((repo.issueQueue ?? []).filter { search.isEmpty || "\($0.key) \($0.title ?? "") \($0.state)".localizedCaseInsensitiveContains(search) }) { issue in
                                VStack(alignment: .leading, spacing: 6) {
                                    HStack {
                                        Text("\(issue.position)").monospacedDigit().foregroundStyle(.secondary).frame(width: 28)
                                        Text("#\(issue.number) \(issue.title ?? "")").fontWeight(.medium).lineLimit(1)
                                        Spacer()
                                        Text(issue.tier).font(.caption.monospaced()).frame(width: 72, alignment: .leading)
                                        Text(issue.status).font(.caption).foregroundStyle(issue.state.hasPrefix("blocked") ? .orange : .secondary).frame(width: 190, alignment: .leading)
                                    }
                                    Text(issue.state).font(.caption).foregroundStyle(.secondary).lineLimit(2).textSelection(.enabled).padding(.leading, 36)
                                    if let base = safeWebURL(repo.url) {
                                        Link("Open issue", destination: base.appendingPathComponent(repo.platform == "gitlab" ? "-/issues/\(issue.number)" : "issues/\(issue.number)" )).font(.caption).padding(.leading, 36)
                                    }
                                }.padding(.vertical, 8)
                            }
                            if repo.queueUpdatedAt != nil && (repo.issueQueue ?? []).isEmpty {
                                Text("No queued issues at the last evaluation.").foregroundStyle(.secondary)
                            }
                        } header: {
                            Text("\(project.name) / \(repo.name) · \(repo.enabled ? "Monitoring" : "Paused") · \((repo.issueQueue ?? []).count) issues")
                        }
                    }
                }
            }
        }
    }
}

// Shared geometry keeps the overview and inspector controls consistent across destinations.
struct DetailHeader: View {
    let title: String
    let close: () -> Void
    var body: some View {
        HStack {
            Text(title).font(.headline)
            Spacer()
            Button("Done", action: close).keyboardShortcut(.cancelAction)
        }.padding(.horizontal, 20).frame(height: 56)
        Divider()
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
            .background(Color(nsColor: .controlBackgroundColor))
    }
}

struct TaskStateLabel: View {
    let row: TaskRow
    let connected: Bool
    var body: some View {
        HStack(spacing: 8) {
            if row.group == .running && connected {
                ProgressView().controlSize(.small).frame(width: 16)
            } else {
                Image(systemName: row.symbol).frame(width: 16)
                    .foregroundStyle(row.group == .attention || row.state == "failed" ? Color.orange : row.state == "done" ? Color.green : Color.secondary)
            }
            Text(row.status).lineLimit(1)
        }.help(connected ? row.reason : "Last known status · connection unavailable")
    }
}
