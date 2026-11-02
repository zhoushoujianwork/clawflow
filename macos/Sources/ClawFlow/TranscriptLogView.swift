import SwiftUI

/// Read-only execution output for one job: Offload messages, tool calls and
/// results, replayed on open and appended while the task is still running.
/// This is content evidence, distinct from the "Coordinator activity"
/// scheduling feed above it in the job detail.
struct TranscriptLogView: View {
    @ObservedObject var model: DashboardModel
    let taskId: String
    @StateObject private var store = TranscriptStore()
    @State private var polling: Task<Void, Never>?

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            header
            if store.gap {
                Label(store.gapReason ?? "Part of the history is unavailable.", systemImage: "exclamationmark.triangle")
                    .font(.caption).foregroundStyle(.orange)
            }
            logBody
            if let error = store.loadError {
                Text(error).font(.caption).foregroundStyle(.orange).textSelection(.enabled)
            }
        }
        .task(id: taskId) {
            store.followLatest()
            await pollLoop()
        }
        .onDisappear { polling?.cancel() }
    }

    private var header: some View {
        HStack(spacing: 10) {
            if store.epochs.count > 1 {
                Picker("Attempt", selection: Binding(
                    get: { store.pinnedEpoch ?? store.currentEpoch },
                    set: { store.select(epoch: $0) }
                )) {
                    ForEach(store.epochs) { e in
                        Text("Attempt \(e.attempt + 1)" + (e.epoch == store.epochs.last?.epoch ? " (latest)" : "")).tag(e.epoch)
                    }
                }.frame(width: 220).labelsHidden()
            }
            Text(store.statusLine).font(.caption).foregroundStyle(.secondary)
            Spacer()
            if store.live {
                ProgressView().controlSize(.small)
                Text("Live").font(.caption).foregroundStyle(.secondary)
            }
            if !store.follow {
                Button("Jump to latest") { store.follow = true }.font(.caption).controlSize(.small)
            }
        }
    }

    @ViewBuilder private var logBody: some View {
        if store.events.isEmpty {
            Text(store.emptyStateText).font(.callout).foregroundStyle(.secondary)
                .frame(maxWidth: .infinity, minHeight: 60, alignment: .center)
        } else {
            ScrollViewReader { proxy in
                ScrollView {
                    // Each event is its own small row view: a new appended
                    // event only inserts one row, it never rebuilds the list.
                    LazyVStack(alignment: .leading, spacing: 4) {
                        ForEach(store.events) { event in TranscriptEventRowView(event: event) }
                        Color.clear.frame(height: 1).id("log-bottom")
                    }
                    .padding(8)
                    .background(GeometryReader { geo in
                        Color.clear.preference(key: LogScrollOffsetKey.self, value: geo.frame(in: .named("logScroll")).maxY)
                    })
                }
                .coordinateSpace(name: "logScroll")
                .onPreferenceChange(LogScrollOffsetKey.self) { maxY in
                    // Only a scroll near the bottom keeps following; scrolling
                    // up to read earlier output must not be interrupted by append.
                    let frameHeight = 320.0
                    store.follow = maxY <= frameHeight + 40
                }
                .onChange(of: store.events.count) { _, _ in
                    if store.follow { withAnimation(.default) { proxy.scrollTo("log-bottom", anchor: .bottom) } }
                }
                .onAppear { proxy.scrollTo("log-bottom", anchor: .bottom) }
            }
            .frame(minHeight: 200, maxHeight: 320)
            .background(.quaternary.opacity(0.3), in: RoundedRectangle(cornerRadius: 6))
        }
    }

    private func pollLoop() async {
        await load()
        while !Task.isCancelled {
            do { try await Task.sleep(for: .seconds(store.live ? 1 : 5)) } catch { break }
            if Task.isCancelled { break }
            await load()
        }
    }

    private func load() async {
        guard model.summary.connected else { return }
        var args = ["transcript", taskId, "--after", String(store.requestAfter)]
        if let epoch = store.requestEpoch { args += ["--epoch", String(epoch)] }
        do {
            let data = try await model.request(args)
            let view = try decoder().decode(TranscriptView.self, from: data)
            guard !Task.isCancelled else { return }
            store.apply(view)
        } catch {
            guard !Task.isCancelled else { return }
            store.loadError = error.localizedDescription
        }
    }
}

private struct LogScrollOffsetKey {
    static var defaultValue: CGFloat = .infinity
    static func reduce(value: inout CGFloat, nextValue: () -> CGFloat) { value = nextValue() }
}
extension LogScrollOffsetKey: PreferenceKey {}

/// Renders one transcript event as a recognizable row: plain message, a
/// tool call/result pair, or a backend error/gap marker.
private struct TranscriptEventRowView: View {
    let event: TranscriptEventRow
    var body: some View {
        HStack(alignment: .top, spacing: 6) {
            Image(systemName: symbol).foregroundStyle(tint).frame(width: 14)
            VStack(alignment: .leading, spacing: 2) {
                if let text = event.text, !text.isEmpty { Text(text) }
                if event.type == "tool_call" {
                    Text((event.toolName ?? "tool") + (event.arguments.map { ": \($0)" } ?? "")).lineLimit(3)
                }
                if event.type == "tool_result", let output = event.output {
                    Text(output).lineLimit(6)
                }
                if event.type == "backend_error" || event.type == "gap", let reason = event.reason {
                    Text(reason)
                }
            }
            Spacer()
        }
        .font(.caption.monospaced())
        .foregroundStyle(event.isError == true ? Color.orange : Color.primary)
        .textSelection(.enabled)
    }
    private var symbol: String {
        switch event.type {
        case "tool_call": return "wrench.and.screwdriver"
        case "tool_result": return (event.isError ?? false) ? "xmark.circle" : "checkmark.circle"
        case "backend_error": return "exclamationmark.triangle"
        case "gap": return "ellipsis.circle"
        case "execution_end": return "flag.checkered"
        default: return "text.bubble"
        }
    }
    private var tint: Color {
        switch event.type {
        case "backend_error": return .orange
        case "tool_result": return (event.isError ?? false) ? .orange : .secondary
        default: return .secondary
        }
    }
}

extension TranscriptStore {
    var statusLine: String {
        if let reason, status != "available" { return reason }
        return ""
    }
    var emptyStateText: String {
        switch status {
        case "no_output_yet": return live ? "Running; no output reported yet." : "No output was captured for this execution."
        case "no_content": return "The execution ended without capturable content."
        case "not_captured": return "Transcript capture was not enabled for this execution."
        case "unsupported": return reason ?? "This Offload version does not support transcript capture."
        case "expired": return "The transcript was removed by retention."
        default: return reason ?? "Output unavailable."
        }
    }
}
