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
            MenuBarBrand(summary: model.summary)
        }
        .menuBarExtraStyle(.window)
    }
}

struct MenuSummary: View {
    @ObservedObject var model: DashboardModel
    @Environment(\.openWindow) private var openWindow
    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            Label(model.summary.title, systemImage: model.summary.symbol).font(.headline)
            if let snapshot = model.snapshot {
                Text("\(snapshot.board.tasks.filter { $0.group == .attention }.count) attention · \(snapshot.board.tasks.filter { $0.group == .running }.count) running · \(snapshot.board.tasks.filter { $0.group == .waiting }.count) waiting")
                Text("\(snapshot.board.workers.filter(\.live).count) of \(snapshot.board.workers.count) workers online").foregroundStyle(.secondary)
                if let received = model.received { Text("Updated \(received, style: .relative) ago").font(.caption).foregroundStyle(.secondary) }
            }
            Button("Open Dashboard") { openWindow(id: "dashboard"); NSApp.activate(ignoringOtherApps: true) }
                .buttonStyle(.borderedProminent)
            Divider()
            LocalServiceControls(services: model.localServices)
            Divider()
            HStack {
                SettingsLink()
                Spacer()
                Button("Quit App") { NSApp.terminate(nil) }
            }
        }.padding(16).frame(width: 340)
    }
}
