import SwiftUI

@MainActor final class DashboardModel: ObservableObject {
    let localServices = LocalServices()
    @Published var connection = Connection.load()
    @Published private(set) var snapshot: DashboardSnapshot?
    @Published private(set) var received: Date?
    @Published private(set) var error: String?
    @Published private(set) var busy = false
    @Published var actionError: String?
    @Published var now = Date()
    private var polling: Task<Void, Never>?
    private var freshnessTimer: Timer?
    private var refreshing = false
    private var generation = 0
    var summary: StatusSummary { .reduce(snapshot: snapshot, received: received, error: error, now: now) }

    func start() {
        guard polling == nil else { return }
        localServices.start()
        freshnessTimer = Timer.scheduledTimer(withTimeInterval: 1, repeats: true) { [weak self] _ in
            Task { @MainActor in self?.now = Date() }
        }
        polling = Task { [weak self] in
            while !Task.isCancelled {
                await self?.refresh()
                try? await Task.sleep(for: .seconds(5))
            }
        }
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
        defer { refreshing = false; now = Date() }
        do {
            let data = try await request(["snapshot"])
            let next = try DashboardSnapshot.decode(data)
            guard requestGeneration == generation else { return }
            snapshot = next; received = Date(); error = nil
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
        busy = true; actionError = nil
        defer { busy = false }
        do { _ = try await request([action, name]); await refresh() }
        catch { actionError = error.localizedDescription + " Refresh before repeating the action." }
    }
    func invite(name: String) async throws -> JoinOffer {
        guard summary.connected, !busy else { throw DashboardError.message("Connect to the controller first.") }
        busy = true; defer { busy = false }
        return try decoder().decode(JoinOffer.self, from: await request(["invite", name]))
    }
}
