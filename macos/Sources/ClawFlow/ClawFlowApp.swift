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
        .defaultSize(width: 1200, height: 760)
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
    @Environment(\.openSettings) private var openSettingsScene
    @AppStorage("menu.scope") private var selectedScope = ""
    var body: some View {
        let now = model.now
        let panel = MenuPanelPresentation.make(snapshot: model.snapshot, received: model.received, error: model.error,
                                               now: now, selectedScope: selectedScope)
        MenuPanelView(
            presentation: panel,
            offloadWarning: model.offloadNeedsAttention ? model.executionReadiness.status?.summary : nil,
            pendingText: model.pendingAction?.textZh,
            actionError: model.actionError,
            actions: MenuPanelActions(
                selectScope: { selectedScope = $0 },
                openURL: { NSWorkspace.shared.open($0) },
                openDashboard: { openWindow(id: "dashboard"); NSApp.activate(ignoringOtherApps: true) },
                toggleMonitoring: { name, monitoring in
                    Task { await model.perform(monitoring ? "disable" : "enable", name: name) }
                },
                checkOffload: openSettings,
                openSettings: openSettings,
                quit: { NSApp.terminate(nil) }),
            localServiceControls: AnyView(LocalServiceControls(services: model.localServices, readiness: model.executionReadiness, showsTitle: false)),
            maximumHeight: max(420, (NSScreen.main?.visibleFrame.height ?? 760) - 48))
        .onAppear { if selectedScope != panel.scopeID { selectedScope = panel.scopeID } }
        .onChange(of: panel.scopeID) { _, scope in if selectedScope != scope { selectedScope = scope } }
    }

    private func openSettings() {
        // The showSettingsWindow: selector is blocked since macOS 14; use the scene action.
        openSettingsScene()
        NSApp.activate(ignoringOtherApps: true)
    }
}
