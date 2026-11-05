import Foundation

/// Execution output of one job, as served by the controller's bounded log API.
/// Scheduling events are a different feature and never enter this model.
struct JobLogResponse: Decodable, Sendable {
    struct Epoch: Decodable, Sendable, Equatable { let epoch: Int; let attempt: Int; let worker: String; let status: String }
    struct Stream: Decodable, Sendable, Equatable {
        let invocation: String
        let offloadRunId: String?
        let backend: String?
        let model: String?
        let availability: String
        let reason: String?
        let state: String
        let completeness: String?
        let eventCount: Int
        let droppedEvents: Int?
        let limited: Bool?
    }
    struct Entry: Decodable, Sendable { let id: Int; let invocation: String; let event: JobLogEvent }
    let taskId: String
    let epoch: Int
    let epochs: [Epoch]
    let active: Bool
    let availability: String
    let streams: [Stream]
    let entries: [Entry]
    let nextAfter: Int
    let hasMore: Bool
}

struct JobLogEvent: Decodable, Sendable, Equatable {
    let seq: Int
    let ts: String?
    let type: String
    let role: String?
    let text: String?
    let toolCallId: String?
    let toolName: String?
    let toolCategory: String?
    let arguments: String?
    let output: String?
    let isError: Bool?
    let exitCode: Int?
    let reason: String?
    let bytes: Int?
    let status: String?
    let errorKind: String?
    let truncated: [String: Int]?
}

/// What the UI should say when there is no output to show. Each case is a
/// different truth about the execution and must not be merged with another.
enum JobLogNotice: Equatable {
    case notCaptured            // no output was reported for this execution (yet)
    case captureDisabled        // Offload ran without transcript capture
    case unsupportedOffload     // the worker's Offload has no capture support
    case unsupportedBackend
    case waiting                // capture is on but nothing has been written yet
    case expired
    case unreadable
    case unavailable(String)

    var title: String {
        switch self {
        case .notCaptured: return "No execution output was reported for this job"
        case .captureDisabled: return "Output capture was not enabled for this execution"
        case .unsupportedOffload: return "The worker's Offload cannot capture output"
        case .unsupportedBackend: return "This backend does not expose output"
        case .waiting: return "Waiting for the first output"
        case .expired: return "Output expired and was removed"
        case .unreadable: return "Output could not be read"
        case .unavailable(let value): return "Output unavailable (\(value))"
        }
    }
    var detail: String {
        switch self {
        case .notCaptured: return "ClawFlow does not rebuild output for earlier executions or from scheduling events."
        case .captureDisabled: return "Executions started without capture have no recorded output."
        case .unsupportedOffload: return "Update Offload on the worker; running jobs are not restarted to collect output."
        case .unsupportedBackend: return "Only backends that expose messages and tool calls can be shown."
        case .waiting: return "The job may still be starting or thinking. This view will fill in automatically."
        case .expired: return "Output is kept for a limited time after the job finishes."
        case .unreadable: return "The worker could not read the recorded output."
        case .unavailable: return "The controller reported this state; no new model session is started to view it."
        }
    }
    static func from(availability: String) -> JobLogNotice {
        switch availability {
        case "not_captured": return .notCaptured
        case "capture_disabled": return .captureDisabled
        case "unsupported_offload": return .unsupportedOffload
        case "unsupported_backend", "unsupported_version": return .unsupportedBackend
        case "not_yet_available", "no_captured_content": return .waiting
        case "expired": return .expired
        case "unreadable": return .unreadable
        default: return .unavailable(availability)
        }
    }
}

/// One rendered row. The id is stable (controller entry id) so appending never
/// re-identifies rows that are already on screen.
struct JobLogLine: Identifiable, Equatable, Sendable {
    enum Kind: Equatable, Sendable { case user, assistant, toolCall, toolResult, error, gap, boundary }
    let id: Int
    let invocation: String
    let kind: Kind
    let title: String
    let body: String
    let isError: Bool
    let truncatedNote: String?
    let time: String?

