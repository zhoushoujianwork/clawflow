import SwiftUI

/// Read-only execution output for one job. It replays what was recorded, then
/// follows new output while the job runs, and remains available afterwards.
/// Leaving this view or the app does not affect the execution.
struct JobLogView: View {
    @ObservedObject var model: DashboardModel
    let row: TaskRow
    @StateObject private var store: JobLogStore
    @State private var follow = JobLogFollowState()
    @State private var scrollRequest = 0

    init(model: DashboardModel, row: TaskRow) {
        self.model = model; self.row = row
        _store = StateObject(wrappedValue: JobLogStore(taskId: row.id) { [weak model] id, epoch, after in
            guard let model else { throw CancellationError() }
            return try await model.request(["logs", id, "--epoch", String(epoch), "--after", String(after), "--limit", "200"])
        })
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            header
            content
        }
        .task(id: row.id) { store.start { model.summary.connected } }
        .onDisappear { store.stop() }
    }

    private var connected: Bool { model.summary.connected }

    private var header: some View {
        HStack(spacing: 10) {
            Label("Execution output", systemImage: "terminal").font(.headline)
            if store.buffer.isLive && connected { ProgressView().controlSize(.small) }
            Text(statusText).font(.caption).foregroundStyle(.secondary)
            Spacer()
            if store.buffer.epochs.count > 1 {
                Picker("Attempt", selection: Binding(get: { store.selectedEpoch == 0 ? store.buffer.epoch : store.selectedEpoch }, set: { store.select(epoch: $0) })) {
                    ForEach(store.buffer.epochs, id: \.epoch) { Text("Attempt \($0.attempt)").tag($0.epoch) }
                }.pickerStyle(.menu).frame(width: 130).labelsHidden()
            }
            if !store.buffer.lines.isEmpty {
                Button(follow.isFollowing ? "Pause" : "Follow latest") {
                    if follow.isFollowing { follow.pause() } else { follow.resume(at: Date()); scrollRequest += 1 }
                }.controlSize(.small).help(follow.isFollowing ? "Stop following new output" : "Jump to the newest output and keep following")
            }
        }
    }

    private var statusText: String {
        if !connected { return "Disconnected · showing last received output" }
        if store.buffer.isLive { return "Live" }
        if store.buffer.interrupted { return "Interrupted" }
        if store.buffer.lines.isEmpty { return "" }
        return store.buffer.active ? "Waiting for output" : "Finished · recorded output"
    }

    @ViewBuilder private var content: some View {
        if let notice = store.buffer.notice {
            VStack(alignment: .leading, spacing: 4) {
                Label(notice.title, systemImage: "info.circle").font(.callout)
                Text(notice.detail).font(.caption).foregroundStyle(.secondary)
                if let error = store.error { Text(error).font(.caption).foregroundStyle(.orange) }
            }.frame(maxWidth: .infinity, alignment: .leading).padding(12).background(.quaternary, in: RoundedRectangle(cornerRadius: 6))
        } else {
            if store.buffer.hasGap {
                Label("Some output is missing or shortened. What is shown is what the recorder kept.", systemImage: "exclamationmark.triangle")
                    .font(.caption).foregroundStyle(.orange)
            }
            if let error = store.error { Text(error).font(.caption).foregroundStyle(.orange) }
            logList
        }
    }

    private var logList: some View {
        ScrollViewReader { proxy in
            ScrollView {
                LazyVStack(alignment: .leading, spacing: 6) {
                    ForEach(store.buffer.lines) { line in
                        JobLogRow(line: line, callTitle: line.toolCallId.flatMap { store.buffer.callTitles[$0] }).equatable()
                    }
                    Color.clear.frame(height: 1).id(JobLogView.bottomID)
                        .onAppear { follow.bottomMarkerAppeared() }
                        .onDisappear { follow.bottomMarkerDisappeared(at: Date()) }
                }.padding(10)
            }
            .frame(height: 320)
            .background(Color(nsColor: .textBackgroundColor), in: RoundedRectangle(cornerRadius: 6))
            .overlay(RoundedRectangle(cornerRadius: 6).stroke(.quaternary))
            .onChange(of: store.buffer.lines.last?.id) { _, _ in
                if follow.linesAppended(at: Date()) { proxy.scrollTo(JobLogView.bottomID, anchor: .bottom) }
            }
            .onChange(of: scrollRequest) { _, _ in proxy.scrollTo(JobLogView.bottomID, anchor: .bottom) }
            .onAppear { proxy.scrollTo(JobLogView.bottomID, anchor: .bottom) }
        }
    }
    static let bottomID = "job-log-bottom"
}

/// One output entry. Equatable so unchanged rows are skipped when new ones are appended.
struct JobLogRow: View, Equatable {
    let line: JobLogLine
    let callTitle: String?
    @State private var expanded = false
    static func == (a: Self, b: Self) -> Bool { a.line == b.line && a.callTitle == b.callTitle }
    private static let collapsedLines = 14

    private var color: Color {
        switch line.kind {
        case .assistant: return .primary
        case .user: return .blue
        case .toolCall: return .purple
        case .toolResult, .boundary: return .secondary
        case .error: return .red
        case .gap: return .orange
        }
    }
    private var symbol: String {
        switch line.kind {
        case .assistant: return "text.bubble"
        case .user: return "person"
        case .toolCall: return "wrench.and.screwdriver"
        case .toolResult: return "arrow.turn.down.right"
        case .error: return "exclamationmark.octagon"
        case .gap: return "exclamationmark.triangle"
        case .boundary: return "flag"
        }
    }
    private var long: Bool { line.body.count > 1200 || line.body.reduce(0) { $1 == "\n" ? $0 + 1 : $0 } >= Self.collapsedLines }

    var body: some View {
        VStack(alignment: .leading, spacing: 3) {
            HStack(spacing: 6) {
                Image(systemName: symbol).foregroundStyle(color).frame(width: 14)
                Text(line.title).font(.caption.weight(.semibold)).foregroundStyle(color)
                if line.kind == .toolResult || line.kind == .error, let callTitle { Text("↳ " + callTitle).font(.caption2).foregroundStyle(.tertiary) }
                if let time = line.time, let short = Self.clock(time) { Spacer(); Text(short).font(.caption2.monospacedDigit()).foregroundStyle(.tertiary) }
            }
            if !line.body.isEmpty {
                Text(line.body)
                    .font(.system(.caption, design: .monospaced))
                    .foregroundStyle(line.kind == .assistant ? Color.primary : color)
                    .lineLimit(expanded ? nil : Self.collapsedLines)
                    .textSelection(.enabled)
                    .frame(maxWidth: .infinity, alignment: .leading)
                if long { Button(expanded ? "Show less" : "Show all") { expanded.toggle() }.buttonStyle(.link).font(.caption2) }
            }
            if let note = line.truncatedNote { Text(note).font(.caption2).foregroundStyle(.orange) }
        }
        .padding(.vertical, 2)
        .padding(.leading, line.kind == .toolResult || line.kind == .error ? 14 : 0)
    }
    private static func clock(_ iso: String) -> String? {
        guard iso.count >= 19 else { return nil }
        let start = iso.index(iso.startIndex, offsetBy: 11), end = iso.index(iso.startIndex, offsetBy: 19)
        return String(iso[start..<end])
    }
}
