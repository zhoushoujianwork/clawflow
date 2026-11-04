import XCTest
@testable import ClawFlow

final class PipelineStageTests: XCTestCase {
    private func job(_ id: String, kind: String = "implement", state: String = "done", stage: String = "done", updated: String = "2026-10-30T00:00:00Z") -> [String: Any] {
        ["id": id, "repo": "o/r", "issue": 7, "pr": 0, "kind": kind, "state": state, "stage": stage,
         "worker": "w", "attempt": 0, "updated_at": updated, "tags": []]
    }
    private func pipeline(_ jobs: [[String: Any]], queue: String? = nil, acceptance: Bool = false,
                          findings: [[String: Any]]? = nil, throughput: [String: Any]? = nil) throws -> (DashboardSnapshot, IssuePipeline) {
        var repo: [String: Any] = ["name": "o/r", "url": "https://github.com/o/r", "platform": "github", "enabled": true,
            "ci_required": true, "acceptance_required": acceptance, "auto_merge": false, "worker_tags": []]
        if let queue { repo["issue_queue"] = [["key": "o/r#7", "position": 1, "number": 7, "title": "Work", "tier": "P1", "state": queue]] }
        var board: [String: Any] = ["generated_at": "2026-10-30T00:00:00Z", "workers": [], "tasks": jobs]
        if let findings { board["findings"] = findings }
        if let throughput { board["throughput"] = throughput }
        let fixture: [String: Any] = ["schema_version": 1, "version": "t", "controller": "c", "board": board, "projects": [["name": "P", "repos": [repo]]]]
        let snapshot = try DashboardSnapshot.decode(JSONSerialization.data(withJSONObject: fixture))
        return (snapshot, try XCTUnwrap(snapshot.issuePipelines.first))
    }
    private func stage(_ row: IssuePipeline, _ id: String) -> PipelineStage? { row.stages.first { $0.id == id } }

