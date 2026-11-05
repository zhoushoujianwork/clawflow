import Combine
import XCTest
@testable import ClawFlow

@MainActor final class DashboardRefreshTests: XCTestCase {
    func testClockTicksDoNotInvalidateListsButStalenessStillDoes() throws {
        let date = Date(timeIntervalSince1970: 1_793_600_000)
        let data = try JSONSerialization.data(withJSONObject: ["schema_version": 1, "version": "test", "controller": "test",
            "board": ["generated_at": ISO8601DateFormatter().string(from: date), "workers": [], "tasks": []], "projects": []])
        let model = DashboardModel()
        model.acceptSnapshot(try DashboardSnapshot.decode(data), at: date)
        XCTAssertTrue(model.summary.connected)
        var updates = 0
        let observation = model.objectWillChange.sink { updates += 1 }
        for second in 1..<20 { model.advanceClock(to: date.addingTimeInterval(Double(second))) }
        XCTAssertEqual(updates, 0, "Elapsed ticks must not invalidate the dashboard table")
        model.advanceClock(to: date.addingTimeInterval(20))
        XCTAssertEqual(updates, 1)
        XCTAssertFalse(model.summary.connected, "Stale snapshots must stop live status")
        model.advanceClock(to: date.addingTimeInterval(21))
        XCTAssertEqual(updates, 1)
        withExtendedLifetime(observation) {}
    }

    func testBackgroundDecodeKeepsDatePrecisionAndProjection() async throws {
        let json = #"{"schema_version":1,"version":"test","controller":"test","board":{"generated_at":"2026-11-02T14:00:00.123456+08:00","workers":[],"tasks":[]},"projects":[]}"#
        let value = try await DashboardSnapshot.decodeInBackground(Data(json.utf8))
        let whole = try XCTUnwrap(ISO8601DateFormatter().date(from: "2026-11-02T06:00:00Z"))
        XCTAssertEqual(value.board.generatedAt.timeIntervalSince(whole), 0.123456, accuracy: 0.00001)
        XCTAssertTrue(value.issuePipelines.isEmpty)
    }
}
