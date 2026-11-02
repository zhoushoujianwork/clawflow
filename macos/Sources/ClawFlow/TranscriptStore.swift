import Foundation

/// Buffers one execution's Offload transcript as pages arrive from the
/// controller's bounded read endpoint. Pure state: no networking, no timers,
/// so it is covered headlessly. A retry (new lease epoch) is a different
/// execution; its content never mixes with the previous attempt's buffer.
@MainActor final class TranscriptStore: ObservableObject {
    @Published private(set) var events: [TranscriptEventRow] = []
    @Published private(set) var status = "no_output_yet"
    @Published private(set) var reason: String?
    @Published private(set) var gap = false
    @Published private(set) var gapReason: String?
    @Published private(set) var capped = false
    @Published private(set) var live = false
    @Published private(set) var hasMore = false
    @Published private(set) var epochs: [TranscriptEpochInfo] = []
    @Published private(set) var currentEpoch: Int64 = 0
    /// nil = always show the latest execution attempt. Set when the user picks
    /// an older attempt explicitly; a new retry then does not pull them away.
    @Published private(set) var pinnedEpoch: Int64?
    @Published var loadError: String?
    /// True while the viewer is scrolled to (or has not left) the bottom.
    /// Only in that state does a new page auto-scroll.
    @Published var follow = true

    private var cursor: Int64 = 0
    private var seenThroughSeq: Int64 = 0

    /// nil tells the request layer to let the controller pick the latest epoch.
    var requestEpoch: Int64? { pinnedEpoch }
    var requestAfter: Int64 { cursor }

    func select(epoch: Int64) {
        let wasFollowingThisEpoch = pinnedEpoch == nil && epoch == currentEpoch
        pinnedEpoch = epoch
        guard !wasFollowingThisEpoch else { return }
        resetBuffer()
    }
    func followLatest() {
        guard pinnedEpoch != nil else { return }
        pinnedEpoch = nil
        resetBuffer()
    }
    private func resetBuffer() {
        events = []; cursor = 0; seenThroughSeq = 0; hasMore = false
        status = "no_output_yet"; reason = nil; gap = false; gapReason = nil; capped = false; live = false
    }

    /// Applies one controller page. Events already held (duplicate retry,
    /// out-of-order redelivery) are dropped by sequence number, not replayed.
    func apply(_ page: TranscriptView) {
        loadError = nil
        if currentEpoch != 0, page.epoch != currentEpoch, pinnedEpoch == nil {
            // The active execution retried into a new epoch while we were
            // following "latest": start that attempt's log from empty, never
            // appending its output after the previous attempt's tail.
            resetBuffer()
        }
        currentEpoch = page.epoch
        epochs = page.epochs
        status = page.status
        reason = page.reason
        gap = page.gap
        gapReason = page.gapReason
        capped = page.capped
        live = page.live
        hasMore = page.hasMore
        for event in page.events where event.seq > seenThroughSeq {
            events.append(event)
            seenThroughSeq = event.seq
        }
        cursor = max(cursor, page.nextAfter)
    }
}
