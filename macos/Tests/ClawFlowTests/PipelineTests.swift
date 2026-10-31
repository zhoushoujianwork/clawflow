import XCTest
@testable import ClawFlow

final class PipelineTests: XCTestCase {
    private func job(_ id: String, issue: Int, pr: Int = 0, repo: String = "o/r", kind: String = "implement", state: String = "done", stage: String = "done") -> [String: Any] {
        ["id": id, "repo": repo, "issue": issue, "pr": pr, "kind": kind, "state": state, "stage": stage,
         "worker": "w", "attempt": 0, "updated_at": "2026-10-30T00:00:00Z", "tags": []]
    }
    private func snapshot(_ jobs: [[String: Any]], projects: [[String: Any]] = []) throws -> DashboardSnapshot {
        let fixture: [String: Any] = ["schema_version": 1, "version": "test", "controller": "test",
            "board": ["generated_at": "2026-10-30T00:00:00Z", "workers": [], "tasks": jobs], "projects": projects]
        return try DashboardSnapshot.decode(JSONSerialization.data(withJSONObject: fixture))
    }
    func testIssueIsOnePipelineAcrossJobsAndPRReview() throws {
        let rows = try snapshot([
            job("dev", issue: 7, pr: 9), job("repair", issue: 7, pr: 9, kind: "repair"),
            job("review", issue: 0, pr: 9, kind: "acceptance"), job("other", issue: 7, repo: "o/other")
        ]).issuePipelines
        XCTAssertEqual(rows.count, 2)
        let row = try XCTUnwrap(rows.first { $0.repo == "o/r" })
        XCTAssertEqual(row.issue, 7)
        XCTAssertEqual(Set(row.jobs.map(\.id)), ["dev", "repair", "review"])
        XCTAssertEqual(row.prs, [9])
        XCTAssertEqual(row.status, "Jobs finished") // execution success is not pipeline delivery success
    }
    func testAmbiguousPRReviewIsNotAttachedToAnArbitraryIssue() throws {
        let rows = try snapshot([job("one", issue: 1, pr: 9), job("two", issue: 2, pr: 9), job("review", issue: 0, pr: 9, kind: "acceptance")]).issuePipelines
        XCTAssertEqual(rows.count, 3)
        let unlinked = try XCTUnwrap(rows.first { $0.issue == 0 })
        XCTAssertEqual(unlinked.jobs.map(\.id), ["review"])
        XCTAssertTrue(unlinked.reference.contains("issue unavailable"))
    }
    func testRunningJobOutranksOldFailureWithinTheIssue() throws {
        let row = try XCTUnwrap(try snapshot([
            job("old", issue: 7, state: "failed", stage: "failed"),
            job("new", issue: 7, kind: "repair", state: "leased", stage: "running")
        ]).issuePipelines.first)
        XCTAssertEqual(row.group, .running)
        XCTAssertEqual(row.status, "Running")
    }
    func testQueuedIssueWithoutJobsAndSharedProjectMembershipAppearOnce() throws {
        let entry: [String: Any] = ["key": "o/r#42", "position": 1, "number": 42, "title": "Queued work", "tier": "P1", "state": "eligible:capacity"]
        let repo: [String: Any] = ["name": "o/r", "url": "https://github.com/o/r", "platform": "github", "enabled": true,
            "ci_required": true, "acceptance_required": true, "auto_merge": false, "worker_tags": [], "issue_queue": [entry]]
        let rows = try snapshot([], projects: [["name": "A", "repos": [repo]], ["name": "B", "repos": [repo]]]).issuePipelines
        XCTAssertEqual(rows.count, 1)
        XCTAssertEqual(rows[0].issue, 42)
        XCTAssertEqual(rows[0].title, "Queued work")
        XCTAssertTrue(rows[0].jobs.isEmpty)
        XCTAssertEqual(rows[0].group, .waiting)
    }
}
