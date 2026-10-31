import XCTest
@testable import ClawFlow

final class PopoverTests: XCTestCase {
    private let now = Date()
    private func iso(_ d: Date) -> String { ISO8601DateFormatter().string(from: d) }
    private func task(_ id: String, repo: String = "o/a", stage: String, state: String, worker: String = "", wait: String? = nil, error: String? = nil) -> String {
        let w = wait.map { ",\"waiting_reason\":\"\($0)\"" } ?? ""
        let e = error.map { ",\"application_error\":\"\($0)\"" } ?? ""
        return "{\"id\":\"\(id)\",\"repo\":\"\(repo)\",\"issue\":\(id.hashValue & 0xff + 1),\"pr\":0,\"kind\":\"implement\",\"state\":\"\(state)\",\"stage\":\"\(stage)\",\"worker\":\"\(worker)\",\"attempt\":0,\"updated_at\":\"\(iso(now))\",\"tags\":[]\(w)\(e)}"
    }
    private func worker(_ name: String, live: Bool = true, paused: Bool = false, active: Int = 0, max: Int = 1) -> String {
        let running = (0..<active).map { "\"r\($0)\"" }.joined(separator: ",")
        return "{\"name\":\"\(name)\",\"tags\":[],\"roles\":[\"operator\"],\"live\":\(live),\"paused\":\(paused),\"revoked\":false,\"max_parallel\":\(max),\"running_tasks\":[\(running)]}"
    }
    private func repo(_ name: String, enabled: Bool) -> String {
        "{\"name\":\"\(name)\",\"url\":\"https://github.com/\(name)\",\"platform\":\"github\",\"enabled\":\(enabled),\"ci_required\":true,\"acceptance_required\":false,\"auto_merge\":false,\"worker_tags\":[]}"
    }
    private func snapshot(tasks: [String] = [], workers: [String] = [], repos: [String]? = nil, at: Date? = nil) throws -> DashboardSnapshot {
        let repos = repos ?? [repo("o/a", enabled: true), repo("o/b", enabled: false)]
        let project = repos.isEmpty ? "[]" : "[{\"name\":\"App\",\"repos\":[\(repos.joined(separator: ","))]}]"
        let json = "{\"schema_version\":1,\"version\":\"t\",\"controller\":\"mini\",\"board\":{\"generated_at\":\"\(iso(at ?? now))\",\"workers\":[\(workers.joined(separator: ","))],\"tasks\":[\(tasks.joined(separator: ","))]},\"projects\":\(project)}"
        return try DashboardSnapshot.decode(Data(json.utf8))
    }
    private func make(_ s: DashboardSnapshot?, received: Date? = nil, error: String? = nil, at: Date? = nil) -> StatusPopover {
        .make(snapshot: s, received: s == nil ? nil : (received ?? now), error: error, now: at ?? now)
    }

