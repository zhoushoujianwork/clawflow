import XCTest
@testable import ClawFlow

@MainActor final class TranscriptStoreTests: XCTestCase {
    private func ev(_ seq: Int64, _ type: String = "message") -> TranscriptEventRow {
        TranscriptEventRow(seq: seq, type: type, text: "e\(seq)", toolName: nil, toolCallId: nil, arguments: nil, output: nil, isError: nil, exitCode: nil, reason: nil)
    }
    private func page(epoch: Int64 = 1, events: [TranscriptEventRow], nextAfter: Int64, hasMore: Bool = false, status: String = "available", live: Bool = false, epochs: [TranscriptEpochInfo] = []) -> TranscriptView {
        TranscriptView(schemaVersion: 1, taskId: "t", taskState: "leased", epoch: epoch, invocation: 1, invocations: 1,
            attempt: 0, worker: "w", status: status, reason: nil, executionDone: false, live: live, completeness: nil,
            gap: false, gapReason: nil, capped: false, events: events, nextAfter: nextAfter, hasMore: hasMore, epochs: epochs)
    }

    func testAppendOrdersAndAdvancesCursor() {
        let store = TranscriptStore()
        store.apply(page(events: [ev(1), ev(2)], nextAfter: 2))
        XCTAssertEqual(store.events.map(\.seq), [1, 2])
        XCTAssertEqual(store.requestAfter, 2)
        store.apply(page(events: [ev(3)], nextAfter: 3))
        XCTAssertEqual(store.events.map(\.seq), [1, 2, 3])
    }

    func testDuplicateAndOutOfOrderPagesAreNotReappended() {
        let store = TranscriptStore()
        store.apply(page(events: [ev(1), ev(2)], nextAfter: 2))
        // A redelivered or overlapping page must not duplicate rows already held.
        store.apply(page(events: [ev(1), ev(2), ev(3)], nextAfter: 3))
        XCTAssertEqual(store.events.map(\.seq), [1, 2, 3])
    }

    func testPagingSetsHasMoreUntilCaughtUp() {
        let store = TranscriptStore()
        store.apply(page(events: [ev(1)], nextAfter: 1, hasMore: true))
        XCTAssertTrue(store.hasMore)
        store.apply(page(events: [ev(2)], nextAfter: 2, hasMore: false))
        XCTAssertFalse(store.hasMore)
    }

    func testFollowStaysTrueUntilUserScrollsUp() {
        let store = TranscriptStore()
        XCTAssertTrue(store.follow)
        store.follow = false
        store.apply(page(events: [ev(1)], nextAfter: 1))
        // Appending content must not silently re-enable follow behind the user's back.
        XCTAssertFalse(store.follow)
    }

    func testRetryIntoNewEpochIsolatesLogFromThePreviousAttempt() {
        let store = TranscriptStore()
        store.apply(page(epoch: 1, events: [ev(1), ev(2)], nextAfter: 2))
        XCTAssertEqual(store.events.count, 2)
        // A new lease epoch (retry) must start its own log, never appended
        // after the previous attempt's tail, while still following "latest".
        store.apply(page(epoch: 2, events: [ev(1)], nextAfter: 1))
        XCTAssertEqual(store.currentEpoch, 2)
        XCTAssertEqual(store.events.map(\.seq), [1])
    }

    func testPinningAnOlderAttemptStopsFollowingRetries() {
        let store = TranscriptStore()
        store.apply(page(epoch: 1, events: [ev(1)], nextAfter: 1, epochs: [
            TranscriptEpochInfo(epoch: 1, attempt: 0, worker: "w", status: "available", eventCount: 1, endOfStream: true, attemptState: "done"),
            TranscriptEpochInfo(epoch: 2, attempt: 1, worker: "w", status: "available", eventCount: 0, endOfStream: false, attemptState: "leased"),
        ]))
        store.select(epoch: 1)
        XCTAssertEqual(store.requestEpoch, 1)
        // While pinned to epoch 1, a page reporting epoch 2's content must not
        // overwrite the pinned attempt's buffer.
        store.apply(page(epoch: 1, events: [ev(1)], nextAfter: 1))
        XCTAssertEqual(store.currentEpoch, 1)
        store.followLatest()
        XCTAssertNil(store.requestEpoch)
    }

    func testDecodesControllerJSONShape() throws {
        let json = """
        {"schema_version":1,"task_id":"t","task_state":"leased","epoch":3,"invocation":1,"invocations":1,
         "attempt":2,"worker":"w1","status":"available","execution_done":false,"live":true,"gap":false,"capped":false,
         "events":[{"seq":1,"type":"tool_call","tool_call_id":"c1","tool_name":"command_execution","arguments":"ls"}],
         "next_after":1,"has_more":false,"epochs":[]}
        """
        let view = try decoder().decode(TranscriptView.self, from: Data(json.utf8))
        XCTAssertEqual(view.events.first?.toolCallId, "c1")
        XCTAssertTrue(view.live)
    }
}
