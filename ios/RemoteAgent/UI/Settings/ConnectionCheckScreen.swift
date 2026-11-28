import Observation
import RACore
import RAProto
import SwiftUI

/// 连接检测 (client-v66 「连接检测」): how long a round trip takes now on
/// each stretch of the way, the nearest first: this device's network, the
/// server, then each online computer. The server's /healthz is timed here;
/// each computer's total, its network and computer segments, slow marks and
/// summary come from Core's `MachineLatencyView`, which runs `probe_latency`
/// once ConnCheckStart arrives. Measured when opened and on 再测一次; nothing
/// runs in the background (architecture §9 「连接检测」).
struct ConnectionCheckScreen: View {
    @Environment(AppModel.self) private var model
    @Environment(AccountContext.self) private var context
    @State private var check: ConnectionCheckModel?
    /// Bumped by 再测一次.
    @State private var run = 0

    var body: some View {
        let settingsVM = context.core.settings
        // Online first: those are the ones measured.
        let byName = settingsVM.machines
        let machines = byName.filter(\.online) + byName.filter { !$0.online }
        let busy = (check?.busy ?? true) || settingsVM.connCheck.machinesBusy
        SettingsPage {
            GroupCard {
                let network = settingsVM.connCheck.networkAvailable
                GRow(L10n.connectionLocalNetwork(), value: network ? L10n.connectionWorking() : L10n.connectionUnavailable()).valueLead { Dot(network ? DS.green : DS.ink3) }
                serverRow(check?.server ?? .wait)
                for m in machines {
                    let tag = m.disambiguator.isEmpty ? nil : m.disambiguator
                    let tagA11y = m.hasDisambiguatorA11Y ? m.disambiguatorA11Y.rendered() : nil
                    if m.online, m.member || model.isDemo {
                        if m.hasLatency {
                            ConnCheckMachineRow(card: m, tag: tag, tagA11y: tagA11y)
                        } else {
                            GRow(m.name, tag: tag, tagA11y: tagA11y, value: m.statusText.rendered().or(L10n.connectionNotChecked()))
                        }
                    } else if !m.member {
                        // Whether it is there is not known from here; the server's row says why.
                        GRow(m.name, tag: tag, tagA11y: tagA11y, value: L10n.connectionNotChecked())
                    } else {
                        GRow(m.name, tag: tag, tagA11y: tagA11y, value: L10n.commonIosOffline()).valueLead { Dot(DS.ink3) }
                    }
                }
            }
            .padding(.top, 4)
            let foot = settingsVM.connCheck.footerText.rendered().or(L10n.connectionLatencyIsOneRoundTripTheServerRowMeasures())
            GroupNote(foot)
            Btn(L10n.connectionTestAgain(), enabled: !busy, background: DS.gbg) { run += 1 }
                .padding(.horizontal, 16)
        }
        .navBar(L10n.homeConnectionCheck())
        .debugSignal("check.again") { run += 1 }
        .task(id: run) {
            await context.coreHost.openSettings()
            let made = if let check { check } else { ConnectionCheckModel(probes: await probes()) }
            check = made
            // Core resets the server verdict on Start; this round's verdict must come after it.
            await context.coreHost.dispatch(PBCoreEvent.with { $0.connCheckStart = PBConnCheckStartEvent() })
            await made.measure()
            guard !Task.isCancelled else { return }
            reportProbeStatus(made.server)
        }
        .onDisappear {
            context.dispatch { $0.connCheckStop = PBConnCheckStopEvent() }
        }
    }

    private func reportProbeStatus(_ ping: Ping) {
        context.dispatch { ev in
            switch ping {
            case .wait:
                ev.connCheckStatus.serverState = .checking
            case .ok(let ms):
                ev.connCheckStatus.serverState = ms >= slowServerMs ? .slow : .working
                ev.connCheckStatus.latencyMs = Int32(ms)
            case .fail(let err):
                ev.connCheckStatus.serverState = .unavailable
                ev.connCheckStatus.lastFailure = err
            }
        }
    }

    /// The server's row: the middle of three /healthz round trips.
    private func serverRow(_ ping: Ping) -> GRow {
        let title = L10n.securityServer()
        let sub = hostOf(context.account.server)
        switch ping {
        case .wait:
            return GRow(title, sub: sub, value: L10n.connectionChecking())
                .valueLead { Mark(Mark.progress, color: DS.ink2, size: 13, relativeTo: .footnote) }
        case .fail(let error):
            return GRow(title, sub: sub, value: error, valueColor: DS.red)
        case .ok(let ms):
            let late = ms >= slowServerMs
            let shown = formatLatencyMs(ms)
            return GRow(title, sub: sub, value: late ? L10n.connectionLatencyMsSlow(ms: shown) : L10n.connectionLatencyMs(ms: shown), valueColor: DS.ink2)
                // Slow asks nothing of you: a light grey dot, not orange (design-system §2).
                .valueLead { Dot(late ? DS.faint : DS.green) }
        }
    }

    private func probes() async -> ConnectionProbes {
        #if DEBUG
        // With the sample connection down (`-demoLink`), the sample server does not answer either.
        if model.isDemo {
            let link = await context.coreHost.currentDemoWorld().connection
            return .demo(link == .connected ? SettingsDemo.state : "serverDown")
        }
        #endif
        let remote = context.remote
        return ConnectionProbes(server: { try await remote.pingServer() })
    }
}

/// Thousands grouped the same way in every language, as Core writes a computer's total.
func formatLatencyMs(_ ms: Int64) -> String {
    max(0, ms).formatted(.number.grouping(.automatic).locale(Locale(identifier: "en_US")))
}

