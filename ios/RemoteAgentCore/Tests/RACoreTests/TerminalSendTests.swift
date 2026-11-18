import Foundation
import RAProto
import Testing
import os

@testable import RACore

private final class DelayedTerminalSocket: TerminalSocket, Sendable {
    let writes = Mailbox<Data>()
    let cancellations = Counter()
    let cancelled = Signal()
    private let completion = OSAllocatedUnfairLock<(@Sendable (Error?) -> Void)?>(initialState: nil)

    func send(_ data: Data, done: @escaping @Sendable (Error?) -> Void) {
        completion.withLock { $0 = done }
        writes.put(data)
    }
    func complete(_ error: Error? = nil) {
        let done = completion.withLock { c in
            defer { c = nil }
            return c
        }
        done?(error)
    }
    func cancel() { cancellations.increment(); cancelled.set() }
    func close() {}
}

@Suite struct TerminalSendTests {
    private var input: PBTerminalFrame { .with { $0.input = Data([1]) } }

    @Test func delayedSendKeepsEncodedBytesReserved() async throws {
        let socket = DelayedTerminalSocket()
        let closed = Mailbox<String>()
        let conn = TermConn(socket: socket, sendBudget: 100,
            seal: { _, _, _ in Data(repeating: 0, count: 99) },
            finish: { closed.put($0) }, closeLocal: {})
        conn.send(input)
        #expect(try await socket.writes.take().count == 99)
        // Only the encoded write can fill this budget; sealing has already returned.
        conn.send(input)
        #expect(closed.all.count == 1)
        #expect(await socket.cancelled.wait())
        #expect(socket.cancellations.value == 1)
        socket.complete()
    }

    @Test func completionAdvancesSenderAndErrorStopsLaterWrites() async throws {
        let socket = DelayedTerminalSocket()
        let closed = Mailbox<String>()
        let sealed = Mailbox<UInt64>()
        let conn = TermConn(socket: socket,
            seal: { _, seq, _ in sealed.put(seq); return Data([UInt8(seq)]) },
            finish: { closed.put($0) }, closeLocal: {})
        conn.send(input)
        _ = try await socket.writes.take()
        conn.send(input)
        #expect(sealed.all == [1])
        socket.complete()
        #expect(try await socket.writes.take() == Data([2]))
        conn.send(input)
        socket.complete(URLError(.networkConnectionLost))
        _ = try await closed.take()
        #expect(await socket.cancelled.wait())
        #expect(socket.cancellations.value == 1)
        #expect(sealed.all == [1, 2])
        conn.send(input)
        #expect(socket.writes.all.isEmpty)
    }

    @Test func encodedFrameOverBudgetEndsWithoutWriting() async throws {
        let socket = DelayedTerminalSocket()
        let closed = Mailbox<String>()
        let conn = TermConn(socket: socket, sendBudget: 100,
            seal: { _, _, _ in Data(repeating: 0, count: 101) },
            finish: { closed.put($0) }, closeLocal: {})
        conn.send(input)
        _ = try await closed.take()
        #expect(socket.writes.all.isEmpty)
        #expect(await socket.cancelled.wait())
        #expect(socket.cancellations.value == 1)
    }
}
