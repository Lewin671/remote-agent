import RACore
import RAProto
import SwiftUI

/// The 「连接」group rendered directly from Core's `PBConnectionGroupView`.
struct ConnectionGroup: View {
    let view: PBConnectionGroupView
    let onToggle: (Bool) -> Void
    let onRetryDirect: () -> Void
    @State private var showDiag = false
    @Environment(\.accessibilityReduceMotion) private var reduceMotion

    var body: some View {
        let supported = view.toggleVisible
        let status = view.statusText.rendered()
        let detail = view.detailText.rendered()
        let metric = view.metricText.rendered()
        let fileSpeed = view.fileSpeedText.rendered()
        let readLatency = view.readLatencyText.rendered()
        let footer = view.footerText.rendered().or(L10n.transportFooter())
        let showStatus = !status.isEmpty || !detail.isEmpty || !metric.isEmpty
        let showSpeeds = view.stage != .disabled && view.stage != .unsupported && view.stage != .offline

        GroupLabel(L10n.transportGroup())
        GroupCard {
            ToggleRow(
                L10n.transportDirect(),
                isOn: Binding(
                    get: { supported && view.toggleChecked },
                    set: { onToggle($0) }
                )
            )
            .disabled(!view.toggleEnabled)
            .opacity(supported ? 1 : 0.4)
            if showStatus {
                DirectStatusRow(stage: view.stage, tone: view.tone, status: status, detail: detail, metric: metric)
            }
            if showSpeeds && !fileSpeed.isEmpty {
                GRow(L10n.transportSpeedFile(), value: fileSpeed)
            }
            if showSpeeds && !readLatency.isEmpty {
                GRow(L10n.transportSpeedRead(), value: readLatency)
            }
            if view.showDiagnosticsEntry {
                GRow(L10n.transportDiag(), chevron: true) { showDiag = true }
            }
        }
        .animation(reduceMotion ? nil : .easeInOut(duration: 0.2), value: view)
        GroupNote(footer)
            .sheet(isPresented: $showDiag) {
                DirectDiagnosticsSheet(
                    verdict: detail.or(status).or(L10n.transportDiagVerdictNone()),
                    canRetry: view.stage != .direct && view.stage != .connecting && view.stage != .measuring,
                    onRetry: onRetryDirect,
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
    let stage: PBConnectionGroupStage
    let tone: PBConnectionGroupTone
    let status: String
    let detail: String
    var metric: String = ""

    var body: some View {
        let sub = [detail, metric].filter { !$0.isEmpty }.joined(separator: " · ")
        HStack(spacing: 10) {
            mark
            VStack(alignment: .leading, spacing: 1) {
                Text(status).font(.body).ink(DS.ink2)
                if !sub.isEmpty { Text(sub).font(.subheadline).ink(DS.ink2) }
            }
            .frame(maxWidth: .infinity, alignment: .leading)
        }
        .padding(.horizontal, 14).padding(.vertical, 11)
        .frame(minHeight: DS.minHit)
        .accessibilityElement(children: .combine)
    }

    @ViewBuilder private var mark: some View {
        switch stage {
        case .direct:
            Dot(DS.green)
        case .connecting, .measuring:
            Mark(Mark.progress, color: DS.ink, size: 13, relativeTo: .body)
        default:
            Dot(tone == .warn ? DS.orange : DS.faint)
        }
    }
}
