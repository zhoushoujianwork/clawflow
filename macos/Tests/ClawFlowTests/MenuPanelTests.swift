import AppKit
import SwiftUI
import XCTest
@testable import ClawFlow

final class MenuPanelTests: XCTestCase {
    private let now = Date(timeIntervalSince1970: 1_793_900_000)

    func testScopeCountsStatusesAndFallback() throws {
        let snapshot = try fixture(tasks: [
            task("run-a", repo: "acme/app", state: "leased", stage: "running"),
            task("wait-a", repo: "acme/app", state: "pending", stage: "pending"),
            task("bad-b", repo: "acme/api", state: "leased", stage: "lease_expired")
        ])
        let all = panel(snapshot)
        XCTAssertEqual(all.status, "需要关注 1")
        XCTAssertEqual(all.running.total, 1)
        XCTAssertEqual(all.queue?.waiting, 1)

        let app = panel(snapshot, scope: "acme/app")
        XCTAssertEqual(app.status, "运行中 1")
        XCTAssertEqual(app.attention.total, 0)
        XCTAssertEqual(app.repository?.name, "acme/app")

        let fallback = panel(snapshot, scope: "missing/repo")
        XCTAssertEqual(fallback.scopeID, "")
        XCTAssertNil(fallback.repository)
        XCTAssertEqual(fallback.tabs.map(\.fullName), ["全部项目", "acme/api", "acme/app", "acme/web"])
    }

    func testIdleRunningAttentionDisconnectedAndNoDataText() throws {
        XCTAssertEqual(panel(try fixture()).status, "空闲")
        XCTAssertEqual(panel(try fixture(tasks: [task("r", state: "leased", stage: "running")])).status, "运行中 1")
        XCTAssertEqual(panel(try fixture(tasks: [task("a", state: "leased", stage: "recovery_pending")])).status, "需要关注 1")
        let disconnected = MenuPanelPresentation.make(snapshot: try fixture(), received: now, error: "offline", now: now, selectedScope: "")
        XCTAssertEqual(disconnected.status, "连接中断")
        XCTAssertFalse(disconnected.connected)
        let noData = MenuPanelPresentation.make(snapshot: nil, received: nil, error: nil, now: now, selectedScope: "")
        XCTAssertEqual(noData.status, "没有数据")
        XCTAssertFalse(noData.hasSnapshot)
    }

    func testTodaySuccessTaskLimitAndMissingCost() throws {
        var tasks = (0..<6).map { task("attention-\($0)", state: "leased", stage: "lease_expired") }
        tasks += [task("done", state: "done", stage: "done"), task("failed", state: "failed", stage: "failed")]
        let value = panel(try fixture(tasks: tasks))
        XCTAssertEqual(value.today?.done, 1)
        XCTAssertEqual(value.today?.failed, 1)
        XCTAssertEqual(value.today?.success, 50)
        XCTAssertEqual(value.attention.items.count, 4)
        XCTAssertEqual(value.attention.hidden, 2)
        XCTAssertNil(value.cost)
    }

    func testSingleProjectCapacityUsesMatchingWorkerTags() throws {
        let snapshot = try fixture(
            tasks: [task("r", repo: "acme/app", state: "leased", stage: "running")],
            workers: [worker("mac", tags: ["host-mac"], slots: 3, running: ["r"]),
                      worker("linux", tags: ["host-linux"], slots: 4)])
        let value = panel(snapshot, scope: "acme/app")
        XCTAssertEqual(value.capacity?.running, 1)
        XCTAssertEqual(value.capacity?.slots, 3)
        XCTAssertEqual(value.capacity?.online, 1)
        XCTAssertEqual(value.capacity?.workers, 1)
    }

    func testQueueReasonKeepsExplicitAndCapacityExplanations() throws {
        let explicit = panel(try fixture(tasks: [task("q", state: "pending", stage: "pending", wait: "等待 CI")]))
        XCTAssertEqual(explicit.queue?.reason, "等待 CI")

        let offlineWorker: [[String: Any]] = [["name": "off", "tags": ["host-mac"], "roles": ["operator"],
                                               "live": false, "paused": false, "revoked": false,
                                               "max_parallel": 1, "running_tasks": []]]
        let noWorker = panel(try fixture(tasks: [task("q", state: "pending", stage: "pending")], workers: offlineWorker), scope: "acme/app")
        XCTAssertEqual(noWorker.queue?.reason, "没有可接单的 Worker")

        let full = panel(try fixture(tasks: [task("q", state: "pending", stage: "pending")],
                                     workers: [worker("mac", tags: ["host-mac"], slots: 1, running: ["other"])]), scope: "acme/app")
        XCTAssertEqual(full.queue?.reason, "Worker 容量已满")
    }

    func testPendingActionTextStillExplainsMonitoringSemantics() {
        XCTAssertEqual(PendingAction(action: "disable", name: "acme/app").textZh, "正在暂停监控 acme/app…")
        XCTAssertEqual(PendingAction(action: "enable", name: "acme/app").textZh, "正在恢复监控 acme/app…")
    }

