import Foundation
import SwiftUI

struct AppRelease: Decodable {
    let tagName: String
    let htmlUrl: String
    let draft: Bool
    let prerelease: Bool

    func message(current: String, channel: String) -> String {
        if channel == "dev" { return "Latest release: \(tagName). This app is a dev build; use make dev for local updates." }
        let latest = tagName.trimmingCharacters(in: CharacterSet(charactersIn: "v"))
        let installed = current.trimmingCharacters(in: CharacterSet(charactersIn: "v"))
        return latest.compare(installed, options: .numeric) == .orderedDescending
            ? "Release \(tagName) is available." : "Latest release: \(tagName)."
    }
    var url: URL? {
        guard let url = URL(string: htmlUrl), url.scheme == "https", url.host == "github.com",
              url.path.hasPrefix("/zhoushoujianwork/clawflow/releases/"), url.user == nil, url.password == nil else { return nil }
        return url
    }
}

struct AppUpdatesView: View {
    @State private var checking = false
    @State private var message: String?
    @State private var release: AppRelease?
    private var version: String { Bundle.main.object(forInfoDictionaryKey: "CFBundleShortVersionString") as? String ?? "unknown" }
    private var channel: String { Bundle.main.object(forInfoDictionaryKey: "ClawFlowBuildChannel") as? String ?? "source" }
    var body: some View {
        Section("App updates") {
            Text("ClawFlow \(version) · \(channel)").font(.caption).textSelection(.enabled)
            HStack {
                Button(checking ? "Checking…" : "Check for release updates") { Task { await check() } }.disabled(checking)
                if let url = release?.url { Link("Open release", destination: url) }
            }
            if let message { Text(message).font(.caption).foregroundStyle(.secondary) }
            Text("Local development: make dev builds, installs and restarts the app. Release checks do not change the app or host services.").font(.caption).foregroundStyle(.secondary)
        }
    }
    @MainActor private func check() async {
        checking = true; message = nil; release = nil
        defer { checking = false }
        do {
            var request = URLRequest(url: URL(string: "https://api.github.com/repos/zhoushoujianwork/clawflow/releases/latest")!)
            request.timeoutInterval = 15
            request.setValue("application/vnd.github+json", forHTTPHeaderField: "Accept")
            let (data, response) = try await URLSession.shared.data(for: request)
            guard let response = response as? HTTPURLResponse else { throw DashboardError.message("Invalid release response.") }
            guard response.statusCode == 200 else { throw DashboardError.message("Release check failed (HTTP \(response.statusCode)). Try again later.") }
            let result = try decoder().decode(AppRelease.self, from: data)
            guard !result.draft, !result.prerelease, !result.tagName.isEmpty, result.url != nil else { throw DashboardError.message("No valid stable release found.") }
            release = result; message = result.message(current: version, channel: channel)
        } catch { message = error.localizedDescription }
    }
}
