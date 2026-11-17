import Observation
import RACore
import SwiftUI

/// What the 「连接」group of a computer's settings page shows (design-system §6): the direct path's
/// switch, state and measured numbers, drawn from RACore's `DirectView`, which the connection check
/// reads too. Kept in a model so that "trying" after a switch-on and the refresh on opening are tested
/// without a screen.
@MainActor
@Observable
final class DirectConnectionModel {
    /// How long a direct path just turned on may say "trying" before it reports anything: a round of
    /// attempts is bounded at about this long.
    static let tryingSeconds: Double = 10

    private(set) var view: DirectView
    private(set) var error = ""
    @ObservationIgnored private let status: @MainActor () -> DirectStatus
    @ObservationIgnored private let setEnabled: @MainActor (Bool) throws -> Void
    @ObservationIgnored private var turnedOn: Date?
    @ObservationIgnored private let now: () -> Date

    init(status: @escaping @MainActor () -> DirectStatus, setEnabled: @escaping @MainActor (Bool) throws -> Void, now: @escaping () -> Date = Date.init) {
        self.status = status
        self.setEnabled = setEnabled
        self.now = now
        view = DirectView.make(status())
    }

    convenience init(remote: Remote, machine: String) {
        self.init(status: { remote.directStatus(machine) }, setEnabled: { try remote.setDirectEnabled(machine, $0) })
    }

    var supported: Bool { view.kind != .unsupported }
    /// The switch: on whenever the user has not turned direct off for this computer.
    var on: Bool { supported && view.kind != .off }

    /// Reads the status again; a path just turned on says "trying" until it reports something.
    func reload() {
        let s = status()
        var next = DirectView.make(s)
        if let at = turnedOn {
            if now().timeIntervalSince(at) > Self.tryingSeconds { turnedOn = nil }
            else if s.state == .none && (s.reason == nil || s.reason == .starting || s.reason == .securityPending) { next = DirectView.trying() }
        }
        if next != view { view = next }
    }

    func set(_ on: Bool) {
        do {
            try setEnabled(on)
            error = ""
            turnedOn = on ? now() : nil
        } catch {
            self.error = errorText(error)
        }
        reload()
    }

    /// The pending "trying" ends by itself even when nothing else changes.
    func expireTrying() {
        guard turnedOn != nil else { return }
        reload()
    }
}

/// The 「连接」group, after 账户额度 and before CLI 版本: a switch row, the state row (only with the
/// switch on and the computer online), a row for each measured number, and one sentence under it.
struct ConnectionGroup: View {
    let remote: Remote
    let machine: String
    let online: Bool
    @State private var model: DirectConnectionModel?
    @State private var showDiag = false
    @Environment(\.accessibilityReduceMotion) private var reduceMotion

    var body: some View {
        Group {
            if remote.directSupported {
                content(model ?? DirectConnectionModel(remote: remote, machine: machine))
            }
        }
        .task(id: machine) {
            guard remote.directSupported else { return }
            let made = DirectConnectionModel(remote: remote, machine: machine)
            model = made
            remote.refreshDirect(machine)
            made.reload()
            for await _ in remote.directChanges.updates() {
                if Task.isCancelled { return }
                made.reload()
            }
        }
        .task(id: model?.view.kind) {
            // A path just turned on may say "trying" for a bounded time.
            guard let model, model.view.kind == .trying else { return }
            try? await Task.sleep(nanoseconds: UInt64((DirectConnectionModel.tryingSeconds + 0.5) * 1_000_000_000))
            model.expireTrying()
        }
    }

    @ViewBuilder private func content(_ model: DirectConnectionModel) -> some View {
        let view = model.view
        if !model.error.isEmpty { GroupBanner(model.error, tone: .error) }
        GroupLabel(L10n.transportGroup())
        GroupCard {
            ToggleRow(L10n.transportDirect(), isOn: Binding(get: { model.on }, set: { model.set($0) }))
            if model.on && online { DirectStatusRow(view: view) }
            if model.on {
                for s in view.speeds { GRow(s.name, value: s.value) }
            }
            if model.on && online && view.showDiagnosticsEntry {
                GRow(L10n.transportDiag(), chevron: true) { showDiag = true }
            }
        }
        .animation(reduceMotion ? nil : .easeInOut(duration: 0.2), value: view)
        GroupNote(L10n.transportFooter())
        .sheet(isPresented: $showDiag) {
            DirectDiagnosticsSheet(
                verdict: view.sub ?? L10n.transportDiagVerdictNone(),
                canRetry: view.kind != .direct && view.kind != .trying,
                onRetry: { remote.refreshDirect(machine) },
                onClose: { showDiag = false }
            )
        }
    }
}

private struct DirectDiagnosticsSheet: View {
    let verdict: String
    let canRetry: Bool
    let onRetry: () -> Void
    let onClose: () -> Void
    @State private var copied = false

    var body: some View {
        NavigationStack {
            SettingsPage {
                GroupCard {
                    GRow(L10n.transportDiag(), sub: verdict)
                    GRow(copied ? L10n.commonCopied() : L10n.transportDiagCopy(), action: true) {
                        UIPasteboard.general.string = verdict
                        copied = true
                    }
                    GRow(L10n.transportDiagRetry(), action: true, enabled: canRetry) {
                        onRetry()
                        onClose()
                    }
                }
                GroupNote(L10n.transportDiagFooter())
            }
            .navBar(L10n.transportDiag())
            .toolbar {
                ToolbarItem(placement: .confirmationAction) {
                    Button(L10n.commonDone(), action: onClose)
                }
            }
        }
    }
}

/// The state row: the graphic, the state word and under it what is added, all `ink2`.
struct DirectStatusRow: View {
    let view: DirectView

    var body: some View {
        HStack(spacing: 10) {
            mark
            VStack(alignment: .leading, spacing: 1) {
                Text(view.word).font(.body).ink(DS.ink2)
                if let sub = view.sub, !sub.isEmpty { Text(sub).font(.subheadline).ink(DS.ink2) }
            }
            .frame(maxWidth: .infinity, alignment: .leading)
        }
        .padding(.horizontal, 14).padding(.vertical, 11)
        .frame(minHeight: DS.minHit)
        .accessibilityElement(children: .combine)
    }

    /// Green dot, turning ink ring, or light grey dot; never a hollow ring.
    @ViewBuilder private var mark: some View {
        switch view.mark {
        case .online: Dot(DS.green)
        case .run: Mark(Mark.progress, color: DS.ink, size: 13, relativeTo: .body)
        case .off: Dot(DS.faint)
        }
    }
}