    @MainActor func testRenderMenuPanelFixturesWhenRequested() throws {
        guard let directory = ProcessInfo.processInfo.environment["CLAWFLOW_MENU_SNAPSHOT_DIR"], !directory.isEmpty else {
            throw XCTSkip("Set CLAWFLOW_MENU_SNAPSHOT_DIR to render menu snapshots")
        }
        try FileManager.default.createDirectory(atPath: directory, withIntermediateDirectories: true)
        let mixed = try fixture(
            tasks: [
                task("running-app", repo: "acme/app", state: "leased", stage: "running", subject: "改进状态面板"),
                task("running-api", repo: "acme/api", state: "leased", stage: "running", subject: "缩短队列延迟"),
                task("attention", repo: "acme/api", state: "leased", stage: "lease_expired", subject: "恢复中断任务"),
                task("waiting", repo: "acme/web", state: "pending", stage: "pending", subject: "更新设置页面", wait: "等待 host-web 标签的 Worker")
            ],
            workers: [worker("mac", tags: ["host-mac"], slots: 3, running: ["running-app"]),
                      worker("linux", tags: ["host-linux"], slots: 2, running: ["running-api"])])
        let paused = try fixture(enabled: ["acme/app": true, "acme/api": true, "acme/web": false])
        let cases: [(String, MenuPanelPresentation)] = [
            ("all-idle", panel(try fixture())),
            ("all-running", panel(mixed)),
            ("single-project", panel(mixed, scope: "acme/app")),
            ("single-paused-project", panel(paused, scope: "acme/web")),
            ("disconnected", MenuPanelPresentation.make(snapshot: mixed, received: now.addingTimeInterval(-90), error: "Controller 连接失败", now: now, selectedScope: "")),
            ("no-data", MenuPanelPresentation.make(snapshot: nil, received: nil, error: nil, now: now, selectedScope: ""))
        ]
        for (name, presentation) in cases {
            for scheme in [ColorScheme.light, .dark] {
                let suffix = scheme == .light ? "light" : "dark"
                let view = MenuPanelView(presentation: presentation, snapshotRendering: true)
                    .environment(\.colorScheme, scheme)
                    .fixedSize(horizontal: false, vertical: true)
                let renderer = ImageRenderer(content: view)
                renderer.scale = 2
                guard let image = renderer.nsImage,
                      let tiff = image.tiffRepresentation,
                      let bitmap = NSBitmapImageRep(data: tiff),
                      let png = bitmap.representation(using: .png, properties: [:]) else {
                    return XCTFail("Could not render \(name)-\(suffix)")
                }
                try png.write(to: URL(fileURLWithPath: directory).appendingPathComponent("\(name)-\(suffix).png"))
            }
        }
    }

    private func panel(_ snapshot: DashboardSnapshot, scope: String = "") -> MenuPanelPresentation {
        MenuPanelPresentation.make(snapshot: snapshot, received: now, error: nil, now: now, selectedScope: scope)
    }

    private func fixture(tasks: [[String: Any]] = [], workers: [[String: Any]]? = nil,
                         enabled: [String: Bool] = [:]) throws -> DashboardSnapshot {
        let repos = [
            repo("acme/app", enabled: enabled["acme/app"] ?? true, tags: ["host-mac"]),
            repo("acme/api", enabled: enabled["acme/api"] ?? true, tags: ["host-linux"]),
            repo("acme/web", enabled: enabled["acme/web"] ?? false, tags: ["host-web"])
        ]
        let boardWorkers = workers ?? [worker("mac", tags: ["host-mac", "host-web"], slots: 3)]
        let object: [String: Any] = [
            "schema_version": 1, "version": "fixture", "controller": "本机 Controller",
            "board": ["generated_at": iso(now), "workers": boardWorkers, "tasks": tasks],
            "projects": [["name": "ClawFlow", "repos": repos]]
        ]
        return try DashboardSnapshot.decode(JSONSerialization.data(withJSONObject: object))
    }

    private func repo(_ name: String, enabled: Bool, tags: [String]) -> [String: Any] {
        ["name": name, "url": "https://github.com/\(name)", "platform": "github", "enabled": enabled,
         "ci_required": true, "acceptance_required": true, "auto_merge": false, "worker_tags": tags]
    }
    private func worker(_ name: String, tags: [String], slots: Int, running: [String] = []) -> [String: Any] {
        ["name": name, "tags": tags, "roles": ["operator"], "live": true, "paused": false,
         "revoked": false, "max_parallel": slots, "running_tasks": running]
    }
    private func task(_ id: String, repo: String = "acme/app", state: String, stage: String,
                      subject: String = "任务标题", wait: String? = nil) -> [String: Any] {
        var value: [String: Any] = [
            "id": id, "repo": repo, "issue": abs(id.hashValue % 900) + 1, "pr": 0,
            "subject": subject, "kind": "implement", "state": state, "stage": stage,
            "worker": stage == "running" ? "mac" : "", "attempt": 1, "tags": [],
            "started_at": iso(now.addingTimeInterval(-754)), "updated_at": iso(now)
        ]
        if ["done", "failed", "cancelled"].contains(state) { value["ended_at"] = iso(now.addingTimeInterval(-30)) }
        if let wait { value["waiting_reason"] = wait }
        return value
    }
    private func iso(_ date: Date) -> String { ISO8601DateFormatter().string(from: date) }
}