/// One online computer in 连接检测, drawn from Core's `MachineLatencyView`:
/// name, the route summary under it and the total at its end, then the
/// network and computer segments (and waiting for direct, after a reroute)
/// as two aligned columns, a slow or stuck one in bold `ink`.
private struct ConnCheckMachineRow: View {
    let card: PBMachineListCardView
    let tag: String?
    let tagA11y: String?

    var body: some View {
        let lat = card.latency
        let a11y = lat.a11YText.rendered()
        VStack(alignment: .leading, spacing: 0) {
            head(lat)
            if !lat.segments.isEmpty || !lat.noteText.rendered().isEmpty {
                VStack(alignment: .leading, spacing: 2) {
                    ForEach(Array(lat.segments.enumerated()), id: \.offset) { _, seg in
                        segment(seg)
                    }
                    let note = lat.noteText.rendered()
                    if !note.isEmpty {
                        Text(note).font(.footnote).ink(DS.ink2)
                            .fixedSize(horizontal: false, vertical: true)
                            .padding(.top, lat.segments.isEmpty ? 0 : 2)
                    }
                }
                .padding(.horizontal, 14)
                .padding(.top, -4)
                .padding(.bottom, 11)
            }
        }
        .accessibilityElement(children: a11y.isEmpty ? .combine : .ignore)
        .modifier(OptionalA11yLabel(text: a11y))
    }

    private func head(_ lat: PBMachineLatencyView) -> GRow {
        let summary: String = {
            let override = lat.summaryOverrideText.rendered()
            if !override.isEmpty { return override }
            guard card.hasDirectRow, card.directRow.visible else { return "" }
            return [card.directRow.statusText.rendered(), card.directRow.detailText.rendered()]
                .filter { !$0.isEmpty }.joined(separator: " · ")
        }()
        let sub = summary.isEmpty ? nil : summary
        let value = lat.valueText.rendered()
        switch lat.state {
        case .measuring:
            return GRow(card.name, tag: tag, tagA11y: tagA11y, sub: sub, value: value.or(L10n.connectionChecking()))
                .valueLead { Mark(Mark.progress, color: DS.ink2, size: 13, relativeTo: .footnote) }
        case .done:
            return GRow(card.name, tag: tag, tagA11y: tagA11y, sub: sub, value: value, valueColor: DS.ink2)
                // Slow asks nothing of you: a light grey dot, not orange (design-system §2).
                .valueLead { Dot(lat.slow ? DS.faint : DS.green) }
        case .failed:
            return GRow(card.name, tag: tag, tagA11y: tagA11y, sub: sub, value: value, valueColor: DS.red)
        default:
            return GRow(card.name, tag: tag, tagA11y: tagA11y, sub: sub, value: value.or(L10n.connectionNotChecked()))
        }
    }

    private func segment(_ seg: PBMachineLatencySegment) -> some View {
        let strong = seg.slow || seg.stuck
        let label = seg.labelText.rendered()
        let slowTag = seg.slow ? seg.slowLabelText.rendered() : ""
        let left = slowTag.isEmpty ? label : label + " · " + slowTag
        let detail = seg.detailText.rendered()
        return VStack(alignment: .leading, spacing: 1) {
            HStack(alignment: .firstTextBaseline, spacing: 12) {
                Text(left).frame(maxWidth: .infinity, alignment: .leading)
                Text(seg.valueText.rendered()).monospacedDigit().lineLimit(1).fixedSize()
            }
            .font(.footnote.weight(strong ? .semibold : .regular))
            .ink(strong ? DS.ink : DS.ink2)
            if !detail.isEmpty {
                Text(detail).font(.caption).ink(DS.ink2).fixedSize(horizontal: false, vertical: true)
            }
        }
    }
}

private struct OptionalA11yLabel: ViewModifier {
    let text: String
    func body(content: Content) -> some View {
        if text.isEmpty { content } else { content.accessibilityLabel(text) }
    }
}

/// The server's measurement, so that its order and rules are tested without a server.
struct ConnectionProbes: Sendable {
    /// One round trip to the server, in ms.
    var server: @Sendable () async throws -> Int64

    #if DEBUG
    /// Fixed answers for looking at the page with `-demo`.
    static func demo(_ state: String?) -> ConnectionProbes {
        ConnectionProbes(server: {
            if state == "checking" { try await Task.sleep(nanoseconds: 3_600_000_000_000) }
            if state == "serverDown" { throw UserError(L10nMessage.connectionNoResponse()) }
            return 86
        })
    }
    #endif
}

/// One run of the server's row: the middle one of three /healthz requests.
/// The computers are measured by Core after ConnCheckStart.
@MainActor
@Observable
final class ConnectionCheckModel {
    private(set) var server = Ping.wait
    private(set) var busy = false

    @ObservationIgnored private let probes: ConnectionProbes
    /// Bumped by every run: a run replaced by a later one writes nothing more.
    @ObservationIgnored private var run = 0

    init(probes: ConnectionProbes) {
        self.probes = probes
    }

    /// Measures the server. Ends early, writing nothing, when its task is cancelled.
    func measure() async {
        run += 1
        let mine = run
        busy = true
        server = .wait
        defer { if mine == run { busy = false } }
        do {
            var samples: [Int64] = []
            for _ in 0..<3 {
                samples.append(try await probes.server())
                guard mine == run, !Task.isCancelled else { return }
            }
            server = .ok(ms: median(samples))
        } catch is CancellationError {
            return
        } catch {
            guard mine == run, !Task.isCancelled else { return }
            server = .fail(error: errorText(error).or(L10n.connectionNoResponse()))
        }
    }
}