    func testCoordinationAndConfirmationWaitAtAdmit() throws {
        let row = try pipeline([], queue: "waiting:coordination_waiting — Explicit dependency #669").1
        XCTAssertEqual(stage(row, "admit")?.state, .waiting)
        XCTAssertEqual(stage(row, "admit")?.detail, "Explicit dependency #669")
        XCTAssertEqual(try pipeline([], queue: "waiting:needs-confirmation").1.stages[0].state, .waiting)
    }
    func testDependencyWaitsWithReadableDetail() throws {
        let admit = try XCTUnwrap(stage(try pipeline([], queue: "waiting:dependency").1, "admit"))
        XCTAssertEqual(admit.state, .waiting)
        XCTAssertEqual(admit.detail, "Waiting for a dependency")
    }
    func testBlockedIsAttentionAndParkedIsWaiting() throws {
        let blocked = try pipeline([], queue: "blocked:some_new_reason").1
        XCTAssertEqual(stage(blocked, "admit")?.state, .attention)
        XCTAssertEqual(blocked.group, .attention)
        XCTAssertEqual(blocked.reason, "Some new reason")
        let parked = try pipeline([], queue: "parked:acceptance_stale").1
        XCTAssertEqual(parked.group, .waiting)
    }
    func testCoordinationCheckedFinishesAdmitAndEligibleWaitsForSlot() throws {
        XCTAssertEqual(stage(try pipeline([], queue: "eligible:coordination checked").1, "admit")?.state, .finished)
        let eligible = try XCTUnwrap(stage(try pipeline([], queue: "eligible").1, "admit"))
        XCTAssertEqual(eligible.state, .waiting); XCTAssertEqual(eligible.detail, "Waiting for a slot")
        XCTAssertEqual(stage(try pipeline([], queue: "eligible:max_in_flight").1, "admit")?.detail, "Repository slots full")
    }
    func testInFlightWithoutJobFinishesAdmitAndImplementIsPending() throws {
        let row = try pipeline([], queue: "in_flight").1
        XCTAssertEqual(stage(row, "admit")?.state, .finished)
        XCTAssertEqual(stage(row, "implement")?.state, .pending)
        XCTAssertTrue(stage(row, "implement")?.jobs.isEmpty == true)
    }
    func testImplementJobProvesAdmitWithoutQueue() throws {
        XCTAssertEqual(stage(try pipeline([job("a")]).1, "admit")?.state, .finished)
    }
    func testNoQueueAndNoJobIsNotReported() {
        XCTAssertEqual(IssuePipeline.stages(jobs: [], queue: nil, acceptanceRequired: false).first?.state, .notReported)
    }
    func testNoVerdictAndStaleAcceptanceWaitAtReview() throws {
        for (state, detail) in [("in_flight:no_verdict", "PR open, awaiting acceptance"), ("parked:acceptance_stale", "PR changed, acceptance out of date")] {
            let review = try XCTUnwrap(stage(try pipeline([job("a")], queue: state).1, "review"), state)
            XCTAssertEqual(review.state, .waiting, state)
            XCTAssertEqual(review.detail, detail, state)
        }
    }
    func testReviewOmittedWhenNotRequiredAndNoJob() throws {
        XCTAssertNil(stage(try pipeline([job("a")], acceptance: false).1, "review"))
        XCTAssertEqual(stage(try pipeline([job("a")], acceptance: true).1, "review")?.state, .pending)
        XCTAssertEqual(stage(try pipeline([job("a"), job("r", kind: "acceptance")], acceptance: false).1, "review")?.state, .finished)
    }
    func testRepairOnlyWithJobsAndCountsThem() throws {
        XCTAssertNil(stage(try pipeline([job("a")]).1, "repair"))
        let repair = try XCTUnwrap(stage(try pipeline([job("a"), job("r1", kind: "repair"), job("r2", kind: "repair")]).1, "repair"))
        XCTAssertEqual(repair.title, "Repair ×2")
        XCTAssertEqual(repair.jobs.count, 2)
    }
    func testDoneJobIsFinishedNeverPassed() throws {
        let implement = try XCTUnwrap(stage(try pipeline([job("a")]).1, "implement"))
        XCTAssertEqual(implement.state, .finished)
        XCTAssertNotEqual(implement.state.tone.color, .green)
        XCTAssertFalse(implement.accessibilityLabel.localizedCaseInsensitiveContains("passed"))
        XCTAssertEqual(implement.accessibilityLabel, "Implement: finished. Job status, not a delivery verdict.")
        XCTAssertNil(try pipeline([job("a")]).1.stages.first { ["ci", "merge"].contains($0.id) })
    }
    func testJobStateMap() throws {
        XCTAssertEqual(stage(try pipeline([job("a", state: "failed", stage: "failed")]).1, "implement")?.state, .failed)
        XCTAssertEqual(stage(try pipeline([job("a", state: "leased", stage: "running")]).1, "implement")?.state, .running)
        XCTAssertEqual(stage(try pipeline([job("a", state: "pending", stage: "pending")]).1, "implement")?.state, .waiting)
        XCTAssertEqual(stage(try pipeline([job("a", state: "leased", stage: "lease_expired")]).1, "implement")?.state, .attention)
        let cancelled = try XCTUnwrap(stage(try pipeline([job("a", state: "cancelled", stage: "cancelled")]).1, "implement"))
        XCTAssertEqual(cancelled.state, .pending); XCTAssertEqual(cancelled.detail, "Cancelled")
        let newest = try pipeline([job("old", state: "failed", stage: "failed"), job("new", updated: "2026-10-30T01:00:00Z")]).1
        XCTAssertEqual(stage(newest, "implement")?.state, .finished)
        XCTAssertEqual(stage(newest, "implement")?.jobs.map(\.id), ["new", "old"])
    }
    func testFindingMakesAttentionAndSuppliesReason() throws {
        let finding: [String: Any] = ["task_id": "a", "repo": "o/r", "issue": 7, "pr": 0, "rule": "no_progress",
            "since": "2026-10-29T23:00:00Z", "age_sec": 3600, "reason": "No progress for 60 minutes"]
        let row = try pipeline([job("a", state: "leased", stage: "running")], findings: [finding]).1
        XCTAssertEqual(row.group, .attention)
        XCTAssertEqual(row.reason, "No progress for 60 minutes")
        XCTAssertEqual(row.findings.count, 1)
        let other = try pipeline([job("b", state: "leased", stage: "running")], findings: [finding]).1
        XCTAssertEqual(other.group, .running)
    }
    func testOldFailedHistoryIsNotAttention() throws {
        let row = try pipeline([job("a", state: "failed", stage: "failed")]).1
        XCTAssertEqual(row.group, .history)
        XCTAssertEqual(row.status, "Latest job failed")
    }
    func testSnapshotDecodesWithAndWithoutFindingsAndThroughput() throws {
        let (old, _) = try pipeline([job("a")])
        XCTAssertNil(old.board.findings); XCTAssertNil(old.board.throughput)
        XCTAssertNil(old.board.tasks[0].lastEventAt)
        let (new, _) = try pipeline([job("a")], findings: [], throughput: ["since": "2026-10-29T23:00:00Z", "done": 4, "failed": 1])
        XCTAssertEqual(new.board.throughput?.done, 4)
        XCTAssertEqual(new.board.findings?.count, 0)
    }
    func testRawStateCodesNeverReachReason() throws {
        for state in ["waiting:dependency", "eligible:max_in_flight", "blocked:area_busy", "in_flight:no_verdict", "parked:acceptance_stale", "eligible", "waiting:coordination_waiting — Explicit dependency #1"] {
            let row = try pipeline([], queue: state).1
            XCTAssertFalse(row.reason.contains(":") || row.reason.contains("_"), "\(state) -> \(row.reason)")
            for stage in row.stages { XCTAssertFalse((stage.detail ?? "").contains("_"), "\(state) -> \(stage.detail ?? "")") }
        }
    }

    func testLaterWorkNeverReadsAsNotStarted() throws {
        let row = try pipeline([job("r", kind: "repair")]).1
        XCTAssertEqual(stage(row, "implement")?.state, .notReported)
        XCTAssertEqual(stage(row, "admit")?.state, .finished)
        XCTAssertEqual(stage(try pipeline([], queue: "eligible").1, "implement")?.state, .pending)
    }
    func testOutstandingVerdictOutranksOlderFinishedReview() throws {
        let row = try pipeline([job("a", kind: "acceptance")], queue: "parked:acceptance_stale").1
        XCTAssertEqual(stage(row, "review")?.state, .waiting)
        XCTAssertEqual(stage(row, "review")?.detail, "PR changed, acceptance out of date")
        XCTAssertEqual(stage(row, "review")?.jobs.count, 1)
        XCTAssertEqual(row.rowNote, "PR changed, acceptance out of date")
        XCTAssertEqual(stage(try pipeline([job("a", kind: "acceptance")]).1, "review")?.state, .finished)
    }
    func testFailedHistoryStatusAndNotes() throws {
        let row = try pipeline([job("i", state: "failed", stage: "failed")]).1
        XCTAssertEqual(row.status, "Latest job failed")
        XCTAssertNil(row.rowNote)
        XCTAssertEqual(try pipeline([], queue: "waiting:dependency").1.rowNote, "Waiting for a dependency")
    }
}
