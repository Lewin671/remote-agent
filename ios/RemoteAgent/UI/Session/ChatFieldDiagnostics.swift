import Foundation
import RACore

/// No observable state or view-layout inputs. Only the foreground lifecycle and
/// public run-loop callbacks control the background monitor.
@MainActor
final class ChatFieldDiagnostics {
    static let shared = ChatFieldDiagnostics()
    private let ring = ChatDiagnosticRing.shared
    private let monitor = ChatStallMonitor()
    private var beginObserver: CFRunLoopObserver?
    private var endObserver: CFRunLoopObserver?

    func foreground(_ active: Bool) {
        if beginObserver == nil {
            let ring = ring
            beginObserver = CFRunLoopObserverCreateWithHandler(nil,
                CFRunLoopActivity.entry.rawValue | CFRunLoopActivity.afterWaiting.rawValue, true, 0) { _, _ in
                    ring.turn(begin: true)
                }
            endObserver = CFRunLoopObserverCreateWithHandler(nil, CFRunLoopActivity.beforeWaiting.rawValue,
                true, CFIndex.max) { _, _ in ring.turn(begin: false) }
            CFRunLoopAddObserver(CFRunLoopGetMain(), beginObserver, .commonModes)
            CFRunLoopAddObserver(CFRunLoopGetMain(), endObserver, .commonModes)
        }
        monitor.foreground(active, at: DispatchTime.now().uptimeNanoseconds)
    }

}

/// All mutable monitor/recorder state belongs to this serial queue, never
/// the main actor. A single outstanding ping prevents a blocked-main backlog.
private final class ChatStallMonitor: @unchecked Sendable {
    private let queue = DispatchQueue(label: "chat-stall-diagnostics", qos: .utility)
    private var active = false
    private var timer: DispatchSourceTimer?
    private var recorder: ChatStallRecorder

    init() {
        let files = ChatStallFiles()
        recorder = ChatStallRecorder(ring: .shared) { report in try? files.save(report) }
    }
    func foreground(_ value: Bool, at time: UInt64) {
        queue.async { [self] in
            active = value
            if !value { recorder.suspend(at: time) }
            if timer == nil && value {
                let timer = DispatchSource.makeTimerSource(queue: queue)
                timer.schedule(deadline: .now(), repeating: .milliseconds(500))
                timer.setEventHandler { @Sendable [weak self] in self?.poll() }
                self.timer = timer
                timer.resume()
            }
        }
    }
    private func poll() {
        guard active else { return }
        let now = DispatchTime.now().uptimeNanoseconds
        let wall = UInt64(max(0, Date().timeIntervalSince1970 * 1000))
        guard let id = recorder.poll(at: now, unixMilliseconds: wall) else { return }
        DispatchQueue.main.async { [weak self] in
            let responded = DispatchTime.now().uptimeNanoseconds
            guard let self else { return }
            self.queue.async { [self] in recorder.acknowledge(id, at: responded) }
        }
    }
}
