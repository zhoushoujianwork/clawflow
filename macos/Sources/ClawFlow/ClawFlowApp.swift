import AppKit
import SwiftUI

final class AppDelegate: NSObject, NSApplicationDelegate {
    func applicationDidFinishLaunching(_ notification: Notification) {
        NSApp.setActivationPolicy(.accessory)
    }
    func applicationShouldTerminateAfterLastWindowClosed(_ sender: NSApplication) -> Bool { false }
}

@main struct ClawFlowApp: App {
    @NSApplicationDelegateAdaptor(AppDelegate.self) private var delegate
    @StateObject private var model = DashboardModel()
    var body: some Scene {
        Window("ClawFlow", id: "dashboard") {
            DashboardView(model: model).task { model.start() }
        }
        .defaultSize(width: 1060, height: 680)
        .commands { CommandGroup(replacing: .newItem) {} }
        Settings { ConnectionView(model: model) }
        MenuBarExtra {
            MenuSummary(model: model).task { model.start() }
        } label: {
            MenuBarBrand(summary: model.summary, offloadAttention: model.offloadNeedsAttention)
        }
        .menuBarExtraStyle(.window)
    }
}

struct MenuSummary: View {
    @ObservedObject var model: DashboardModel
    @Environment(\.openWindow) private var openWindow
    var body: some View {
        let status = model.popover
        VStack(alignment: .leading, spacing: 10) {
            header(status)
            if model.offloadNeedsAttention, let readiness = model.executionReadiness.status {
                Label(readiness.summary, systemImage: "exclamationmark.triangle").font(.caption).foregroundStyle(.orange)
                Text("Open Settings for setup steps, then recheck.").font(.caption).foregroundStyle(.secondary)
            }
            ScrollView {
                VStack(alignment: .leading, spacing: 12) {
                    if status.hasData {
                        taskGroup("Needs attention", symbol: "exclamationmark.triangle", section: status.blockers)
                        taskGroup("Running", symbol: "arrow.triangle.2.circlepath", section: status.running)
                        taskGroup("Queued", symbol: "hourglass", section: status.queued)
                        projects(status)
                    }
                }.frame(maxWidth: .infinity, alignment: .leading)
            }.frame(maxHeight: 360).accessibilityLabel("Status details")
            if let pending = model.pendingAction {
                HStack(spacing: 6) { ProgressView().controlSize(.small); Text(pending.text) }.font(.caption)
            }
            if let error = model.actionError { Text(error).font(.caption).foregroundStyle(.orange).fixedSize(horizontal: false, vertical: true) }
            DisclosureGroup("On this Mac") { LocalServiceControls(services: model.localServices, readiness: model.executionReadiness).padding(.top, 6) }
                .font(.callout)
            Divider()
            HStack {
                Button("Open Dashboard") { openWindow(id: "dashboard"); NSApp.activate(ignoringOtherApps: true) }
                    .keyboardShortcut("d")
                Spacer()
                SettingsLink()
                Button("Quit App") { NSApp.terminate(nil) }
            }
            Text("Quitting this app does not stop the controller or workers.").font(.caption2).foregroundStyle(.secondary)
        }.padding(14).frame(width: 380)
    }

    private func header(_ status: StatusPopover) -> some View {
        VStack(alignment: .leading, spacing: 4) {
            Label(status.summary.title, systemImage: status.summary.symbol).font(.headline)
            Text(status.explanation).font(.callout).fixedSize(horizontal: false, vertical: true)
            Text(status.connectivity + (status.freshness.map { " · " + $0 } ?? "")).font(.caption).foregroundStyle(.secondary)
            if let detail = status.detail { Text(detail).font(.caption).foregroundStyle(.orange).lineLimit(2) }
            Text(status.workerLine + (status.isStale ? " (last known)" : "")).font(.caption).foregroundStyle(.secondary)
        }
        .accessibilityElement(children: .combine)
    }

    @ViewBuilder private func taskGroup(_ title: String, symbol: String, section: PopoverSection) -> some View {
        if section.total > 0 {
            VStack(alignment: .leading, spacing: 4) {
                Label("\(title) · \(section.total)\(model.popover.isStale ? " (last known)" : "")", systemImage: symbol).font(.subheadline.weight(.semibold))
                ForEach(section.items) { item in
                    VStack(alignment: .leading, spacing: 1) {
                        if let url = item.url { Link(item.title, destination: url).lineLimit(2) } else { Text(item.title).lineLimit(2) }
                        Text(item.detail).font(.caption).foregroundStyle(.secondary).lineLimit(2)
                    }.accessibilityElement(children: .combine).accessibilityLabel(item.accessibilityLabel)
                }
                if section.hidden > 0 { Text("+\(section.hidden) more in the dashboard").font(.caption).foregroundStyle(.secondary) }
            }
        }
    }

    @ViewBuilder private func projects(_ status: StatusPopover) -> some View {
        VStack(alignment: .leading, spacing: 6) {
            Label("Projects · \(status.monitoring.count) monitoring · \(status.paused.count) paused", systemImage: "folder").font(.subheadline.weight(.semibold))
            if status.repoCount == 0 { Text(status.projectNote).font(.caption).foregroundStyle(.secondary) }
            ForEach(status.monitoring) { repoRow($0, status: status) }
            ForEach(status.paused) { repoRow($0, status: status) }
            if status.repoCount > 0 { Text(status.projectNote).font(.caption2).foregroundStyle(.secondary) }
        }
    }

    private func repoRow(_ repo: PopoverRepo, status: StatusPopover) -> some View {
        VStack(alignment: .leading, spacing: 2) {
            HStack {
                Image(systemName: repo.monitoring ? "eye" : "pause.circle")
                if let url = repo.url { Link(repo.name, destination: url) } else { Text(repo.name) }
                Spacer()
                Text(repo.stateLabel).font(.caption.weight(.medium))
                Button(repo.monitoring ? "Pause" : "Resume") { Task { await model.perform(repo.monitoring ? "disable" : "enable", name: repo.name) } }
                    .controlSize(.small)
                    .disabled(!status.controlsEnabled || model.busy)
                    .help(repo.semantics)
                    .accessibilityLabel(repo.monitoring ? "Pause monitoring for \(repo.name)" : "Resume monitoring for \(repo.name)")
                    .accessibilityHint(repo.semantics)
            }
            Text(repo.counts + (status.isStale ? " (last known)" : "")).font(.caption).foregroundStyle(.secondary)
            Text(repo.policies).font(.caption2).foregroundStyle(.secondary)
        }.accessibilityElement(children: .contain).padding(.vertical, 2)
    }
}
