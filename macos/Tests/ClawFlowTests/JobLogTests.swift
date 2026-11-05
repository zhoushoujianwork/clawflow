import Combine
import XCTest
@testable import ClawFlow

private func page(task: String = "task-1", epoch: Int = 3, active: Bool = true, availability: String = "available",
                  streamState: String = "live", streamAvailability: String = "available", dropped: Int = 0,
                  entries: [(Int, String)] = [], more: Bool = false, epochs: [Int]? = nil, streams: Bool = true) -> JobLogResponse {
    let epochList = (epochs ?? [epoch]).enumerated().map { ["epoch": $0.element, "attempt": $0.offset + 1, "worker": "w", "status": "running"] as [String: Any] }
    let streamList: [[String: Any]] = streams ? [["invocation": "inv", "availability": streamAvailability, "state": streamState, "event_count": entries.count, "dropped_events": dropped]] : []
    let rows: [[String: Any]] = entries.map { id, kind in
        var event: [String: Any] = ["seq": id, "type": kind]
        switch kind {
        case "message": event["role"] = "assistant"; event["text"] = "message \(id)"
        case "tool_call": event["tool_call_id"] = "call-\(id)"; event["tool_name"] = "Bash"; event["arguments"] = "ls"
        case "tool_result": event["tool_call_id"] = "call-\(id - 1)"; event["output"] = "file.go"; event["exit_code"] = 0
        case "gap": event["reason"] = "event_too_large"; event["bytes"] = 4096
        default: break
        }
        return ["id": id, "invocation": "inv", "event": event]
    }
    let body: [String: Any] = ["task_id": task, "epoch": epoch, "epochs": epochList, "active": active, "availability": availability,
        "streams": streamList, "entries": rows, "next_after": entries.last?.0 ?? 0, "has_more": more]
    let data = try! JSONSerialization.data(withJSONObject: body)
    return try! decoder().decode(JobLogResponse.self, from: data)
}

final class JobLogBufferTests: XCTestCase {
    func testReplayThenAppendKeepsOrderAndExistingIdentities() {
        var buffer = JobLogBuffer()
        XCTAssertEqual(buffer.apply(page(entries: [(1, "message"), (2, "tool_call"), (3, "tool_result")]), requestedEpoch: nil), 3)
        let before = buffer.lines.map(\.id)
        XCTAssertEqual(buffer.apply(page(entries: [(4, "message")]), requestedEpoch: nil), 1)
        XCTAssertEqual(buffer.lines.map(\.id), before + [4], "appending must not re-identify existing rows")
        XCTAssertEqual(buffer.after, 4)
        XCTAssertTrue(buffer.isLive)
    }

    func testDuplicateAndStalePagesAreIgnored() {
        var buffer = JobLogBuffer()
        buffer.apply(page(entries: [(1, "message"), (2, "message"), (3, "message")]), requestedEpoch: nil)
        XCTAssertEqual(buffer.apply(page(entries: [(1, "message"), (2, "message"), (3, "message")]), requestedEpoch: nil), 0)
        XCTAssertEqual(buffer.apply(page(entries: [(2, "message"), (3, "message"), (4, "message")]), requestedEpoch: nil), 1)
        XCTAssertEqual(buffer.lines.map(\.id), [1, 2, 3, 4])
    }

    func testPagingContinuesFromLastEntryAndReportsMore() {
        var buffer = JobLogBuffer()
        buffer.apply(page(entries: [(1, "message"), (2, "message")], more: true), requestedEpoch: nil)
        XCTAssertTrue(buffer.hasMore)
        XCTAssertEqual(buffer.after, 2)
        buffer.apply(page(entries: [(3, "message")], more: false), requestedEpoch: nil)
        XCTAssertFalse(buffer.hasMore)
        XCTAssertEqual(buffer.lines.count, 3)
    }

    func testRetryEpochIsIsolatedFromPreviousAttempt() {
        var buffer = JobLogBuffer()
        buffer.apply(page(epoch: 3, entries: [(1, "message"), (2, "message")], epochs: [3]), requestedEpoch: nil)
        // Following the newest attempt after a retry starts a clean view.
        buffer.apply(page(epoch: 4, entries: [(1, "message")], epochs: [3, 4]), requestedEpoch: nil)
        XCTAssertEqual(buffer.epoch, 4)
        XCTAssertEqual(buffer.lines.map(\.id), [1])
        XCTAssertEqual(buffer.epochs.count, 2)
        // A late page for the old epoch, when a specific epoch is pinned, is dropped.
        XCTAssertEqual(buffer.apply(page(epoch: 3, entries: [(5, "message")]), requestedEpoch: 4), 0)
        XCTAssertEqual(buffer.lines.count, 1)
    }

