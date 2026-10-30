import XCTest
@testable import ClawFlow

@MainActor final class ExecutionStatusTests: XCTestCase {
    static let blocked = """
    {"schema_version":1,"generated_at":"2026-10-30T12:00:00Z","state":"blocked","summary":"Offload is unavailable for: creds-laptop.",
     "workers":[{"schema_version":1,"state":"blocked","reason":"missing_executable","summary":"Offload executable not found (configured command: offload).",
       "worker":"creds-laptop","environment":"service","configured_command":"offload","profiles":["base"],
       "guidance":{"steps":["Install Offload explicitly (requires Go 1.24+): go install github.com/zhoushoujianwork/offload@latest"],"link":"https://github.com/zhoushoujianwork/offload#quick-start","recheck":"clawflow execution status --recheck"},
       "checked_at":"2026-10-30T12:00:00Z"},
      {"schema_version":1,"state":"ready","summary":"Offload is installed and compatible; live authentication is not verified.","worker":"other","unverified":["live_authentication"],"checked_at":"2026-10-30T12:00:00Z"}]}
    """
    static let ready = """
    {"schema_version":1,"generated_at":"2026-10-30T12:01:00Z","state":"ready","summary":"Offload is ready; live authentication is not verified.","workers":[]}
    """
    static let viewer = """
    {"schema_version":1,"generated_at":"2026-10-30T12:01:00Z","state":"not_required","summary":"No execution worker runs on this machine; a local Offload is not required.","workers":[]}
    """

    func testDecodesCLIDocumentAndShowsSameActionableText() throws {
        let status = try ExecutionStatus.decode(Data(Self.blocked.utf8))
        XCTAssertTrue(status.blocked)
        XCTAssertEqual(status.affected.map(\.worker), ["creds-laptop"])
        let lines = status.lines(for: status.affected[0])
        XCTAssertTrue(lines.contains("Configured command: offload"))
        XCTAssertTrue(lines.contains { $0.contains("go install github.com/zhoushoujianwork/offload@latest") })
        XCTAssertTrue(lines.contains("Docs: https://github.com/zhoushoujianwork/offload#quick-start"))
        XCTAssertTrue(lines.contains("Recheck: clawflow execution status --recheck"))
        let healthy = status.lines(for: status.workers[1])
        XCTAssertTrue(healthy.contains("Live authentication is not verified."))
        XCTAssertFalse(healthy.contains { $0.localizedCaseInsensitiveContains("authenticated") && !$0.contains("not verified") })
    }

    func testServiceRowMapsToCLIWorkerName() throws {
        let status = try ExecutionStatus.decode(Data(Self.blocked.utf8))
        XCTAssertEqual(status.readiness(forService: "com.zhoushoujianwork.clawflow.worker-creds-laptop")?.state, "blocked")
        XCTAssertNil(status.readiness(forService: "com.zhoushoujianwork.clawflow.controller"))
    }

    func testViewerOnlyIsNotAttention() throws {
        let status = try ExecutionStatus.decode(Data(Self.viewer.utf8))
        XCTAssertFalse(status.needsAttention)
        XCTAssertNil(status.attentionKey)
    }

    func testRejectsUnknownSchema() {
        XCTAssertThrowsError(try ExecutionStatus.decode(Data(#"{"schema_version":2,"state":"ready","summary":"x","workers":[]}"#.utf8)))
    }

    func testRepeatedFailureNotifiesOnceAndRecoveryClearsWarning() throws {
        let model = ExecutionReadiness()
        var posted: [String] = []
        model.notify = { posted.append($0) }
        let blocked = try ExecutionStatus.decode(Data(Self.blocked.utf8))
        for _ in 0..<10 { model.apply(blocked) }
        XCTAssertEqual(posted.count, 1)
        XCTAssertTrue(model.status?.needsAttention ?? false)
        model.apply(try ExecutionStatus.decode(Data(Self.ready.utf8)))
        XCTAssertFalse(model.status?.needsAttention ?? true)
        model.apply(blocked)
        XCTAssertEqual(posted.count, 2, "a new outage after recovery may notify again")
    }

    func testMenuBarAttentionFollowsReadiness() throws {
        let model = DashboardModel()
        XCTAssertFalse(model.offloadNeedsAttention)
        model.executionReadiness.apply(try ExecutionStatus.decode(Data(Self.blocked.utf8)))
        XCTAssertTrue(model.offloadNeedsAttention)
        model.executionReadiness.apply(try ExecutionStatus.decode(Data(Self.ready.utf8)))
        XCTAssertFalse(model.offloadNeedsAttention)
    }
}
