import AppKit
import SwiftUI

struct ConnectionView: View {
    @ObservedObject var model: DashboardModel
    @State private var draft = Connection.load()
    @State private var testing = false
    @State private var message: String?
    var body: some View {
        Form {
            Section("Controller connection") {
                TextField("HTTPS address", text: $draft.url, prompt: Text("https://controller.local:8093"))
                TextField("Admin token file", text: $draft.tokenFile, prompt: Text("~/.clawflow/dashboard.token"))
                TextField("Certificate SHA-256", text: $draft.fingerprint)
                Text("Use the address and fingerprint supplied by your controller administrator. Secrets stay in the existing private token file; they are never stored in app preferences.").font(.caption).foregroundStyle(.secondary)
            }
            Section("Application") {
                TextField("CLI path (optional)", text: $draft.executable)
                Text("Uses the bundled CLI by default. An empty controller address uses existing clawflow platform settings.").font(.caption).foregroundStyle(.secondary)
            }
            HStack {
                if testing { ProgressView().controlSize(.small) }
                Text(message ?? "").font(.caption).textSelection(.enabled)
                Spacer()
                Button("Test and Save") { Task { await save() } }.disabled(testing || model.busy).keyboardShortcut(.defaultAction)
            }
            Section("Host a controller") {
                Text("On the host, configure PostgreSQL and enable settings.coordinator.enabled, then run:").font(.caption)
                Text("clawflow controller service install\nclawflow worker token create --name dashboard --scope admin").font(.caption.monospaced()).textSelection(.enabled)
                Text("Save the admin token to a private file and enter its path above. Stop the legacy web service before installing the controller. No service starts when you open this app.").font(.caption).foregroundStyle(.secondary)
            }
        }.formStyle(.grouped).frame(width: 580, height: 560).onAppear { draft = model.connection }
    }
    private func save() async {
        testing = true; message = nil; defer { testing = false }
        do {
            let data = try await BridgeProcess().run(executable: draft.cli, arguments: draft.arguments(["snapshot"]))
            let snapshot = try DashboardSnapshot.decode(data)
            model.configure(draft)
            message = "Connected to \(snapshot.controller)."
        } catch { message = error.localizedDescription }
    }
}

struct AddWorkerView: View {
    @ObservedObject var model: DashboardModel
    @Environment(\.dismiss) private var dismiss
    @State private var name = ""
    @State private var offer: JoinOffer?
    @State private var error: String?
    @State private var issuing = false
    var body: some View {
        VStack(alignment: .leading, spacing: 16) {
            Text("Add LAN worker").font(.title2)
            if let offer {
                Text("Invitation for \(offer.name) · expires \(offer.expiresAt, style: .time)")
                Text("1. Run on the worker host:").font(.headline)
                command(model.connection.joinCommand())
                Text("2. Paste this invitation at the prompt:").font(.headline)
                Text(offer.secret).font(.caption.monospaced()).textSelection(.enabled)
                Text("3. Start the worker with its local Offload profile and project tag:").font(.headline)
                command(model.connection.startCommand())
                Text("The worker appears with assignments paused. Check its tags and roles, then select Resume assignments. This invitation works once; keep the saved worker token private.").font(.callout).foregroundStyle(.secondary)
            } else {
                TextField("Worker name", text: $name, prompt: Text("linux-builder"))
                Text("Creates a one-use invitation valid for ten minutes. The worker connects outbound over pinned HTTPS and keeps its execution credentials locally.").foregroundStyle(.secondary)
                if model.connection.url.isEmpty || model.connection.fingerprint.isEmpty {
                    Text("Enter the controller's LAN HTTPS address and fingerprint in Settings before inviting workers.").foregroundStyle(.orange)
                }
                if let error { Text(error).foregroundStyle(.orange).textSelection(.enabled) }
            }
            HStack {
                if issuing { ProgressView().controlSize(.small) }
                Spacer()
                Button(offer == nil ? "Cancel" : "Done") { dismiss() }.keyboardShortcut(.cancelAction).disabled(issuing)
                if offer == nil {
                    Button("Create invitation") { Task { await create() } }.buttonStyle(.borderedProminent)
                        .disabled(issuing || name.isEmpty || model.connection.url.isEmpty || model.connection.fingerprint.isEmpty)
                }
            }
        }.padding(24).frame(width: 600)
            .interactiveDismissDisabled(issuing)
    }
    private func command(_ text: String) -> some View {
        HStack(alignment: .top) {
            Text(text).font(.caption.monospaced()).textSelection(.enabled).frame(maxWidth: .infinity, alignment: .leading)
            Button { NSPasteboard.general.clearContents(); NSPasteboard.general.setString(text, forType: .string) } label: { Image(systemName: "doc.on.doc") }.help("Copy command")
        }.padding(10).background(.quaternary, in: RoundedRectangle(cornerRadius: 6))
    }
    private func create() async {
        issuing = true; error = nil; defer { issuing = false }
        do { offer = try await model.invite(name: name) }
        catch { self.error = error.localizedDescription + " If the response was lost, use a new worker name; an existing invitation is never replaced." }
    }
}
