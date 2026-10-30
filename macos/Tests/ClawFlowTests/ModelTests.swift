import XCTest
@testable import ClawFlow

final class ModelTests: XCTestCase {
    func testAttemptElapsedUsesStartAndStopsAtEnd() throws {
        let value = try snapshot()
        let original = value.board.tasks[0]
        XCTAssertEqual(original.elapsed(at: Date()), "Duration unavailable")
        let json = #"{"id":"t","repo":"o/r","issue":1,"pr":0,"kind":"implement","state":"leased","stage":"running","worker":"w","attempt":0,"updated_at":"2026-10-30T00:09:00Z","started_at":"2026-10-30T00:00:00Z","ended_at":"2026-10-30T00:01:05Z","tags":[]}"#
        let row = try decoder().decode(TaskRow.self, from: Data(json.utf8))
        XCTAssertEqual(row.elapsed(at: Date()), "00:01:05")
    }

    func testReleaseCheckKeepsDevChannelAndValidatesLink() throws {
        let data = Data(#"{"tag_name":"v1.2.3","html_url":"https://github.com/zhoushoujianwork/clawflow/releases/tag/v1.2.3","draft":false,"prerelease":false}"#.utf8)
        let release = try decoder().decode(AppRelease.self, from: data)
        XCTAssertNotNil(release.url)
        XCTAssertTrue(release.message(current: "1.2.4-dev.123", channel: "dev").contains("make dev"))
        XCTAssertTrue(release.message(current: "1.2.2", channel: "release").contains("available"))
        let invalid = AppRelease(tagName: "v9", htmlUrl: "https://example.com/release", draft: false, prerelease: false)
        XCTAssertNil(invalid.url)
    }

    func testIssueQueueKeepsSchedulingReason() throws {
        let data = Data(#"{"key":"o/r#7","position":1,"number":7,"title":"Feature","tier":"P1","state":"eligible:max_in_flight"}"#.utf8)
        let row = try decoder().decode(IssueQueueRow.self, from: data)
        XCTAssertEqual(row.id, "o/r#7")
        XCTAssertEqual(row.status, "Ready / waiting for capacity")
        XCTAssertEqual(row.state, "eligible:max_in_flight")
    }

    func testActivityLabelsDistinguishExecutionPurpose() {
        XCTAssertEqual(TaskActivity(kind: "implement").title, "Development")
        XCTAssertEqual(TaskActivity(kind: "acceptance").title, "PR Review")
        XCTAssertEqual(TaskActivity(kind: "repair").title, "PR Repair")
        XCTAssertEqual(TaskActivity(kind: "reply").title, "Issue Reply")
        XCTAssertEqual(TaskActivity(kind: "new_kind").title, "Unknown activity")
    }
    private func snapshot(stage: String = "running", state: String = "leased", at: Date = Date(), pr: Int = 0) throws -> DashboardSnapshot {
        let time = ISO8601DateFormatter().string(from: at)
        let json = """
        {"schema_version":1,"version":"test","controller":"mini","board":{"generated_at":"\(time)","workers":[],"tasks":[{"id":"t1","repo":"o/r","issue":42,"pr":\(pr),"kind":"implement","state":"\(state)","stage":"\(stage)","worker":"linux","attempt":0,"updated_at":"\(time)","tags":[]}]},"projects":[{"name":"App","repos":[{"name":"o/r","url":"https://github.com/o/r","platform":"github","enabled":true,"ci_required":true,"acceptance_required":true,"auto_merge":false,"worker_tags":[]}]}]}
        """
        return try DashboardSnapshot.decode(Data(json.utf8))
    }
    func testIssueAndPRLinksRemainDistinct() throws {
        let value = try snapshot(pr: 611)
        XCTAssertEqual(value.board.tasks[0].link(in: value)?.absoluteString, "https://github.com/o/r/issues/42")
        XCTAssertEqual(value.board.tasks[0].prLink(in: value)?.absoluteString, "https://github.com/o/r/pull/611")
    }
    func testUnknownNeverLooksIdle() {
        let state = StatusSummary.reduce(snapshot: nil, received: nil, error: nil)
        XCTAssertFalse(state.connected); XCTAssertEqual(state.count, "–")
    }
    func testFreshnessAndFailureKeepDataButDisableActions() throws {
        let now = Date(), value = try snapshot()
        XCTAssertTrue(StatusSummary.reduce(snapshot: value, received: now, error: nil, now: now).connected)
        XCTAssertFalse(StatusSummary.reduce(snapshot: value, received: now, error: "offline", now: now).connected)
        XCTAssertFalse(StatusSummary.reduce(snapshot: value, received: now, error: nil, now: now.addingTimeInterval(21)).connected)
        XCTAssertFalse(StatusSummary.reduce(snapshot: try snapshot(at: now.addingTimeInterval(-60)), received: now, error: nil, now: now).connected)
    }
    func testPastFailureIsHistoryNotCurrentAttention() throws {
        let value = try snapshot(stage: "failed", state: "failed")
        XCTAssertEqual(value.board.tasks[0].group, .history)
        XCTAssertEqual(StatusSummary.reduce(snapshot: value, received: Date(), error: nil).title, "Idle")
    }
    func testLeaseLossAndWaitingHaveDifferentMeaning() throws {
        XCTAssertEqual(try snapshot(stage: "lease_expired").board.tasks[0].group, .attention)
        let value = try snapshot(stage: "pending", state: "pending")
        XCTAssertEqual(value.board.tasks[0].group, .waiting)
        XCTAssertEqual(StatusSummary.reduce(snapshot: value, received: Date(), error: nil).count, "0")
    }
    func testDoneDoesNotMeanDeliveredAndLinksStayOnRepoHost() throws {
        let value = try snapshot(stage: "done", state: "done")
        XCTAssertEqual(value.board.tasks[0].status, "Attempt completed")
        XCTAssertEqual(value.board.tasks[0].link(in: value)?.absoluteString, "https://github.com/o/r/issues/42")
        XCTAssertNil(safeWebURL("file:///etc/passwd"))
        XCTAssertNil(safeWebURL("https://user:secret@example.com"))
    }
    func testBridgeUsesArgumentsAndQuotesJoinCommand() {
        var c = Connection(); c.url = "https://example.com"; c.tokenFile = "/tmp/a b"; c.fingerprint = "abc'xyz"
        XCTAssertTrue(c.arguments(["drain", "worker name"]).contains("worker name"))
        XCTAssertTrue(c.arguments(["snapshot"]).contains("/tmp/a b"))
        XCTAssertTrue(c.joinCommand().contains("'abc'\\''xyz'"))
        XCTAssertFalse(c.joinCommand().contains("secret"))
    }
    func testUnsupportedSchemaFails() {
        XCTAssertThrowsError(try DashboardSnapshot.decode(Data("{}".utf8)))
    }
}

final class BridgeTests: XCTestCase {
    func testDrainsBothStreamsAndReturnsJSONOnly() async throws {
        let data = try await BridgeProcess().run(executable: "/bin/sh", arguments: ["-c", "printf diagnostic >&2; printf '{\"ok\":true}'"])
        XCTAssertEqual(String(decoding: data, as: UTF8.self), "{\"ok\":true}")
    }
    func testTimeoutStopsOnlyHelper() async {
        do {
            _ = try await BridgeProcess().run(executable: "/bin/sleep", arguments: ["30"], timeout: 0.1)
            XCTFail("expected timeout")
        } catch { XCTAssertTrue(error.localizedDescription.contains("timed out")) }
    }
    func testNonzeroStatusSurfacesError() async {
        do {
            _ = try await BridgeProcess().run(executable: "/bin/sh", arguments: ["-c", "printf 'denied' >&2; exit 2"])
            XCTFail("expected failure")
        } catch { XCTAssertEqual(error.localizedDescription, "denied") }
    }
}
