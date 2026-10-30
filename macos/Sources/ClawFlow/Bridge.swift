import Foundation

struct BridgeFailure: LocalizedError {
    let diagnostic: String
    var errorDescription: String? {
        diagnostic.components(separatedBy: .newlines).first { !$0.trimmingCharacters(in: .whitespaces).isEmpty } ?? diagnostic
    }
}

struct Connection: Codable, Equatable {
    var executable = ""
    var url = ""
    var tokenFile = ""
    var fingerprint = ""
    var isLocalHost: Bool { ["localhost", "127.0.0.1", "::1"].contains(URL(string: url)?.host ?? "") }
    static func load() -> Self {
        guard let data = UserDefaults.standard.data(forKey: "controllerConnection"), let value = try? JSONDecoder().decode(Self.self, from: data) else { return Self() }
        return value
    }
    func save() { UserDefaults.standard.set(try? JSONEncoder().encode(self), forKey: "controllerConnection") }
    var cli: String {
        if !executable.isEmpty { return NSString(string: executable).expandingTildeInPath }
        if let helpers = Bundle.main.executableURL?.deletingLastPathComponent().deletingLastPathComponent() {
            let candidate = helpers.appendingPathComponent("Helpers/clawflow").path
            if FileManager.default.isExecutableFile(atPath: candidate) { return candidate }
        }
        return FileManager.default.homeDirectoryForCurrentUser.appendingPathComponent(".clawflow/bin/clawflow").resolvingSymlinksInPath().path
    }
    func arguments(_ command: [String]) -> [String] {
        var args = ["dashboard"] + command
        if !url.isEmpty { args += ["--url", url, "--token-file", NSString(string: tokenFile).expandingTildeInPath, "--fingerprint", fingerprint] }
        return args
    }
    func joinCommand(workerName: String = "native-worker") -> String {
        func quote(_ s: String) -> String { "'" + s.replacingOccurrences(of: "'", with: "'\\''") + "'" }
        return "clawflow worker join --coordinator \(quote(url)) --fingerprint \(quote(fingerprint)) --token-file ~/.clawflow/workers/\(workerName).token"
    }
    func startCommand(workerName: String = "native-worker") -> String { joinCommand(workerName: workerName).replacingOccurrences(of: "worker join", with: "worker") + " --state-dir ~/.clawflow/workers/\(workerName)/state --roles operator --run-untagged=false --tags YOUR_PROJECT_TAG" }
}

/// Each bridge invocation is bounded, executes an argument array without a shell,
/// and drains stdout/stderr concurrently. It never launches an execution worker.
final class BridgeProcess: @unchecked Sendable {
    private let lock = NSLock()
    private let process = Process()
    private var finished = false
    private var stopped = false
    private var output = Data()
    private var diagnostic = Data()
    private let limit = 8 * 1024 * 1024

    func stop() {
        lock.lock(); defer { lock.unlock() }
        stopped = true
        if !finished && process.isRunning { process.terminate() }
        DispatchQueue.global().asyncAfter(deadline: .now() + 1) { [self] in
            lock.lock(); defer { lock.unlock() }
            if !finished && process.isRunning { kill(process.processIdentifier, SIGKILL) }
        }
    }
    func run(executable: String, arguments: [String], timeout: TimeInterval = 20) async throws -> Data {
        try await withTaskCancellationHandler(operation: {
            try await withCheckedThrowingContinuation { continuation in
                DispatchQueue.global().async { [self] in
                    do {
                        let out = Pipe(), err = Pipe()
                        process.executableURL = URL(fileURLWithPath: executable)
                        process.arguments = arguments
                        process.standardInput = FileHandle.nullDevice
                        process.standardOutput = out; process.standardError = err
                        lock.lock()
                        if stopped { lock.unlock(); throw CancellationError() }
                        do { try process.run(); lock.unlock() } catch { lock.unlock(); throw error }
                        try? out.fileHandleForWriting.close(); try? err.fileHandleForWriting.close()
                        let readers = DispatchGroup()
                        for (handle, isError) in [(out.fileHandleForReading, false), (err.fileHandleForReading, true)] {
                            readers.enter()
                            DispatchQueue.global().async { [self] in
                                defer { try? handle.close(); readers.leave() }
                                while true {
                                    let data = handle.availableData
                                    if data.isEmpty { break }
                                    lock.lock()
                                    let overflow = (isError ? diagnostic.count : output.count) + data.count > limit
                                    if !overflow { if isError { diagnostic.append(data) } else { output.append(data) } }
                                    lock.unlock()
                                    if overflow { stop() }
                                }
                            }
                        }
                        let deadline = DispatchWorkItem { [weak self] in self?.stop() }
                        DispatchQueue.global().asyncAfter(deadline: .now() + timeout, execute: deadline)
                        process.waitUntilExit()
                        lock.lock(); finished = true; lock.unlock()
                        deadline.cancel(); readers.wait()
                        lock.lock(); let cancelled = stopped; let data = output; let errors = diagnostic; lock.unlock()
                        if cancelled { throw DashboardError.message("Controller request timed out or was cancelled. Refresh before repeating a change.") }
                        guard process.terminationStatus == 0 else {
                            let raw = String(data: errors.prefix(2048), encoding: .utf8) ?? "Controller request failed."
                            throw BridgeFailure(diagnostic: raw)
                        }
                        continuation.resume(returning: data)
                    } catch { continuation.resume(throwing: error) }
                }
            }
        }, onCancel: { self.stop() })
    }
}
