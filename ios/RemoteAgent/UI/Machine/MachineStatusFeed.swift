// What a screen reads of a computer's status: RACore's `MachineStatusController` (the polling and its state
// machine) as an observable value, and the modifier that keeps it running only while the page is on screen
// and the app in front (ui-spec A1, A4). The page reads `state`, `last`, `curveSinceMs`; what it draws comes from
// RACore's MachineStatusModel.
import Observation
import RACore
import RAProto
import SwiftUI

typealias MachineStatusFetch = @Sendable () async throws -> PBMachineStatus

@MainActor
@Observable
final class MachineStatusFeed {
    private(set) var state: MachinePollState = .idle
    private(set) var last: PBMachineStatus?
    private(set) var curveSinceMs: Int64 = 0
    /// When `last` arrived on this phone, for 「{N 秒/分钟}前的数据」.
    private(set) var receivedAtMs: Int64 = 0
    @ObservationIgnored private var controller: MachineStatusController?
    @ObservationIgnored private let interval: Duration

    init(interval: Duration = .seconds(2)) { self.interval = interval }

    var view: StatusView { statusView(state: state, last: last) }

    /// Starts or stops the polling for what the page is now: visible, the computer online, its daemon able to say.
    func update(fetch: @escaping MachineStatusFetch, visible: Bool, online: Bool, supported: Bool) {
        if controller == nil {
            controller = MachineStatusController(interval: interval, fetch: fetch, changed: { [weak self] in self?.sync() })
        }
        controller?.setActive(visible: visible, online: online, supported: supported)
    }

    /// Asks again at once (the retry of 读不到这台电脑的状态).
    func retry() { controller?.retry() }

    func stop() { controller?.dispose() }

    private func sync() {
        guard let c = controller else { return }
        if c.last?.observedMs != last?.observedMs || (c.last == nil) != (last == nil) { receivedAtMs = nowMs() }
        last = c.last
        curveSinceMs = c.curveSinceMs
        state = c.state
    }
}

/// Keeps a feed polling for as long as the view is on screen and the app in front.
private struct StatusPolling: ViewModifier {
    let feed: MachineStatusFeed
    let fetch: MachineStatusFetch
    let online: Bool
    let supported: Bool
    @Environment(AppModel.self) private var model
    @State private var appeared = false

    private struct Inputs: Equatable { var appeared: Bool; var foreground: Bool; var online: Bool; var supported: Bool }

    func body(content: Content) -> some View {
        let inputs = Inputs(appeared: appeared, foreground: model.foreground, online: online, supported: supported)
        content
            .onAppear { appeared = true }
            .onDisappear { appeared = false }
            .onChange(of: inputs, initial: true) { _, now in
                feed.update(fetch: fetch, visible: now.appeared && now.foreground, online: now.online, supported: now.supported)
            }
            .onDisappear { feed.stop() }
    }
}

extension View {
    /// Polls `feed` while this view is showing (`MachineStatusFeed.update`).
    func machineStatusPolling(_ feed: MachineStatusFeed, fetch: @escaping MachineStatusFetch, online: Bool, supported: Bool) -> some View {
        modifier(StatusPolling(feed: feed, fetch: fetch, online: online, supported: supported))
    }
}

extension AccountContext {
    /// How this account reads a computer's status; sample data answers from a fixture (debug builds).
    @MainActor func machineStatusFetch(_ id: String, demo: Bool) -> MachineStatusFetch {
        #if DEBUG
        if demo { return MachineStatusDemo.fetch(machine: id) }
        #endif
        let remote = self.remote
        return { try await remote.getMachineStatus(id) }
    }
}
