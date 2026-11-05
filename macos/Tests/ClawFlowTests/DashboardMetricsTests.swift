import XCTest
@testable import ClawFlow

final class DashboardMetricsTests: XCTestCase {
    private var calendar: Calendar = {
        var c = Calendar(identifier: .gregorian); c.timeZone = TimeZone(identifier: "UTC")!; return c
    }()
    private let generated = "2026-11-04T12:00:00Z"
    private func job(_ id: String, repo: String = "o/r", kind: String = "implement", state: String = "done", updated: String = "2026-11-04T10:00:00Z",
                     started: String? = nil, ended: String? = nil, stage: String? = nil) -> [String: Any] {
        var value: [String: Any] = ["id": id, "repo": repo, "issue": 1, "pr": 0, "kind": kind, "state": state, "stage": stage ?? state,
                                    "worker": "w", "attempt": 0, "updated_at": updated, "tags": []]
        if let started { value["started_at"] = started }
        if let ended { value["ended_at"] = ended }
        return value
    }
    private func snapshot(_ tasks: [[String: Any]], workers: [[String: Any]] = []) throws -> DashboardSnapshot {
        let fixture: [String: Any] = ["schema_version": 1, "version": "t", "controller": "c",
            "board": ["generated_at": generated, "workers": workers, "tasks": tasks], "projects": []]
        return try DashboardSnapshot.decode(JSONSerialization.data(withJSONObject: fixture))
    }
    private func metrics(_ value: DashboardSnapshot, repo: String? = nil) -> DashboardMetrics {
        let tasks = value.board.tasks.filter { repo == nil || $0.repo == repo }
        return DashboardMetrics.make(tasks: tasks, pipelines: value.issuePipelines.filter { repo == nil || $0.repo == repo },
                                     now: value.board.generatedAt, calendar: calendar)
    }

    func testTodayCountsUseSnapshotDayNotWallClock() throws {
        let value = try snapshot([
            job("a"), job("b", state: "failed"), job("old", updated: "2026-11-03T23:59:00Z"),
            job("run", state: "leased", stage: "running"),
        ])
        let m = metrics(value)
        XCTAssertEqual(m.jobCount, 4)
        XCTAssertEqual(m.todayDone, 1)
        XCTAssertEqual(m.todayFailed, 1)
    }

    func testSuccessRateMedianAndRepairs() throws {
        let value = try snapshot([
            job("a", started: "2026-11-04T09:00:00Z", ended: "2026-11-04T09:10:00Z"),
            job("b", started: "2026-11-04T09:00:00Z", ended: "2026-11-04T09:30:00Z"),
            job("c", started: "2026-11-04T09:00:00Z", ended: "2026-11-04T09:20:00Z"),
            job("f", state: "failed"),
            job("r", kind: "repair"), job("r2", kind: "repair", state: "cancelled"),
        ])
        let m = metrics(value)
        XCTAssertEqual(m.successRate ?? 0, 4.0 / 5.0, accuracy: 0.0001)
        XCTAssertEqual(m.medianImplementDuration, 20 * 60)
        XCTAssertEqual(m.repairJobs, 2)
    }

    func testMissingDataIsNilNotZero() throws {
        let m = metrics(try snapshot([]))
        XCTAssertEqual(m.jobCount, 0)
        XCTAssertNil(m.todayDone); XCTAssertNil(m.todayFailed)
        XCTAssertNil(m.successRate); XCTAssertNil(m.medianImplementDuration); XCTAssertNil(m.repairJobs)
        // Only running jobs: no finished outcome, so no rate.
        XCTAssertNil(metrics(try snapshot([job("x", state: "leased", stage: "running")])).successRate)
        // Done jobs without timestamps give no median.
        XCTAssertNil(metrics(try snapshot([job("y")])).medianImplementDuration)
    }

    func testSevenDayBucketsOldestFirst() throws {
        let value = try snapshot([
            job("a"), job("b", state: "failed"),
            job("c", updated: "2026-11-02T08:00:00Z"),
            job("d", updated: "2026-10-28T08:00:00Z"), // eight days back, outside the window
        ])
        let m = metrics(value)
        XCTAssertEqual(m.days.count, 7)
        XCTAssertEqual(m.days.first?.start, calendar.date(from: DateComponents(year: 2026, month: 10, day: 29)))
        XCTAssertEqual(m.days.last.map { [$0.done, $0.failed] }, [1, 1])
        XCTAssertEqual(m.days[4].done, 1)
        XCTAssertEqual(m.days.map { $0.done + $0.failed }.reduce(0, +), 3)
        XCTAssertEqual(m.dayMax, 2)
        XCTAssertEqual(metrics(try snapshot([])).dayMax, 1)
    }

    func testMetricsAreCachedPerRepoFilter() throws {
        let value = try snapshot([job("a"), job("b", repo: "o/other", state: "failed")])
        XCTAssertEqual(value.metricsByRepo.keys.sorted(), ["", "o/other", "o/r"])
        XCTAssertEqual(value.metrics(repo: "").jobCount, 2)
        XCTAssertEqual(value.metrics(repo: "o/other").jobCount, 1)
        XCTAssertEqual(value.metrics(repo: "o/other").successRate, 0)
        XCTAssertEqual(value.metrics(repo: "o/missing").jobCount, 0)
        XCTAssertNil(value.metrics(repo: "o/missing").todayDone)
    }

    func testCapacityCountsSlotsAndHeldLeases() throws {
        let worker: (String, Int, [String]) -> [String: Any] = { name, slots, running in
            ["name": name, "tags": [], "roles": ["implement"], "live": true, "paused": false, "revoked": false,
             "max_parallel": slots, "running_tasks": running]
        }
        let value = try snapshot([job("lost", state: "leased", stage: "lease_expired")],
                                 workers: [worker("a", 2, ["x"]), worker("b", 2, [])])
        let c = value.capacity
        XCTAssertEqual(c.total, 4); XCTAssertEqual(c.running, 1); XCTAssertEqual(c.held, 1); XCTAssertEqual(c.live, 2)
        XCTAssertEqual(c.heldPosition ?? 0, 0.5, accuracy: 0.0001)
        XCTAssertNil(try snapshot([]).capacity.heldPosition)
    }
}
