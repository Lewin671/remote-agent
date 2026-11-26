import Foundation
import RAProto
import Security

public struct SecurityWorkflowError: Error, LocalizedError, Equatable, Sendable {
    public let message: String
    public let code: PBSecurityErrorCode
    public let retryable: Bool
    public let uncertain: Bool
    public let errorAction: String

    public init(
        message: String,
        code: PBSecurityErrorCode = .unspecified,
        retryable: Bool = true,
        uncertain: Bool = false,
        errorAction: String = ""
    ) {
        self.message = message
        self.code = code
        self.retryable = retryable
        self.uncertain = uncertain
        self.errorAction = errorAction
    }

    public var errorDescription: String? { message }
}

public func translateSecurityErrorMessage(_ res: PBSecurityWorkflowResultEffect) -> String {
    if res.hasErrorText {
        let rendered = renderCoreText(res.errorText)
        if !rendered.isEmpty { return rendered }
    }
    return res.structuredErrorCode != .unspecified ? String(describing: res.structuredErrorCode) : ""
}

/// Lock-protected one-shot bridge between a `CheckedContinuation` and async completion or task cancellation.
final class ContinuationWaiter<T: Sendable>: @unchecked Sendable {
    private let lock = NSLock()
    private var continuation: CheckedContinuation<T, Error>?
    private var earlyResult: Result<T, Error>?

    func install(_ cont: CheckedContinuation<T, Error>) -> Bool {
        let immediate: Result<T, Error>? = lock.withLock {
            if let early = earlyResult {
                earlyResult = nil
                return early
            }
            continuation = cont
            return nil
        }
        if let immediate {
            cont.resume(with: immediate)
            return false
        }
        return true
    }

    func resolve(_ result: Result<T, Error>) {
        let cont: CheckedContinuation<T, Error>? = lock.withLock {
            if let c = continuation {
                continuation = nil
                return c
            }
            if earlyResult == nil {
                earlyResult = result
            }
            return nil
        }
        cont?.resume(with: result)
    }
}

func awaitWaiter<R: Sendable>(
    isClosed: @escaping @Sendable () -> Bool,
    closedError: @escaping @Sendable () -> Error,
    register: (ContinuationWaiter<R>) throws -> UInt64,
    unregister: @escaping @Sendable (UInt64) -> ContinuationWaiter<R>?,
    dispatch: (UInt64) -> Void
) async throws -> R {
    let waiter = ContinuationWaiter<R>()
    let id = try register(waiter)
    return try await withTaskCancellationHandler {
        try await withCheckedThrowingContinuation { continuation in
            guard waiter.install(continuation) else { return }
            if isClosed() {
                unregister(id)?.resolve(.failure(closedError()))
                return
            }
            dispatch(id)
            if isClosed() {
                unregister(id)?.resolve(.failure(closedError()))
            }
        }
    } onCancel: {
        unregister(id)?.resolve(.failure(CancellationError()))
    }
}

/// Async promise/continuation wrapper over `SecurityWorkflowActionEvent` / `SecurityWorkflowResultEffect`
/// for the remaining device/account management mutations driven through `Remote`.
public final class SecurityClient: @unchecked Sendable {
    private let lock = NSLock()
    private let dispatchEvent: @Sendable (PBCoreEvent) -> Void
    private let setForegroundFn: @Sendable (Bool) -> Void
    private let isClosedFn: @Sendable () -> Bool
    private var nextActionID: UInt64 = 1
    private var waiters: [UInt64: ContinuationWaiter<PBSecurityWorkflowResultEffect>] = [:]
    private var statusBox: PBSecurityWorkflowStatus?

    public init(
        dispatchEvent: @escaping @Sendable (PBCoreEvent) -> Void,
        setForegroundFn: @escaping @Sendable (Bool) -> Void,
        isClosedFn: @escaping @Sendable () -> Bool
    ) {
        self.dispatchEvent = dispatchEvent
        self.setForegroundFn = setForegroundFn
        self.isClosedFn = isClosedFn
    }

    public var currentStatus: PBSecurityWorkflowStatus? {
        lock.withLock { statusBox }
    }

