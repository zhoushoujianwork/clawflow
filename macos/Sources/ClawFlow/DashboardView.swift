import SwiftUI

enum Destination: String, CaseIterable, Identifiable {
    case tasks = "Tasks", workers = "Workers", projects = "Projects"
    var id: String { rawValue }
    var symbol: String { switch self { case .tasks: return "checklist"; case .workers: return "desktopcomputer"; case .projects: return "folder" } }
}

struct DashboardView: View {
    @ObservedObject var model: DashboardModel
    @State private var destination: Destination? = .tasks
    @State private var search = ""
    @State private var projectFilter = ""
    var body: some View {
        NavigationSplitView {
            List(Destination.allCases, selection: $destination) { item in
                Label(item.rawValue, systemImage: item.symbol).tag(item)
            }.navigationSplitViewColumnWidth(min: 130, ideal: 150, max: 190)
            SettingsLink().padding()
        } detail: {
            VStack(spacing: 0) {
                HStack {
                    Label(model.summary.title, systemImage: model.summary.symbol)
                    Spacer()
                    Text(model.snapshot?.controller ?? "Connect in Settings").foregroundStyle(.secondary)
                }.font(.callout).padding(12)
                Divider()
                if let error = model.error {
                    Text(error).font(.caption).foregroundStyle(.orange).textSelection(.enabled).padding(10)
                }
                if let error = model.actionError {
                    HStack { Text(error).foregroundStyle(.orange); Button("Dismiss") { model.actionError = nil } }.font(.caption).padding(10)
                }
                if let snapshot = model.snapshot {
                    switch destination ?? .tasks {
                    case .tasks: TasksView(model: model, snapshot: snapshot, search: search, projectFilter: $projectFilter)
                    case .workers: WorkersView(model: model, snapshot: snapshot, search: search)
                    case .projects: ProjectsView(model: model, snapshot: snapshot, search: search) { name in
                        projectFilter = name; destination = .tasks; search = ""
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
        .frame(minWidth: 760, minHeight: 480)
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
    var body: some View {
        VStack(spacing: 0) {
            HStack {
                Picker("Tasks", selection: $history) { Text("Active").tag(false); Text("History").tag(true) }.pickerStyle(.segmented).frame(width: 180)
                Spacer()
                Picker("Project", selection: $projectFilter) {
                    Text("All projects").tag("")
                    ForEach(snapshot.projects) { Text($0.name).tag($0.name) }
                }.frame(maxWidth: 260)
            }.padding(12)
            HSplitView {
                List(rows, selection: $selected) { row in
                    HStack(alignment: .top) {
                        Image(systemName: row.symbol).frame(width: 20).foregroundStyle(row.group == .attention ? .orange : .secondary)
                        VStack(alignment: .leading, spacing: 5) {
                            Text(row.title).lineLimit(2)
                            Text("\(row.status) · \(row.worker.isEmpty ? "Unassigned" : row.worker)").font(.caption).foregroundStyle(.secondary)
                        }
                    }.padding(.vertical, 5).tag(row.id)
                }.frame(minWidth: 250, idealWidth: 380)
                    .overlay { if rows.isEmpty { ContentUnavailableView("No matching tasks", systemImage: "checklist") } }
                if let row = rows.first(where: { $0.id == selected }) {
                    TaskDetail(model: model, snapshot: snapshot, row: row).id(row.id).frame(minWidth: 280, idealWidth: 380)
                } else {
                    ContentUnavailableView("Select a task", systemImage: "sidebar.right").frame(minWidth: 280)
                }
            }
        }
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
                Label(row.status, systemImage: row.symbol).font(.headline)
                Text(row.title).font(.title3).textSelection(.enabled)
                Text(row.reason).textSelection(.enabled)
                LabeledContent("Worker", value: row.worker.isEmpty ? "Unassigned" : row.worker)
                LabeledContent("Attempt", value: "\(row.attempt + 1)")
                LabeledContent("Last update") { Text(row.updatedAt, style: .relative) }
                if !row.tags.isEmpty { LabeledContent("Required tags", value: row.tags.joined(separator: ", ")) }
                if let link = row.link(in: snapshot) { Link(row.issue > 0 ? "Open issue" : "Open pull request", destination: link).buttonStyle(.bordered) }
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
            }.padding(20).frame(maxWidth: .infinity, alignment: .leading)
        }
    }
    private func loadEvents() async {
        guard model.summary.connected, !loading else { return }
        loading = true; eventError = nil; defer { loading = false }
        do { events = try decoder().decode([TaskEvent].self, from: await model.request(["events", row.id])) }
        catch { eventError = error.localizedDescription }
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
            }.padding(12)
            HSplitView {
                List(snapshot.board.workers.filter { search.isEmpty || "\($0.name) \($0.tags.joined(separator: " "))".localizedCaseInsensitiveContains(search) }, selection: $selected) { row in
                    VStack(alignment: .leading, spacing: 5) {
                        Label(row.name, systemImage: row.live ? "desktopcomputer" : "wifi.slash")
                        Text("\(row.state) · \(row.runningTasks.count)/\(row.maxParallel) active").font(.caption).foregroundStyle(.secondary)
                    }.padding(.vertical, 5).tag(row.id)
                }.frame(minWidth: 240)
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
                } else { ContentUnavailableView("Select a worker", systemImage: "desktopcomputer").frame(minWidth: 300) }
            }
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
            ForEach(snapshot.projects.filter { search.isEmpty || $0.name.localizedCaseInsensitiveContains(search) || $0.repos.contains { $0.name.localizedCaseInsensitiveContains(search) } }) { project in
                Section {
                    ForEach(project.repos) { repo in
                        VStack(alignment: .leading, spacing: 10) {
                            HStack {
                                Text(repo.name).font(.headline)
                                Spacer()
                                Text(repo.enabled ? "Monitoring" : "Paused").foregroundStyle(.secondary)
                                Button(repo.enabled ? "Pause" : "Resume") { Task { await model.perform(repo.enabled ? "disable" : "enable", name: repo.name) } }
                                    .disabled(!model.summary.connected || model.busy)
                            }
                            Text("CI \(repo.ciRequired ? "required" : "optional") · Acceptance \(repo.acceptanceRequired ? "required" : "optional") · Auto-merge \(repo.autoMerge ? "on" : "off")").font(.caption).foregroundStyle(.secondary)
                            if !repo.workerTags.isEmpty { Text("Worker tags: " + repo.workerTags.joined(separator: ", ")).font(.caption) }
                        }.padding(.vertical, 8)
                    }
                } header: {
                    HStack { Text(project.name); Spacer(); Button("View tasks") { showTasks(project.name) }.buttonStyle(.plain) }
                }
            }
        }
    }
}
