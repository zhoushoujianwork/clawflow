import XCTest
@testable import ClawFlow

final class DashboardPresentationTests: XCTestCase {
    private func issue(_ state: String) throws -> IssueQueueRow {
        let json = #"{"key":"o/r#1","position":1,"number":1,"title":"t","tier":"P2","state":"\#(state)"}"#
        return try decoder().decode(IssueQueueRow.self, from: Data(json.utf8))
    }

    func testIssueDetailNeverShowsRawStateCodes() throws {
        for (state, expected) in [
            ("in_flight", nil),
            ("in_flight:no_verdict", "PR open, awaiting acceptance"),
            ("parked:acceptance_stale", "PR changed, acceptance out of date"),
            ("waiting:dependency", "Waiting for a dependency"),
            ("eligible:max_in_flight", "Repository slots full"),
            ("eligible:coordination checked", nil),
            ("waiting:coordination_waiting — Explicit dependency #669", "Explicit dependency #669"),
            ("blocked:some_new_reason", "Some new reason"),
        ] as [(String, String?)] {
            XCTAssertEqual(try issue(state).detail, expected, state)
        }
    }

    func testIssueToneFollowsStatePrefix() throws {
        XCTAssertEqual(try issue("in_flight:no_verdict").tone, .active)
        XCTAssertEqual(try issue("eligible").tone, .ready)
        XCTAssertEqual(try issue("blocked:x").tone, .blocked)
        XCTAssertEqual(try issue("parked:x").tone, .waiting)
        XCTAssertEqual(try issue("something_new").tone, .neutral)
    }

    private func snapshot(projects: String, tasks: String = "") throws -> DashboardSnapshot {
        let time = "2026-11-04T00:00:00Z"
        let json = #"{"schema_version":1,"version":"t","controller":"c","board":{"generated_at":"\#(time)","workers":[],"tasks":[\#(tasks)]},"projects":[\#(projects)]}"#
        return try DashboardSnapshot.decode(Data(json.utf8))
    }
    private func repo(_ name: String) -> String {
        #"{"name":"\#(name)","url":"https://example.test/\#(name)","platform":"github","enabled":true,"ci_required":true,"acceptance_required":false,"auto_merge":false,"worker_tags":[],"issue_queue":[{"key":"\#(name)#1","position":1,"number":1,"tier":"P2","state":"in_flight"}]}"#
    }

    func testRepositoryInSeveralProjectsAppearsOnce() throws {
        let value = try snapshot(projects: #"{"name":"a","repos":[\#(repo("o/x")),\#(repo("o/y"))]},{"name":"b","repos":[\#(repo("o/y"))]},{"name":"c","repos":[\#(repo("o/z"))]}"#)
        XCTAssertEqual(value.uniqueProjects.map(\.name), ["a", "c"])
        XCTAssertEqual(value.uniqueProjects.flatMap(\.repos).map(\.name), ["o/x", "o/y", "o/z"])
        XCTAssertEqual(value.openIssueCount, 3)
        XCTAssertEqual(Destination.issues.badge(in: value), "3")
        XCTAssertEqual(Destination.projects.badge(in: value), "3")
        XCTAssertNil(Destination.tasks.badge(in: value))
        XCTAssertNil(Destination.tasks.badge(in: nil))
    }

    func testTaskTitleDoesNotRepeatRepository() throws {
        let task = #"{"id":"t","repo":"o/widgets","issue":7,"pr":0,"kind":"implement","state":"pending","stage":"pending","worker":"","attempt":0,"updated_at":"2026-11-04T00:00:00Z","tags":[],"subject":"Fix login"}"#
        let row = try snapshot(projects: "", tasks: task).board.tasks[0]
        XCTAssertEqual(row.displayTitle, "Fix login")
        XCTAssertEqual(row.repoShortName, "widgets")
        XCTAssertEqual(row.reference, "#7")
    }

    func testRelativeAgeIsAlwaysEnglish() {
        let now = Date(timeIntervalSince1970: 1_000_000)
        XCTAssertEqual(relativeAge(now.addingTimeInterval(-30), now: now), "just now")
        XCTAssertEqual(relativeAge(now.addingTimeInterval(30), now: now), "just now")
        XCTAssertEqual(relativeAge(now.addingTimeInterval(-300), now: now), "5 min ago")
        XCTAssertEqual(relativeAge(now.addingTimeInterval(-6600), now: now), "1 h 50 min ago")
        XCTAssertEqual(relativeAge(now.addingTimeInterval(-3 * 86400), now: now), "3 days ago")
    }
}
