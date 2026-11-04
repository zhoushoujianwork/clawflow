import SwiftUI

/// Only the elapsed label ticks; scrolling lists do not observe a global clock.
/// Shows the current attempt's duration in Chinese, "—" when the start or end time is unknown.
struct TaskElapsedText: View {
    let row: TaskRow
    let connected: Bool
    let frozenAt: Date

    var body: some View {
        if connected && row.group == .running && row.durationAvailable {
            TimelineView(.periodic(from: .now, by: 1)) { context in
                Text(row.durationZh(at: context.date))
            }
        } else {
            Text(row.durationZh(at: frozenAt))
        }
    }
}
