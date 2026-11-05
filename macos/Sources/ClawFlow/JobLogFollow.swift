import Foundation

/// Decides whether the log should stay pinned to the newest line. Kept free of
/// SwiftUI so the rules can be tested headlessly.
///
/// Following is on by default. Scrolling up (the bottom marker leaves the
/// viewport) or pressing Pause stops it; the view is then never moved by new
/// output. Reaching the bottom again resumes automatic following, unless the
/// user paused explicitly.
struct JobLogFollowState: Equatable {
    private(set) var paused = false
    private(set) var awayFromBottom = false
    private var ignoreDisappearUntil = Date.distantPast
    /// Layout of a freshly appended line briefly moves the bottom marker off screen.
    static let grace: TimeInterval = 0.6

    var isFollowing: Bool { !paused && !awayFromBottom }

    /// Returns true when the view should move to the newest line.
    mutating func linesAppended(at now: Date) -> Bool {
        guard isFollowing else { return false }
        ignoreDisappearUntil = now.addingTimeInterval(Self.grace)
        return true
    }
    mutating func bottomMarkerAppeared() { awayFromBottom = false }
    mutating func bottomMarkerDisappeared(at now: Date) {
        if now < ignoreDisappearUntil { return }
        awayFromBottom = true
    }
    mutating func pause() { paused = true }
    /// Resuming always jumps to the newest line.
    mutating func resume(at now: Date) {
        paused = false; awayFromBottom = false
        ignoreDisappearUntil = now.addingTimeInterval(Self.grace)
    }
}
