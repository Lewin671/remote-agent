import Foundation
@testable import RACore

/// A one-shot gate for holding async work until the test releases it.
actor TestLatch {
    private var released = false
    private var waiting: [CheckedContinuation<Void, Never>] = []

    func wait() async {
        if released { return }
        await withCheckedContinuation { waiting.append($0) }
    }

    func open() {
        released = true
        for waiter in waiting { waiter.resume() }
        waiting = []
    }
}