    func testPageForAnotherTaskIsNeverMerged() {
        var buffer = JobLogBuffer()
        buffer.apply(page(task: "task-1", entries: [(1, "message")]), requestedEpoch: nil)
        XCTAssertEqual(buffer.apply(page(task: "task-2", entries: [(2, "message")]), requestedEpoch: nil), 0)
        XCTAssertEqual(buffer.lines.count, 1)
    }

    func testToolResultsKeepTheirCallAndErrorsAreRecognisable() throws {
        var buffer = JobLogBuffer()
        buffer.apply(page(entries: [(1, "tool_call"), (2, "tool_result")]), requestedEpoch: nil)
        XCTAssertEqual(buffer.lines[0].kind, .toolCall)
        XCTAssertEqual(buffer.lines[1].kind, .toolResult)
        XCTAssertEqual(buffer.callTitles[try XCTUnwrap(buffer.lines[1].toolCallId)], "Tool · Bash")
        let failure = #"{"task_id":"task-1","epoch":3,"epochs":[],"active":false,"availability":"available","streams":[],"next_after":9,"has_more":false,"entries":[{"id":9,"invocation":"inv","event":{"seq":9,"type":"tool_result","output":"boom","exit_code":2}}]}"#
        buffer.apply(try decoder().decode(JobLogResponse.self, from: Data(failure.utf8)), requestedEpoch: nil)
        XCTAssertEqual(buffer.lines.last?.kind, .error)
        XCTAssertEqual(buffer.lines.last?.isError, true)
    }

    func testGapsAndLimitsAreSurfaced() {
        var buffer = JobLogBuffer()
        buffer.apply(page(entries: [(1, "message"), (2, "gap")]), requestedEpoch: nil)
        XCTAssertTrue(buffer.hasGap)
        XCTAssertEqual(buffer.lines[1].kind, .gap)
        var dropped = JobLogBuffer()
        dropped.apply(page(dropped: 3, entries: [(1, "message")]), requestedEpoch: nil)
        XCTAssertTrue(dropped.hasGap)
    }

    func testBufferIsBoundedAndSaysSo() {
        var buffer = JobLogBuffer()
        let total = JobLogBuffer.maxLines + 50
        var next = 1
        while next <= total {
            let upper = min(next + 199, total)
            buffer.apply(page(entries: (next...upper).map { ($0, "message") }), requestedEpoch: nil)
            next = upper + 1
        }
        XCTAssertEqual(buffer.lines.count, JobLogBuffer.maxLines)
        XCTAssertEqual(buffer.evicted, 50)
        XCTAssertTrue(buffer.hasGap)
        XCTAssertEqual(buffer.lines.last?.id, total)
    }

    func testEveryNoContentStateIsDistinct() {
        let states: [(String, JobLogNotice)] = [
            ("not_captured", .notCaptured), ("capture_disabled", .captureDisabled), ("unsupported_offload", .unsupportedOffload),
            ("unsupported_backend", .unsupportedBackend), ("not_yet_available", .waiting), ("expired", .expired), ("unreadable", .unreadable)]
        var titles = Set<String>()
        for (availability, expected) in states {
            var buffer = JobLogBuffer()
            let streams = availability != "not_captured"
            buffer.apply(page(availability: streams ? "available" : availability, streamState: "ended", streamAvailability: availability, streams: streams), requestedEpoch: nil)
            XCTAssertEqual(buffer.notice, expected, availability)
            titles.insert(buffer.notice!.title)
        }
        XCTAssertEqual(titles.count, states.count, "each no-content situation must read differently")
        var gap = JobLogBuffer()
        gap.apply(page(entries: [(1, "message")]), requestedEpoch: nil)
        XCTAssertNil(gap.notice, "a notice must never hide recorded output")
    }

    func testFinishedAndInterruptedStatesAreNotLive() {
        var done = JobLogBuffer()
        done.apply(page(active: false, streamState: "ended", entries: [(1, "message")]), requestedEpoch: nil)
        XCTAssertFalse(done.isLive)
        XCTAssertEqual(done.lines.count, 1, "output stays viewable after the job ends")
        var cut = JobLogBuffer()
        cut.apply(page(active: false, streamState: "interrupted", entries: [(1, "message")]), requestedEpoch: nil)
        XCTAssertTrue(cut.interrupted)
    }

