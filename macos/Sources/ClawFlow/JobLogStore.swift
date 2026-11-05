import Foundation
import SwiftUI

/// Reads one job's output through the controller bridge. It is owned by the log
/// view only, so appends invalidate that view and nothing else on the dashboard.
/// Opening or closing it never changes the execution: it only issues read-only
/// controller requests and starts no provider session.
@MainActor final class JobLogStore: ObservableObject {
    @Published private(set) var buffer = JobLogBuffer()
    @Published private(set) var error: String?
    @Published private(set) var loading = false
    @Published private(set) var selectedEpoch: Int = 0
    private let taskId: String
    private let fetch: (_ taskId: String, _ epoch: Int, _ after: Int) async throws -> Data
    private var loop: Task<Void, Never>?
    static let liveInterval: Duration = .seconds(2)
    static let idleInterval: Duration = .seconds(10)

    init(taskId: String, fetch: @escaping (_ taskId: String, _ epoch: Int, _ after: Int) async throws -> Data) {
        self.taskId = taskId; self.fetch = fetch
    }

    /// Replays what exists, then follows while the execution is live and the app is connected.
    func start(isConnected: @escaping @MainActor () -> Bool) {
        guard loop == nil else { return }
        loop = Task { [weak self] in
            while !Task.isCancelled {
                guard let self else { return }
                if isConnected() { await self.poll() }
                // Keep draining immediately while more pages are waiting.
                if self.buffer.hasMore { continue }
                let wait = self.buffer.isLive || self.buffer.availability == "not_captured" && self.buffer.active ? Self.liveInterval : Self.idleInterval
                try? await Task.sleep(for: wait)
            }
        }
    }
    func stop() { loop?.cancel(); loop = nil }

    /// Switches to another execution attempt without touching the current one's data on the controller.
    func select(epoch: Int) {
        guard epoch != selectedEpoch else { return }
        selectedEpoch = epoch
        buffer.reset()
        Task { await poll() }
    }

    func poll() async {
        guard !loading else { return }
        loading = true; defer { loading = false }
        let epoch = selectedEpoch, after = buffer.after
        do {
            let data = try await fetch(taskId, epoch, after)
            guard !Task.isCancelled, epoch == selectedEpoch else { return }
            let page = try await Task.detached(priority: .userInitiated) { try decoder().decode(JobLogResponse.self, from: data) }.value
            guard !Task.isCancelled, epoch == selectedEpoch else { return }
            // Selecting the newest epoch (0) follows retries; the buffer isolates the new attempt.
            buffer.apply(page, requestedEpoch: epoch)
            error = nil
        } catch {
            if !Task.isCancelled { self.error = error.localizedDescription }
        }
    }
}
