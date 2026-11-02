import SwiftUI

/// Only the elapsed label ticks; scrolling lists do not observe a global clock.
struct TaskElapsedText: View {
    let row: TaskRow
    let connected: Bool
    let frozenAt: Date
    var unavailable = "Duration unavailable"

    var body: some View {
        if !row.durationAvailable {
            Text(unavailable)
        } else if connected && row.group == .running {
            TimelineView(.periodic(from: .now, by: 1)) { context in
                Text(row.elapsed(at: context.date))
            }
        } else {
            Text(row.elapsed(at: frozenAt))
        }
    }
}
