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
        XCTAssertEqual(fallback.tabs.map(\.fullName), ["项目概览", "acme/api", "acme/app", "acme/web"])
    }

    func testIdleRunningAttentionDisconnectedAndNoDataText() throws {
        XCTAssertEqual(panel(try fixture()).status, "空闲")
        XCTAssertEqual(panel(try fixture(tasks: [task("r", state: "leased", stage: "running")])).status, "运行中 1")
        XCTAssertEqual(panel(try fixture(tasks: [task("a", state: "leased", stage: "recovery_pending")])).status, "需要关注 1")
        let disconnected = MenuPanelPresentation.make(snapshot: try fixture(), received: now, error: "offline", now: now, selectedScope: "")
        XCTAssertEqual(disconnected.status, "连接中断")
        XCTAssertFalse(disconnected.connected)
        XCTAssertTrue(disconnected.dimsLiveData)
        XCTAssertEqual(disconnected.capacity?.rightText, "最后已知")
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
        XCTAssertEqual(value.attention.items.count, 3)
        XCTAssertEqual(value.attention.hidden, 3)
        XCTAssertNil(value.cost)
    }

    func testOverviewLimitsAttentionAndRunningToThreeRowsTotal() throws {
        let tasks = (0..<2).map { task("attention-\($0)", state: "leased", stage: "lease_expired") }
            + (0..<4).map { task("running-\($0)", state: "leased", stage: "running") }
        let overview = panel(try fixture(tasks: tasks))
        XCTAssertEqual(overview.attention.items.count + overview.running.items.count, 3)
        XCTAssertEqual(overview.attention.hidden, 0)
        XCTAssertEqual(overview.running.items.count, 1)
        XCTAssertEqual(overview.running.hidden, 3)

        let project = panel(try fixture(tasks: tasks), scope: "acme/app")
        XCTAssertEqual(project.attention.items.count, 2)
        XCTAssertEqual(project.running.items.count, 4)
    }

    func testUsageScopesAndOlderControllerCompatibility() throws {
        let oldSnapshot = try fixture()
        XCTAssertNil(oldSnapshot.usage)
        XCTAssertNil(panel(oldSnapshot).cost)

        let snapshot = try fixture(usage: usageFixture())
        let overview = panel(snapshot)
        XCTAssertEqual(overview.scopeTitle, "概览")
        XCTAssertEqual(overview.cost?.todayUSD, 12.34)
        XCTAssertEqual(overview.cost?.todayTokens, 47_000_000)
        XCTAssertEqual(overview.cost?.sevenDaysUSD, 115.74)
        XCTAssertEqual(overview.cost?.sevenDaysTokens, 1_800_000_000)
        XCTAssertEqual(overview.cost?.thirtyDaysUSD, 166)
        XCTAssertEqual(overview.cost?.thirtyDaysTokens, 5_500_000_000)
        XCTAssertEqual(overview.cost?.daily.count, 30)
        XCTAssertEqual(overview.cost?.mostUsedModel, "gpt-6-astra")
        XCTAssertEqual(overview.cost?.estimated, true)
        XCTAssertEqual(overview.cost?.footer, "根据 token 用量估算 · 不是账单")
        XCTAssertEqual(overview.cost?.recent.map(\.title), ["今天", "昨天", "2 天前", "3 天前"])
        XCTAssertEqual(overview.cost?.recent[0].summary, "$12.34 · 47M")
        XCTAssertEqual(overview.cost?.recent[1].summary, "$0 · 0")
        XCTAssertEqual(overview.cost?.recent[0].date.isEmpty, false)

        let app = panel(snapshot, scope: "acme/app")
        XCTAssertEqual(app.cost?.todayUSD, 8.1)
        XCTAssertEqual(app.cost?.thirtyDaysUSD, 100)
    }

    func testRecentUsageFillsMissingCalendarDaysAndKnownFooter() throws {
        let snapshot = try fixture(usage: usageFixture(missingYesterday: true, estimated: false))
        let cost = try XCTUnwrap(panel(snapshot).cost)
        XCTAssertEqual(cost.recent.count, 4)
        XCTAssertEqual(cost.recent[1].title, "昨天")
        XCTAssertEqual(cost.recent[1].summary, "—")
        XCTAssertEqual(cost.recent[2].summary, "$30 · 60M")
        XCTAssertEqual(cost.footer, "根据运行记录汇总 · 不是账单")
        XCTAssertEqual(menuCurrency(78.77), "$78.77")
        XCTAssertEqual(menuTokens(1_500_000_000), "1.5B")
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

        let empty = panel(try fixture())
        XCTAssertEqual(empty.queue?.summary, "运行 0 · 等待 0")
        XCTAssertNil(empty.queue?.reason)
    }

    func testPausedProjectUsesPausedHeaderState() throws {
        let value = panel(try fixture(enabled: ["acme/web": false]), scope: "acme/web")
        XCTAssertEqual(value.status, "已暂停监控")
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
        let usage = usageFixture()
        let mixed = try fixture(
            tasks: [
                task("running-app", repo: "acme/app", state: "leased", stage: "running", subject: "改进状态面板"),
                task("running-api", repo: "acme/api", state: "leased", stage: "running", subject: "缩短队列延迟"),
                task("attention", repo: "acme/api", state: "leased", stage: "lease_expired", subject: "恢复中断任务"),
                task("waiting", repo: "acme/web", state: "pending", stage: "pending", subject: "更新设置页面", wait: "等待 host-web 标签的 Worker")
            ] + (1...7).map {
                task("done-\($0)", repo: "acme/app", state: "done", stage: "done", subject: "今日完成 \($0)")
            } + [
                task("failed-today", repo: "acme/api", state: "failed", stage: "failed", subject: "今日失败")
            ],
            workers: [worker("mac", tags: ["host-mac"], slots: 3, running: ["running-app"]),
                      worker("linux", tags: ["host-linux"], slots: 2, running: ["running-api"])],
            usage: usage)
        let paused = try fixture(enabled: ["acme/app": true, "acme/api": true, "acme/web": false], usage: usage)
        let longRepos = (1...7).map { repo("acme/very-long-project-\($0)", enabled: true, tags: []) }
        let many = try fixture(repositoryRows: longRepos, usage: usage)
        let services = LocalServices()
        var controller = LocalService(id: "com.zhoushoujianwork.clawflow.controller", path: "/tmp/controller.plist")
        controller.loaded = true; controller.running = true; controller.pid = "4242"
        services.rows = [controller]
        let readiness = ExecutionReadiness()
        let localControls = AnyView(LocalServiceControls(services: services, readiness: readiness, showsTitle: false))
        let cases: [(String, MenuPanelPresentation, Bool, AnyView)] = [
            ("all-idle", panel(try fixture(usage: usage)), false, AnyView(EmptyView())),
            ("all-running", panel(mixed), false, AnyView(EmptyView())),
            ("single-project", panel(mixed, scope: "acme/app"), false, AnyView(EmptyView())),
            ("single-paused-project", panel(paused, scope: "acme/web"), false, AnyView(EmptyView())),
            ("many-projects", panel(many, scope: "acme/very-long-project-7"), false, AnyView(EmptyView())),
            ("local-services-expanded", panel(try fixture(usage: usage)), true, localControls),
            ("disconnected", MenuPanelPresentation.make(snapshot: mixed, received: now.addingTimeInterval(-90), error: "Controller 连接失败", now: now, selectedScope: ""), false, AnyView(EmptyView())),
            ("no-cost-data", panel(try fixture()), false, AnyView(EmptyView())),
            ("no-data", MenuPanelPresentation.make(snapshot: nil, received: nil, error: nil, now: now, selectedScope: ""), false, AnyView(EmptyView()))
        ]
        for (name, presentation, expanded, controls) in cases {
            for scheme in [ColorScheme.light, .dark] {
                let suffix = scheme == .light ? "light" : "dark"
                let view = MenuPanelView(presentation: presentation, localServiceControls: controls,
                                         snapshotRendering: true, servicesInitiallyExpanded: expanded)
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
                         enabled: [String: Bool] = [:], repositoryRows: [[String: Any]]? = nil,
                         usage: [String: Any]? = nil) throws -> DashboardSnapshot {
        let defaultRepos = [
            repo("acme/app", enabled: enabled["acme/app"] ?? true, tags: ["host-mac"]),
            repo("acme/api", enabled: enabled["acme/api"] ?? true, tags: ["host-linux"]),
            repo("acme/web", enabled: enabled["acme/web"] ?? false, tags: ["host-web"])
        ]
        let boardWorkers = workers ?? [worker("mac", tags: ["host-mac", "host-web"], slots: 3)]
        var object: [String: Any] = [
            "schema_version": 1, "version": "fixture", "controller": "本机 Controller",
            "board": ["generated_at": iso(now), "workers": boardWorkers, "tasks": tasks],
            "projects": [["name": "ClawFlow", "repos": repositoryRows ?? defaultRepos]]
        ]
        if let usage { object["usage"] = usage }
        return try DashboardSnapshot.decode(JSONSerialization.data(withJSONObject: object))
    }

    private func usageFixture(missingYesterday: Bool = false, estimated: Bool = true) -> [String: Any] {
        let costs: [Double] = [0, 0, 0.2, 0, 0, 0.5, 0, 0, 1.1, 0, 0, 0, 2.2, 0, 0.6,
                               0, 0, 4.5, 12, 3.2, 0, 0.8, 25, 52, 0.4, 0, 21, 30, 0, 12.34]
        let calendar = Calendar(identifier: .gregorian)
        let start = calendar.date(byAdding: .day, value: -29, to: now)!
        let formatter = DateFormatter()
        formatter.locale = Locale(identifier: "en_US_POSIX")
        formatter.timeZone = TimeZone(secondsFromGMT: 0)
        formatter.dateFormat = "yyyy-MM-dd"
        func daily(scale: Double, todayCost: Double, todayTokens: Int64) -> [[String: Any]] {
            costs.enumerated().compactMap { index, cost in
                if missingYesterday && index == costs.count - 2 { return nil }
                return ["date": formatter.string(from: calendar.date(byAdding: .day, value: index, to: start)!),
                        "cost_usd": index == costs.count - 1 ? todayCost : cost * scale,
                        "total_tokens": index == costs.count - 1 ? todayTokens : Int64(cost * scale * 2_000_000)] as [String: Any]
            }
        }
        func aggregate(_ cost: Double, _ tokens: Int64) -> [String: Any] {
            ["cost_usd": cost, "total_tokens": tokens, "input_tokens": tokens / 2,
             "output_tokens": tokens / 4, "cache_read_input_tokens": tokens / 4,
             "cache_creation_input_tokens": 0, "cost_is_estimate": estimated]
        }
        func scope(todayCost: Double, todayTokens: Int64, cost: Double, tokens: Int64,
                   model: String = "gpt-6-astra") -> [String: Any] {
            let ratio = cost / 166
            return ["today": aggregate(todayCost, todayTokens),
                    "seven_days": aggregate(115.74 * ratio, Int64(Double(tokens) * 1_800_000_000 / 5_500_000_000)),
                    "thirty_days": aggregate(cost, tokens),
                    "daily": daily(scale: ratio, todayCost: todayCost, todayTokens: todayTokens), "most_used_model": model]
        }
        return [
            "generated_at": iso(now),
            "total": scope(todayCost: 12.34, todayTokens: 47_000_000, cost: 166, tokens: 5_500_000_000),
            "by_repo": [
                "acme/app": scope(todayCost: 8.1, todayTokens: 32_000_000, cost: 100, tokens: 3_000_000_000),
                "acme/api": scope(todayCost: 4.24, todayTokens: 15_000_000, cost: 50, tokens: 2_000_000_000),
                "acme/web": scope(todayCost: 0, todayTokens: 0, cost: 16, tokens: 500_000_000, model: "gpt-6.1-sol")
            ]
        ]
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
