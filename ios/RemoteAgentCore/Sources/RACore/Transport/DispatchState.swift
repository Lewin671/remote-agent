import Foundation

/// One immutable authorization; active preflights and network deliveries are distinct.
final class DispatchState: @unchecked Sendable {
    private let lock = NSLock()
    private var priorUnknown: Bool
    private var possible = 0
    private var active = 0
    init(prior: Bool) { priorUnknown = prior }
    var unresolved: Bool { lock.withLock { risk(excluding: 0) } }
    var hasPrior: Bool { lock.withLock { priorUnknown || possible > 0 } }
    private func risk(excluding: Int) -> Bool { priorUnknown || possible > 0 || active > excluding }
    func open(prior: Bool) -> Call {
        lock.withLock { priorUnknown = priorUnknown || prior; active += 1; return Call(self) }
    }
    final class Call: @unchecked Sendable {
        private let owner: DispatchState
        private var closed = false
        fileprivate init(_ owner: DispatchState) { self.owner = owner }
        var uncertain: Bool { owner.lock.withLock { owner.risk(excluding: closed ? 0 : 1) } }
        var hasPrior: Bool { owner.hasPrior }
        func delivery() -> Delivery { Delivery(self, owner) }
        func close() { owner.lock.withLock { if !closed { closed = true; owner.active -= 1 } } }
        final class Delivery: @unchecked Sendable {
            private let call: Call
            private let owner: DispatchState
            private var sent = false
            private var settled = false
            fileprivate init(_ call: Call, _ owner: DispatchState) { self.call = call; self.owner = owner }
            func dispatch() throws {
                try owner.lock.withLock {
                    guard !sent && !settled && !call.closed else { throw DeviceSecurity.Failure.invalid }
                    sent = true; owner.possible += 1
                }
            }
            @discardableResult func notForwarded() -> Bool {
                owner.lock.withLock {
                    if !settled { settled = true; if sent { owner.possible -= 1 } }
                    return owner.risk(excluding: call.closed ? 0 : 1)
                }
            }
        }
    }
}