    init(entry: JobLogResponse.Entry) {
        let e = entry.event
        id = entry.id; invocation = entry.invocation; time = e.ts
        var note: String?
        if let cut = e.truncated, !cut.isEmpty { note = "Shortened by the recorder: " + cut.keys.sorted().joined(separator: ", ") }
        truncatedNote = note
        switch e.type {
        case "message":
            let user = e.role == "user"
            kind = user ? .user : .assistant; title = user ? "Prompt" : "Assistant"; body = e.text ?? ""; isError = false
        case "tool_call":
            kind = .toolCall; title = "Tool · " + (e.toolName ?? "call"); body = e.arguments ?? ""; isError = false
        case "tool_result":
            let failed = e.isError == true || (e.exitCode ?? 0) != 0
            kind = failed ? .error : .toolResult
            title = "Result" + (e.exitCode.map { " · exit \($0)" } ?? "")
            body = e.output ?? ""; isError = failed
        case "backend_error":
            kind = .error; title = "Backend error"; body = e.text ?? e.output ?? e.errorKind ?? ""; isError = true
        case "gap":
            kind = .gap; title = "Output gap"; body = "Some output is missing" + (e.reason.map { " (\($0))" } ?? "") + (e.bytes.map { ", \($0) bytes not stored" } ?? ""); isError = false
        case "attempt_start":
            kind = .boundary; title = "Attempt started"; body = ""; isError = false
        case "execution_end":
            kind = .boundary; title = "Execution ended" + (e.status.map { " · \($0)" } ?? ""); body = e.errorKind ?? ""; isError = e.status != nil && e.status != "ok"
        default:
            kind = .boundary; title = e.type; body = ""; isError = false
        }
    }
    /// Tool calls and results are tied by tool_call_id for the reader.
    var toolCallId: String? = nil
}

/// Pure, testable append buffer. It dedupes by controller entry id, ignores
/// out-of-order replays, and isolates epochs so a retry never mixes into an older attempt.
struct JobLogBuffer: Equatable {
    private(set) var taskId = ""
    private(set) var epoch = 0
    private(set) var lines: [JobLogLine] = []
    private(set) var after = 0
    private(set) var response: JobLogResponse.Epoch?
    private(set) var epochs: [JobLogResponse.Epoch] = []
    private(set) var streams: [JobLogResponse.Stream] = []
    private(set) var active = false
    private(set) var availability = "not_captured"
    private(set) var hasMore = false
    /// Tool call ids to the line index of the call, for correlation display.
    private(set) var callTitles: [String: String] = [:]
    static let maxLines = 5000
    private(set) var evicted = 0

    /// Applies a page. Returns the number of lines appended. A page for a
    /// different task or epoch than requested is dropped, never merged.
    @discardableResult
    mutating func apply(_ page: JobLogResponse, requestedEpoch: Int?) -> Int {
        if taskId.isEmpty { taskId = page.taskId }
        guard page.taskId == taskId else { return 0 }
        if let requested = requestedEpoch, requested != 0, page.epoch != requested { return 0 }
        if epoch != page.epoch {
            // A new epoch (retry) starts a clean view; the previous attempt remains reachable by choosing it.
            epoch = page.epoch; lines = []; after = 0; callTitles = [:]; evicted = 0
        }
        epochs = page.epochs; streams = page.streams; active = page.active; availability = page.availability; hasMore = page.hasMore
        var added = 0
        for entry in page.entries where entry.id > after {
            var line = JobLogLine(entry: entry)
            line.toolCallId = entry.event.toolCallId
            if entry.event.type == "tool_call", let id = entry.event.toolCallId { callTitles[id] = line.title }
            lines.append(line); after = entry.id; added += 1
        }
        if lines.count > Self.maxLines {
            evicted += lines.count - Self.maxLines
            lines.removeFirst(lines.count - Self.maxLines)
        }
        return added
    }

    mutating func reset() { self = JobLogBuffer() }

    var notice: JobLogNotice? {
        guard lines.isEmpty else { return nil }
        if let stream = streams.first, stream.availability != "available" { return .from(availability: stream.availability) }
        if !streams.isEmpty { return .waiting }
        return .from(availability: availability)
    }
    var isLive: Bool { active && streams.contains { $0.state == "live" } }
    var hasGap: Bool { streams.contains { ($0.droppedEvents ?? 0) > 0 || $0.limited == true } || lines.contains { $0.kind == .gap } || evicted > 0 }
    var interrupted: Bool { streams.contains { $0.state == "interrupted" } }
}
