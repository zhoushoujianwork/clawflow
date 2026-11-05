import Combine
import SwiftUI

@MainActor final class DashboardModel: ObservableObject {
    let localServices = LocalServices()
    let executionReadiness = ExecutionReadiness()
    @Published var connection = Connection.load()
    @Published private(set) var snapshot: DashboardSnapshot?
    @Published private(set) var received: Date?
    @Published private(set) var error: String?
    @Published private(set) var busy = false
    @Published var actionError: String?
    /// The monitoring/worker change currently awaiting the controller; shown as pending feedback.
    @Published private(set) var pendingAction: PendingAction?
    private(set) var now = Date()
    private var polling: Task<Void, Never>?
    private var executionLoop: Task<Void, Never>?
    private var forwarding: AnyCancellable?
    private var freshnessTimer: Timer?
    private var refreshing = false
    private var generation = 0
    var summary: StatusSummary { .reduce(snapshot: snapshot, received: received, error: error, now: now) }
    /// Menu-bar attention includes an unusable local executor, not only task attention.
    var offloadNeedsAttention: Bool { executionReadiness.status?.needsAttention ?? false }

    func start() {
        guard polling == nil else { return }
        localServices.start(ensureRunning: connection.isLocalHost)
        forwarding = executionReadiness.objectWillChange.sink { [weak self] _ in self?.objectWillChange.send() }
        executionReadiness.notify = { Notifier.post("Offload needs setup", body: $0) }
        executionLoop = Task { [weak self] in
            // The CLI serves a cached result, so this loop never causes a probe per tick.
            while !Task.isCancelled {
                if let self { await self.executionReadiness.refresh(cli: self.connection.cli) }
                try? await Task.sleep(for: .seconds(15))
            }
        }
        let timer = Timer(timeInterval: 1, repeats: true) { [weak self] _ in
            Task { @MainActor [weak self] in self?.advanceClock(to: Date()) }
        }
        RunLoop.main.add(timer, forMode: .common)
        freshnessTimer = timer
        polling = Task { [weak self] in
            while !Task.isCancelled {
                await self?.refresh()
                try? await Task.sleep(for: .seconds(5))
            }
        }
    }
    // A clock tick must not invalidate every table cell. Only freshness transitions
    // change global state; live elapsed labels own their small TimelineView.
    func advanceClock(to date: Date) {
        let next = StatusSummary.reduce(snapshot: snapshot, received: received, error: error, now: date)
        if next != summary { objectWillChange.send() }
        now = date
    }
    func acceptSnapshot(_ next: DashboardSnapshot, at date: Date) {
        now = date
        snapshot = next; received = date; error = nil
    }
    func configure(_ connection: Connection) {
        generation += 1
        self.connection = connection; connection.save()
        snapshot = nil; received = nil; error = nil; actionError = nil
        Task { await refresh() }
    }
    func refresh() async {
        guard !refreshing else { return }
        refreshing = true
        let requestGeneration = generation
        defer { refreshing = false; advanceClock(to: Date()) }
        do {
            let data = try await request(["snapshot"])
            let next = try await DashboardSnapshot.decodeInBackground(data)
            guard requestGeneration == generation else { return }
            acceptSnapshot(next, at: Date())
        } catch {
            guard requestGeneration == generation else { return }
            self.error = error.localizedDescription
        }
    }
    func request(_ command: [String]) async throws -> Data {
        let c = connection
        return try await BridgeProcess().run(executable: c.cli, arguments: c.arguments(command))
    }
    func perform(_ action: String, name: String) async {
        guard summary.connected, !busy else { return }
        busy = true; actionError = nil; pendingAction = PendingAction(action: action, name: name)
        defer { busy = false; pendingAction = nil }
        do { _ = try await request([action, name]); await refresh() }
        catch { actionError = error.localizedDescription + " Refresh before repeating the action." }
    }
    func invite(name: String) async throws -> JoinOffer {
        guard summary.connected, !busy else { throw DashboardError.message("Connect to the controller first.") }
        busy = true; defer { busy = false }
        return try decoder().decode(JoinOffer.self, from: await request(["invite", name]))
    }
}
