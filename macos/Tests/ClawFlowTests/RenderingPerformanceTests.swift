import XCTest
@testable import ClawFlow

final class RenderingPerformanceTests: XCTestCase {
    func testSnapshotProjectionReadCost() throws {
        let tasks: [[String: Any]] = (0..<1000).map { i in
            ["id": "job-\(i)", "repo": "o/r", "issue": i / 4 + 1, "pr": 0,
             "kind": "implement", "state": "done", "stage": "done", "worker": "w",
             "attempt": 0, "tags": [], "updated_at": "2026-11-02T06:00:00.123456+00:00"]
        }
        let data = try JSONSerialization.data(withJSONObject: ["schema_version": 1, "version": "test", "controller": "fixture",
            "board": ["generated_at": "2026-11-02T06:00:00Z", "workers": [], "tasks": tasks], "projects": []])
        let options = XCTMeasureOptions(); options.iterationCount = 3
        measure(options: options) {
            do {
                let snapshot = try DashboardSnapshot.decode(data)
                XCTAssertEqual(snapshot.board.tasks.count, 1000)
                for _ in 0..<100 { XCTAssertEqual(snapshot.issuePipelines.count, 250) }
            } catch { XCTFail("Snapshot failed: \(error)") }
        }
    }
}
