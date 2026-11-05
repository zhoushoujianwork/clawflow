import Foundation

/// Distinguishes the installed app from a disposable development candidate.
/// A candidate has its own bundle identifier, so UserDefaults (the preferences
/// domain) is already separate; this type additionally keeps it away from the
/// stable host's launchd services and CLI.
struct AppIdentity: Equatable {
    static let stableBundleID = "com.clawflow.dashboard"
    static let candidatePrefix = stableBundleID + ".dev."
    let bundleID: String

    static var current: AppIdentity { AppIdentity(bundleID: Bundle.main.bundleIdentifier ?? stableBundleID) }

    /// 12 lowercase hex characters, matching the Go candidate id.
    var candidateID: String? {
        guard bundleID.hasPrefix(Self.candidatePrefix) else { return nil }
        let id = String(bundleID.dropFirst(Self.candidatePrefix.count))
        let hex = Set("0123456789abcdef")
        return id.count == 12 && id.allSatisfy({ hex.contains($0) }) ? id : nil
    }
    /// True for any bundle in the candidate namespace, even a malformed one,
    /// so a bad identifier fails closed instead of acting as the stable app.
    var isCandidate: Bool { bundleID.hasPrefix(Self.candidatePrefix) }
    var servicePrefix: String? { candidateID.map { "com.zhoushoujianwork.clawflow.dev.\($0)." } }

    /// May this app manage the launchd service with this label?
    func manages(_ label: String) -> Bool {
        if isCandidate {
            guard let prefix = servicePrefix else { return false }
            return label.hasPrefix(prefix)
        }
        guard !label.hasPrefix("com.zhoushoujianwork.clawflow.dev.") else { return false }
        return label == "com.zhoushoujianwork.clawflow.controller" || label.hasPrefix("com.zhoushoujianwork.clawflow.worker-")
    }
    /// Candidates never start services on launch and never use the installed CLI.
    var startsHostServices: Bool { !isCandidate }
    var allowsInstalledCLIFallback: Bool { !isCandidate }
}