    func testSchedulingActionsCannotBecomeOutput() {
        // The controller's scheduling event shape has no place in the log model.
        let scheduling = #"{"task_id":"task-1","epoch":1,"epochs":[],"active":true,"availability":"not_captured","streams":[],"entries":[],"next_after":0,"has_more":false}"#
        var buffer = JobLogBuffer()
        buffer.apply(try! decoder().decode(JobLogResponse.self, from: Data(scheduling.utf8)), requestedEpoch: nil)
        XCTAssertTrue(buffer.lines.isEmpty)
        XCTAssertEqual(buffer.notice, .notCaptured)
    }
}

final class JobLogFollowTests: XCTestCase {
    private let t0 = Date(timeIntervalSince1970: 1_793_600_000)

    func testFollowsByDefaultAndScrollsOnAppend() {
        var follow = JobLogFollowState()
        XCTAssertTrue(follow.isFollowing)
        XCTAssertTrue(follow.linesAppended(at: t0))
    }

    func testScrollingUpStopsFollowingAndNewOutputDoesNotMoveTheView() {
        var follow = JobLogFollowState()
        XCTAssertTrue(follow.linesAppended(at: t0))
        // The marker briefly leaves the viewport while the new row lays out: still following.
        follow.bottomMarkerDisappeared(at: t0.addingTimeInterval(0.1))
        XCTAssertTrue(follow.isFollowing)
        // The user scrolls up well after the append.
        follow.bottomMarkerDisappeared(at: t0.addingTimeInterval(5))
        XCTAssertFalse(follow.isFollowing)
        XCTAssertFalse(follow.linesAppended(at: t0.addingTimeInterval(6)), "must not pull the user back down")
        XCTAssertFalse(follow.linesAppended(at: t0.addingTimeInterval(7)))
    }

    func testReturningToBottomResumesFollowing() {
        var follow = JobLogFollowState()
        follow.bottomMarkerDisappeared(at: t0)
        XCTAssertFalse(follow.isFollowing)
        follow.bottomMarkerAppeared()
        XCTAssertTrue(follow.isFollowing)
    }

    func testExplicitPauseSurvivesBottomVisibilityUntilResumed() {
        var follow = JobLogFollowState()
        follow.pause()
        follow.bottomMarkerAppeared()
        XCTAssertFalse(follow.isFollowing)
        XCTAssertFalse(follow.linesAppended(at: t0))
        follow.resume(at: t0)
        XCTAssertTrue(follow.isFollowing)
        XCTAssertTrue(follow.linesAppended(at: t0.addingTimeInterval(1)))
    }
}

@MainActor final class JobLogStoreTests: XCTestCase {
    private func response(task: String = "task-1", epoch: Int = 3, active: Bool, entries: [(Int, String)], more: Bool = false, epochs: [Int]? = nil) -> Data {
        let rows = entries.map { id, kind in ["id": id, "invocation": "inv", "event": ["seq": id, "type": kind, "role": "assistant", "text": "m\(id)"] as [String: Any]] as [String: Any] }
        let epochList = (epochs ?? [epoch]).enumerated().map { ["epoch": $0.element, "attempt": $0.offset + 1, "worker": "w", "status": "running"] as [String: Any] }
        let body: [String: Any] = ["task_id": task, "epoch": epoch, "epochs": epochList, "active": active, "availability": "available",
            "streams": [["invocation": "inv", "availability": "available", "state": active ? "live" : "ended", "event_count": entries.count]],
            "entries": rows, "next_after": entries.last?.0 ?? 0, "has_more": more]
        return try! JSONSerialization.data(withJSONObject: body)
    }

    func testResumesFromCursorAfterAFailedPollWithoutDuplicates() async {
        var calls: [(Int, Int)] = []
        var fail = false
        let store = JobLogStore(taskId: "task-1") { _, epoch, after in
            calls.append((epoch, after))
            if fail { fail = false; throw DashboardError.message("connection lost") }
            return after == 0 ? self.response(active: true, entries: [(1, "message"), (2, "message")]) : self.response(active: true, entries: [(3, "message")])
        }
        await store.poll()
        XCTAssertEqual(store.buffer.lines.map(\.id), [1, 2])
        fail = true
        await store.poll()
        XCTAssertNotNil(store.error)
        XCTAssertEqual(store.buffer.lines.map(\.id), [1, 2], "a failed poll keeps what was received")
        await store.poll()
        XCTAssertNil(store.error)
        XCTAssertEqual(store.buffer.lines.map(\.id), [1, 2, 3])
        XCTAssertEqual(calls.map(\.1), [0, 2, 2], "reconnect continues from the stored cursor")
    }

