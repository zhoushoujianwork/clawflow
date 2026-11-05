import Foundation

/// Feedback for a repository or worker action while the controller applies it.
struct PendingAction: Equatable {
    let action: String
    let name: String
}
