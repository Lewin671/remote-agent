import Foundation
import os

/// A shared job resolves once. Canceling a waiter only unregisters that waiter;
/// the job and its owner slot live until settlement or explicit owner retirement.
final class SharedResult<Value: Sendable>: Sendable {
    private typealias Continuation = AsyncThrowingStream<Value, Error>.Continuation
    private struct State: Sendable {
        var result: Result<Value, Error>?
        var waiters: [UUID: Continuation] = [:]
    }
    private let state = OSAllocatedUnfairLock(initialState: State())
    private func send(_ result: Result<Value, Error>, to waiter: Continuation) {
        switch result {
        case .success(let value): waiter.yield(value); waiter.finish()
        case .failure(let error): waiter.finish(throwing: error)
        }
    }
    func resolve(_ result: Result<Value, Error>) {
        let waiters = state.withLock { s -> [Continuation] in
            guard s.result == nil else { return [] }
            s.result = result
            defer { s.waiters.removeAll() }
            return Array(s.waiters.values)
        }
        for waiter in waiters { send(result, to: waiter) }
    }
    var value: Value {
        get async throws {
            try Task.checkCancellation()
            let id = UUID()
            let (stream, waiter) = AsyncThrowingStream<Value, Error>.makeStream(bufferingPolicy: .bufferingNewest(1))
            let result = state.withLock { s in
                if s.result == nil { s.waiters[id] = waiter }
                return s.result
            }
            if let result { send(result, to: waiter) }
            defer { state.withLock { _ = $0.waiters.removeValue(forKey: id) }; waiter.finish() }
            return try await withTaskCancellationHandler {
                var iterator = stream.makeAsyncIterator()
                guard let value = try await iterator.next() else { throw CancellationError() }
                try Task.checkCancellation()
                return value
            } onCancel: { waiter.finish(throwing: CancellationError()) }
        }
    }
}