    func testSelectingAnotherAttemptDropsTheCurrentViewAndReadsThatEpoch() async {
        var requested: [Int] = []
        let store = JobLogStore(taskId: "task-1") { _, epoch, _ in
            requested.append(epoch)
            return epoch == 2 ? self.response(epoch: 2, active: false, entries: [(1, "message")], epochs: [2, 3])
                              : self.response(epoch: 3, active: true, entries: [(1, "message"), (2, "message")], epochs: [2, 3])
        }
        await store.poll()
        XCTAssertEqual(store.buffer.epoch, 3)
        store.select(epoch: 2)
        XCTAssertTrue(store.buffer.lines.isEmpty, "the previous attempt's lines must not linger")
        await store.poll()
        XCTAssertEqual(store.buffer.epoch, 2)
        XCTAssertEqual(store.buffer.lines.count, 1)
        XCTAssertFalse(store.buffer.isLive)
    }
}

@MainActor final class JobLogDashboardCostTests: XCTestCase {
    func testAppendingLogLinesDoesNotInvalidateTheDashboardModel() async throws {
        let date = Date(timeIntervalSince1970: 1_793_600_000)
        let data = try JSONSerialization.data(withJSONObject: ["schema_version": 1, "version": "test", "controller": "test",
            "board": ["generated_at": ISO8601DateFormatter().string(from: date), "workers": [], "tasks": []], "projects": []])
        let model = DashboardModel()
        model.acceptSnapshot(try DashboardSnapshot.decode(data), at: date)
        var invalidations = 0
        let observation = model.objectWillChange.sink { invalidations += 1 }
        var n = 0
        let store = JobLogStore(taskId: "task-1") { _, _, after in
            n += 1
            let rows = (after + 1...after + 20).map { id in ["id": id, "invocation": "inv", "event": ["seq": id, "type": "message", "role": "assistant", "text": "m"] as [String: Any]] as [String: Any] }
            return try JSONSerialization.data(withJSONObject: ["task_id": "task-1", "epoch": 1, "epochs": [], "active": true, "availability": "available",
                "streams": [["invocation": "inv", "availability": "available", "state": "live", "event_count": 20]], "entries": rows, "next_after": after + 20, "has_more": false])
        }
        for _ in 0..<10 { await store.poll() }
        XCTAssertEqual(store.buffer.lines.count, 200)
        for second in 1..<10 { model.advanceClock(to: date.addingTimeInterval(Double(second))) }
        XCTAssertEqual(invalidations, 0, "log traffic and clock ticks must not recompute the dashboard")
        XCTAssertEqual(n, 10)
        withExtendedLifetime(observation) {}
    }

    func testClosingTheLogStopsPollingAndSendsNothingToTheExecution() async throws {
        var requests: [Int] = []
        let store = JobLogStore(taskId: "task-1") { _, _, after in
            requests.append(after)
            return try JSONSerialization.data(withJSONObject: ["task_id": "task-1", "epoch": 1, "epochs": [], "active": true, "availability": "not_captured",
                "streams": [], "entries": [], "next_after": 0, "has_more": false])
        }
        store.start { true }
        try await Task.sleep(for: .milliseconds(150))
        store.stop()
        let count = requests.count
        try await Task.sleep(for: .milliseconds(300))
        XCTAssertEqual(requests.count, count, "closing the log must stop all reads")
        XCTAssertGreaterThanOrEqual(count, 1)
    }
}

final class JobDetailWithoutSchedulingEventsTests: XCTestCase {
    private func source(_ name: String) throws -> String {
        let root = URL(fileURLWithPath: #filePath).deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent()
        return try String(contentsOf: root.appendingPathComponent("Sources/ClawFlow/\(name)"), encoding: .utf8)
    }

    func testJobDetailNeitherLoadsNorShowsSchedulingEvents() throws {
        for file in ["DashboardView.swift", "Models.swift"] {
            let text = try source(file)
            XCTAssertFalse(text.contains("TaskEvent"), file)
            XCTAssertFalse(text.contains("调度事件"), file)
            XCTAssertFalse(text.contains("刷新事件"), file)
            XCTAssertFalse(text.contains("[\"events\""), file)
        }
    }

    func testMissingTranscriptStatesTheReasonWithoutEvents() {
        var buffer = JobLogBuffer()
        buffer.apply(page(availability: "not_captured", entries: [], streams: false), requestedEpoch: nil)
        XCTAssertTrue(buffer.lines.isEmpty)
        XCTAssertEqual(buffer.notice, .notCaptured)
        XCTAssertFalse(JobLogNotice.notCaptured.title.isEmpty)
        XCTAssertFalse(JobLogNotice.notCaptured.detail.isEmpty)
    }
}
