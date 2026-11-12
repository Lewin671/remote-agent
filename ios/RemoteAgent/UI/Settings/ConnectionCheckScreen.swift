import Observation
import RACore
import SwiftUI

/// 连接检测 (mockup 「连接检测」, Android's ConnCheckSheet): how long a round
/// trip takes now on each stretch of the way, the nearest first: this
/// device's network, the server, then each computer through the server.
/// Measured when opened and on 再测一次; nothing runs in the background
/// (architecture §9 「连接检测」).
struct ConnectionCheckScreen: View {
    @Environment(AppModel.self) private var model
    @Environment(AccountContext.self) private var context
    @State private var check: ConnectionCheckModel?
    /// Bumped by 再测一次.
    @State private var run = 0

    var body: some View {
        let state = model.state
        // Online first: those are the ones measured.
        let byName = sortedMachines(state)
        let machines = byName.filter(\.online) + byName.filter { !$0.online }
        SettingsPage {
            GroupCard {
                let network = state.link != .noNetwork
                GRow(L10n.connectionLocalNetwork(), value: network ? L10n.connectionWorking() : L10n.connectionUnavailable()).valueLead { Dot(network ? DS.green : DS.ink3) }
                result(L10n.securityServer(), sub: hostOf(context.account.server), check?.server ?? .wait, slow: slowServerMs)
                for m in machines {
                    let name = machineLabel(state, id: m.id)
                    if m.online, !state.unlinked || model.isDemo {
                        result(name, check?.machines[m.id] ?? .wait, slow: slowMachineMs)
                    } else if state.unlinked {
                        // Whether it is there is not known from here; the server's row says why.
                        GRow(name, value: L10n.connectionNotChecked())
                    } else {
                        GRow(name, value: L10n.commonIosOffline()).valueLead { Dot(DS.ink3) }
                    }
                }
            }
            .padding(.top, 4)
            GroupNote(L10n.connectionLatencyIsOneRoundTripTheServerRowMeasures())
            Btn(L10n.connectionTestAgain(), enabled: check?.busy == false, background: DS.gbg) { run += 1 }
                .padding(.horizontal, 16)
        }
        .navBar(L10n.homeConnectionCheck())
        .debugSignal("check.again") { run += 1 }
        .task(id: run) {
            let made = check ?? ConnectionCheckModel(probes: probes)
            check = made
            // The computers the server lists as online now, not as of the last drawing.
            let online = context.remote.current.machines.values.filter(\.online).map(\.id)
            await made.measure(online: model.isDemo ? machines.filter(\.online).map(\.id) : online)
        }
    }

    /// A row with what its stretch measured.
    private func result(_ title: String, sub: String? = nil, _ ping: Ping, slow: Int64) -> GRow {
        switch ping {
        case .wait:
            return GRow(title, sub: sub, value: L10n.connectionChecking())
                .valueLead { Mark(Mark.progress, color: DS.ink2, size: 13, relativeTo: .footnote) }
        case .skip:
            return GRow(title, sub: sub, value: L10n.connectionNotChecked())
        case .fail(let error):
            return GRow(title, sub: sub, value: error, valueColor: DS.red)
        case .ok(let ms):
            let late = ms >= slow
            return GRow(title, sub: sub, value: "\(ms) ms" + (late ? L10n.connectionSlow() : ""), valueColor: DS.ink2)
                // Slow asks nothing of you: a light grey dot, not orange (design-system §2).
                .valueLead { Dot(late ? DS.faint : DS.green) }
        }
    }

    private var probes: ConnectionProbes {
        #if DEBUG
        // With the sample connection down (`-demoLink`), the sample server does not answer either.
        if model.isDemo { return .demo(model.state.link == .online ? SettingsDemo.state : "serverDown") }
        #endif
        let remote = context.remote
        return ConnectionProbes(server: { try await remote.pingServer() }, machine: { try await remote.pingMachine($0) })
    }
}

/// The two measurements, so that the order and the rules are tested without a server.
struct ConnectionProbes: Sendable {
    /// One round trip to the server, in ms.
    var server: @Sendable () async throws -> Int64
    /// One round trip to a computer through the server, in ms.
    var machine: @Sendable (_ id: String) async throws -> Int64

    #if DEBUG
    /// Fixed answers for looking at the page with `-demo`.
    static func demo(_ state: String?) -> ConnectionProbes {
        ConnectionProbes(
            server: {
                if state == "checking" { try await Task.sleep(nanoseconds: 3_600_000_000_000) }
                if state == "serverDown" { throw UserError(L10nMessage.connectionNoResponse()) }
                return 86
            },
            machine: { id in
                if state == "machineDown", id == "vps" { throw CommandError(CommandError.unavailable, L10nMessage.errorsTheComputerIsOfflineTheCommandWasNotSent()) }
                // Told apart by ID so that one of them is slow.
                return id == "vps" ? 2140 : 132
            })
    }
    #endif
}

/// One run of the check: the server first, by the middle one of three
/// requests, then every online computer at once. When the server does not
/// answer the computers are not measured: nothing reaches them but through
/// it.
@MainActor
@Observable
final class ConnectionCheckModel {
    private(set) var server = Ping.wait
    private(set) var machines: [String: Ping] = [:]
    private(set) var busy = false

    @ObservationIgnored private let probes: ConnectionProbes
    /// Bumped by every run: a run replaced by a later one writes nothing more.
    @ObservationIgnored private var run = 0

    init(probes: ConnectionProbes) {
        self.probes = probes
    }

    /// Measures the server and the computers `online`. Ends early, writing nothing, when its task is cancelled.
    func measure(online: [String]) async {
        run += 1
        let mine = run
        busy = true
        server = .wait
        machines = Dictionary(uniqueKeysWithValues: Set(online).map { ($0, Ping.wait) })
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
            for id in machines.keys { machines[id] = .skip }
            return
        }
        let probe = probes.machine
        await withTaskGroup(of: (String, Ping?).self) { group in
            for id in machines.keys {
                group.addTask {
                    do {
                        return (id, .ok(ms: try await probe(id)))
                    } catch is CancellationError {
                        return (id, nil)
                    } catch {
                        return (id, .fail(error: errorText(error).or(L10n.connectionNoResponse())))
                    }
                }
            }
            for await (id, ping) in group {
                guard mine == run, !Task.isCancelled else { continue }
                if let ping { machines[id] = ping }
            }
        }
    }
}