    func testIdleWithReadyWorker() throws {
        let p = make(try snapshot(workers: [worker("w")]))
        XCTAssertEqual(p.summary.kind, .idle); XCTAssertNil(p.summary.menuText)
        XCTAssertEqual(p.summary.accessibilityLabel, "ClawFlow: idle, nothing running or queued")
        XCTAssertTrue(p.explanation.hasPrefix("Idle: nothing running or queued."))
        XCTAssertEqual(p.workerLine, "Workers: 1 of 1 ready")
    }
    func testRunningShowsCountAndWorker() throws {
        let p = make(try snapshot(tasks: [task("a", stage: "running", state: "leased", worker: "w")], workers: [worker("w", active: 1, max: 2)]))
        XCTAssertEqual(p.summary.kind, .running); XCTAssertEqual(p.summary.menuText, "1")
        XCTAssertEqual(p.running.total, 1); XCTAssertTrue(p.running.items[0].detail.contains("w"))
        XCTAssertTrue(p.summary.accessibilityLabel.contains("1 running"))
    }
    func testQueuedExplainsNoReadyWorker() throws {
        let t = [task("a", stage: "pending", state: "pending")]
        let none = make(try snapshot(tasks: t, workers: [worker("w", live: false)]))
        XCTAssertEqual(none.summary.kind, .waiting); XCTAssertEqual(none.summary.menuText, "1")
        XCTAssertTrue(none.explanation.contains("no worker is ready")); XCTAssertTrue(none.workerLine.contains("1 offline"))
        let full = make(try snapshot(tasks: t, workers: [worker("w", active: 1, max: 1)]))
        XCTAssertTrue(full.explanation.contains("at capacity"))
        let drained = make(try snapshot(tasks: t, workers: [worker("w", paused: true)]))
        XCTAssertTrue(drained.workerLine.contains("not accepting new assignments"))
        XCTAssertTrue(drained.explanation.contains("no worker is ready"))
        let reason = make(try snapshot(tasks: [task("a", stage: "pending", state: "pending", wait: "Waiting for CI")], workers: [worker("w")]))
        XCTAssertTrue(reason.explanation.contains("Waiting for CI"))
    }
    func testAttentionOutranksRunningAndShowsReason() throws {
        let p = make(try snapshot(tasks: [task("a", stage: "lease_expired", state: "leased"), task("b", stage: "running", state: "leased", worker: "w"), task("c", stage: "pending", state: "pending")], workers: [worker("w")]))
        XCTAssertEqual(p.summary.kind, .attention); XCTAssertEqual(p.summary.title, "Needs attention")
        XCTAssertEqual(p.summary.menuText, "1"); XCTAssertEqual(p.summary.menuSymbol, "exclamationmark")
        XCTAssertEqual(p.summary.accessibilityLabel, "ClawFlow: needs attention, 1 need attention, 1 running, 1 queued")
        XCTAssertTrue(p.blockers.items[0].detail.contains("lease expired"))
    }
    func testNeverConnectedAndUnavailableAreUnknownNotZero() {
        let none = make(nil)
        XCTAssertEqual(none.summary.kind, .unknown); XCTAssertEqual(none.summary.count, "–")
        XCTAssertNil(none.summary.running); XCTAssertNil(none.summary.menuText)
        XCTAssertFalse(none.hasData); XCTAssertFalse(none.controlsEnabled)
        XCTAssertTrue(none.explanation.contains("unknown, not idle")); XCTAssertEqual(none.workerLine, "Workers: unknown")
        let down = StatusPopover.make(snapshot: nil, received: nil, error: "connection refused\nmore", now: now)
        XCTAssertEqual(down.summary.kind, .unavailable); XCTAssertEqual(down.detail, "connection refused")
        XCTAssertTrue(down.summary.accessibilityLabel.contains("status unknown"))
    }
    func testDisconnectKeepsLastKnownWithAgeAndDisablesControls() throws {
        let received = now.addingTimeInterval(-185)
        let s = try snapshot(tasks: [task("a", stage: "running", state: "leased", worker: "w")], workers: [worker("w")], at: received)
        let p = make(s, received: received, error: "offline")
        XCTAssertEqual(p.summary.kind, .stale); XCTAssertTrue(p.isStale)
        XCTAssertNil(p.summary.running); XCTAssertEqual(p.summary.count, "–"); XCTAssertNil(p.summary.menuText)
        XCTAssertEqual(p.summary.menuSymbol, "slash.circle")
        XCTAssertEqual(p.freshness, "Last known data from 3 min ago. It may have changed.")
        XCTAssertEqual(p.running.total, 1); XCTAssertFalse(p.controlsEnabled)
        XCTAssertTrue(p.summary.accessibilityLabel.contains("last known"))
    }
    func testOldSnapshotWithoutErrorIsStale() throws {
        let received = now.addingTimeInterval(-25)
        XCTAssertTrue(make(try snapshot(at: received), received: received).isStale)
    }
    func testNoProjects() throws {
        let p = make(try snapshot(repos: []))
        XCTAssertEqual(p.repoCount, 0); XCTAssertEqual(p.projectNote, "No projects configured.")
        XCTAssertTrue(p.explanation.contains("no repositories are configured"))
    }
    func testMixedEnabledAndPausedProjectsKeepPoliciesSeparate() throws {
        let t = [task("a", repo: "o/a", stage: "running", state: "leased", worker: "w"), task("b", repo: "o/b", stage: "pending", state: "pending"), task("c", repo: "o/b", stage: "done", state: "done")]
        let p = make(try snapshot(tasks: t, workers: [worker("w")]))
        XCTAssertEqual(p.monitoring.map(\.name), ["o/a"]); XCTAssertEqual(p.paused.map(\.name), ["o/b"])
        XCTAssertEqual(p.monitoring[0].stateLabel, "Monitoring"); XCTAssertEqual(p.paused[0].stateLabel, "Monitoring paused")
        XCTAssertEqual(p.monitoring[0].counts, "1 running · 0 queued")
        XCTAssertEqual(p.paused[0].counts, "0 running · 1 queued")   // queued work continues; history is not counted
        XCTAssertEqual(p.monitoring[0].policies, "CI required · Acceptance optional · Auto-merge off")
        XCTAssertTrue(p.paused[0].semantics.contains("Queued and running tasks continue"))
        XCTAssertTrue(p.paused[0].semantics.contains("worker availability is separate"))
        XCTAssertTrue(p.monitoring[0].semantics.contains("not stopped"))
        XCTAssertTrue(p.paused[0].accessibilityLabel.contains("Monitoring paused"))
        XCTAssertTrue(p.controlsEnabled)
    }
    func testAllPausedWithReadyWorkerExplainsIdle() throws {
        let p = make(try snapshot(workers: [worker("w")], repos: [repo("o/a", enabled: false)]))
        XCTAssertTrue(p.explanation.contains("monitoring is paused for every repository"))
        XCTAssertEqual(p.workerLine, "Workers: 1 of 1 ready")   // pause is not worker unavailability
    }
    func testManyProjectsAndTasksAreBounded() throws {
        let tasks = (0..<10).map { task("t\($0)", stage: "pending", state: "pending") }
        let repos = (0..<40).map { repo("o/r\($0)", enabled: $0 % 2 == 0) }
        let p = make(try snapshot(tasks: tasks, workers: [worker("w")], repos: repos))
        XCTAssertEqual(p.queued.items.count, StatusPopover.taskLimit); XCTAssertEqual(p.queued.total, 10); XCTAssertEqual(p.queued.hidden, 6)
        XCTAssertEqual(p.repoCount, 40); XCTAssertEqual(p.monitoring.count, 20)
    }
    func testPendingActionTextNamesTheActualChange() {
        XCTAssertEqual(PendingAction(action: "disable", name: "o/a").text, "Pausing monitoring for o/a…")
        XCTAssertEqual(PendingAction(action: "enable", name: "o/a").text, "Resuming monitoring for o/a…")
    }
    func testAgeText() {
        XCTAssertEqual(ageText(from: now, now: now), "just now")
        XCTAssertEqual(ageText(from: now.addingTimeInterval(-30), now: now), "30s ago")
        XCTAssertEqual(ageText(from: now.addingTimeInterval(-7200), now: now), "2 h ago")
    }
}