    public func handleWorkflowResult(_ res: PBSecurityWorkflowResultEffect) {
        let waiter = lock.withLock { () -> ContinuationWaiter<PBSecurityWorkflowResultEffect>? in
            if res.hasStatus { statusBox = res.status }
            return waiters.removeValue(forKey: res.actionID)
        }
        guard let waiter else { return }
        if !res.ok {
            let msg = translateSecurityErrorMessage(res)
            waiter.resolve(.failure(SecurityWorkflowError(
                message: msg,
                code: res.structuredErrorCode,
                retryable: res.retryable,
                uncertain: res.uncertain,
                errorAction: res.errorAction
            )))
        } else {
            waiter.resolve(.success(res))
        }
    }

    public func handleStatusChanged(_ status: PBSecurityWorkflowStatus) {
        lock.withLock { statusBox = status }
    }

    public func rejectAll(reason: String = "security client closed") {
        let pending = lock.withLock { () -> [ContinuationWaiter<PBSecurityWorkflowResultEffect>] in
            let all = Array(waiters.values)
            waiters.removeAll()
            return all
        }
        let err = SecurityWorkflowError(message: reason, code: .canceled, retryable: false)
        for waiter in pending { waiter.resolve(.failure(err)) }
    }

    public func setForeground(_ active: Bool) {
        setForegroundFn(active)
    }

    private static let closedErr = SecurityWorkflowError(message: "security client closed", code: .canceled, retryable: false)

    @discardableResult
    public func runAction(_ build: @Sendable (inout PBSecurityWorkflowActionEvent) -> Void) async throws -> PBSecurityWorkflowResultEffect {
        try await awaitWaiter(
            isClosed: isClosedFn,
            closedError: { Self.closedErr },
            register: { waiter in
                try lock.withLock {
                    guard !isClosedFn() else { throw Self.closedErr }
                    try Task.checkCancellation()
                    let id = nextActionID
                    nextActionID &+= 1
                    waiters[id] = waiter
                    return id
                }
            },
            unregister: { [self] id in lock.withLock { waiters.removeValue(forKey: id) } },
            dispatch: { id in
                var action = PBSecurityWorkflowActionEvent()
                action.actionID = id
                build(&action)
                dispatchEvent(PBCoreEvent.with { $0.securityAction = action })
            }
        )
    }

    private func empty(_ set: @Sendable (inout PBSecurityWorkflowActionEvent, PBSecurityActionEmpty) -> Void) async throws -> PBSecurityWorkflowResultEffect {
        try await runAction { set(&$0, PBSecurityActionEmpty()) }
    }

    private func bool(_ flag: Bool, _ set: @Sendable (inout PBSecurityWorkflowActionEvent, PBSecurityBoolAction) -> Void) async throws -> PBSecurityWorkflowResultEffect {
        try await runAction { set(&$0, .with { $0.flag = flag }) }
    }

    private func str(_ value: String, _ set: @Sendable (inout PBSecurityWorkflowActionEvent, PBSecurityStringAction) -> Void) async throws -> PBSecurityWorkflowResultEffect {
        try await runAction { set(&$0, .with { $0.value = value }) }
    }

    @discardableResult
    public func bootstrap() async throws -> PBSecurityWorkflowStatus {
        try await empty { $0.bootstrap = $1 }.status
    }

    public func removeDevice(_ deviceId: String) async throws {
        _ = try await runAction { $0.removeDevice = .with { $0.deviceID = deviceId } }
    }

    public func deleteMachineRecords(_ machineId: String) async throws { _ = try await str(machineId) { $0.deleteMachineRecords = $1 } }

    public func deleteAccount() async throws { _ = try await empty { $0.deleteAccount = $1 } }

    public func updateDeviceMeta(
        deviceId: String = "",
        name: String? = nil,
        platform: String? = nil,
        description: String? = nil,
        describeSelfOnly: Bool = false
    ) async throws {
        _ = try await runAction {
            $0.updateDeviceMeta = .with {
                $0.deviceID = deviceId
                if let name { $0.name = name }
                if let platform { $0.platform = platform }
                if let description { $0.description_p = description }
                $0.describeSelfOnly = describeSelfOnly
            }
        }
    }
}
