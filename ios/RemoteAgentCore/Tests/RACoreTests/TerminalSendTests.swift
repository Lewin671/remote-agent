import Foundation
import RAProto
import Testing
import os

@testable import RACore

private final class DelayedTerminalSocket: TerminalSocket, Sendable {
    let writes = Mailbox<Data>()
    let cancellations = Counter()
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
    func cancel() { cancellations.increment() }
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
        #expect(try #require(await socket.writes.poll(1000)).count == 99)
        // Only the encoded write can fill this budget; sealing has already returned.
        conn.send(input)
        #expect(closed.all.count == 1)
        try await eventually("terminal cancelled") { socket.cancellations.value == 1 }
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
        _ = try #require(await socket.writes.poll(1000))
        conn.send(input)
        #expect(sealed.all == [1])
        socket.complete()
        #expect(try #require(await socket.writes.poll(1000)) == Data([2]))
        conn.send(input)
        socket.complete(URLError(.networkConnectionLost))
        _ = try #require(await closed.poll(1000))
        try await eventually("terminal cancelled") { socket.cancellations.value == 1 }
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
        _ = try #require(await closed.poll(1000))
        #expect(socket.writes.all.isEmpty)
        try await eventually("terminal cancelled") { socket.cancellations.value == 1 }
    }
}
