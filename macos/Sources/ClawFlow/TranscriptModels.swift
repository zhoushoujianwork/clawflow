import Foundation

/// Offload execution output, read through the coordinator's bounded
/// `/admin/v1/tasks/{id}/transcript` endpoint. This is content evidence, not
/// the scheduling activity feed in `TaskEvent`; the two are never conflated.
struct TranscriptEpochInfo: Decodable, Identifiable, Sendable {
    let epoch: Int64
    let attempt: Int64
    let worker: String
    let status: String
    let eventCount: Int64
    let endOfStream: Bool
    let attemptState: String?
    var id: Int64 { epoch }
}

/// One Offload transcript event. Only the fields the app renders are
/// decoded; unknown keys (ts, run_id, caller_run_id, ...) are ignored.
struct TranscriptEventRow: Decodable, Identifiable, Sendable {
    let seq: Int64
    let type: String
    let text: String?
    let toolName: String?
    let toolCallId: String?
    let arguments: String?
    let output: String?
    let isError: Bool?
    let exitCode: Int?
    let reason: String?
    var id: Int64 { seq }
}

/// One bounded page returned by the controller for a single execution
/// (epoch + invocation). The viewer never infers content from this; every
/// field is relayed, and absence of output is distinct from "not captured".
struct TranscriptView: Decodable, Sendable {
    let schemaVersion: Int
    let taskId: String
    let taskState: String?
    let epoch: Int64
    let invocation: Int
    let invocations: Int
    let attempt: Int64
    let worker: String?
    let status: String
    let reason: String?
    let executionDone: Bool
    let live: Bool
    let completeness: String?
    let gap: Bool
    let gapReason: String?
    let capped: Bool
    let events: [TranscriptEventRow]
    let nextAfter: Int64
    let hasMore: Bool
    let epochs: [TranscriptEpochInfo]

    /// Readable explanation for every distinct status the controller can
    /// return. Never claims content that was not captured or has expired.
    var statusText: String {
        switch status {
        case "available": return "Live output"
        case "no_output_yet": return live ? "Running; no output yet" : "No output was captured"
        case "no_content": return "Execution ended without capturable content"
        case "not_captured": return "Not captured for this execution"
        case "unsupported": return "Offload version does not support transcript capture"
        case "expired": return "Removed by retention"
        default: return "Output unavailable"
        }
    }
}
