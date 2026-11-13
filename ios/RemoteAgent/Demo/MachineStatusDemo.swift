// Sample answers for the computer-status screens (debug builds, `-demo`): what a computer would say of itself, in the
// states a screenshot or a script wants. `-machineStatus <normal|tight|memory|loading|failed|stale|few>`; the other
// arguments (`-machineMain`, `-machineList`, `-machineScrub`, `-machineSwap`, `-machineFrozen`) are the page's, listed with
// the others at the head of App/Launch.swift. The samples end at the moment of asking, so a page left open keeps moving.
import Foundation
import RACore
import RAProto

#if DEBUG
@MainActor enum MachineStatusDemo {
    static var mode: String { HomeLaunch.value("-machineStatus") ?? "normal" }

    private final class Calls: @unchecked Sendable {
        private let lock = NSLock()
        private var n = 0
        func next() -> Int { lock.lock(); defer { lock.unlock() }; n += 1; return n }
    }

    private struct Gone: Error {}

    static func fetch(machine: String) -> MachineStatusFetch {
        let mode = mode
        let calls = Calls()
        return {
            let n = calls.next()
            switch mode {
            case "failed": throw Gone()
            case "loading": try await Task.sleep(for: .seconds(3600)); throw Gone()
            case "stale" where n > 2: throw Gone()
            default: return status(mode: mode, now: nowMs())
            }
        }
    }

    /// A curve that wanders, the same for the same moment.
    nonisolated private static func wave(_ k: Int, base: Double, swing: Double) -> Double {
        base + swing * (0.55 * sin(Double(k) / 5.5) + 0.3 * sin(Double(k) / 2.3 + 1) + 0.15 * sin(Double(k) / 1.1))
    }

    nonisolated static func status(mode: String, now: Int64) -> PBMachineStatus {
        let tight = mode == "tight" || mode == "memory"
        let gib = UInt64(1 << 30)
        let count = mode == "few" ? 1 : 60
        var s = PBMachineStatus()
        s.observedMs = now
        s.cpuPercent = tight ? 88 : 23
        s.cores = 10
        s.loadOne = tight ? 11.4 : 3.2
        s.memoryTotalBytes = 16 * gib
        s.memoryUsedBytes = tight ? UInt64(12.6 * Double(gib)) : UInt64(11.2 * Double(gib))
        s.memoryPressure = tight ? .warning : .normal
        s.memoryPressurePosition = tight ? 0.72 : 0.34
        s.swapUsedBytes = tight ? UInt64(4.8 * Double(gib)) : UInt64(1.3 * Double(gib))
        s.disk = .with {
            $0.observedMs = now
            $0.totalBytes = 460_000_000_000
            $0.availableBytes = tight ? 1_200_000_000 : 42_000_000_000
            $0.pressure = tight ? .critical : .normal
            $0.emergencyReserveBytes = 268_435_456
        }
        s.agentBytes = 3_100_000_000
        s.uptimeSeconds = 3 * 86_400 + 4 * 3600 + 900
        s.battery = .with { $0.percent = 81; $0.charging = true }
        s.samples = (0..<count).map { k in
            let back = count - 1 - k
            return .with {
                $0.timeMs = now - Int64(back) * 2000
                $0.cpuPercent = min(98, max(3, wave(k, base: tight ? 70 : 28, swing: tight ? 26 : 16)))
                $0.memoryUsedBytes = UInt64(Double(s.memoryUsedBytes) * (1 + 0.03 * sin(Double(k) / 7)))
            }
        }
        func usage(_ id: String, _ exe: String, session: String = "", cpu: Double, mb: Double) -> PBResourceUsage {
            .with { $0.id = id; $0.executable = exe; $0.sessionID = session; $0.cpuPercent = cpu; $0.memoryBytes = UInt64(mb * Double(1 << 20)) }
        }
        if tight {
            s.sessions = [usage("run", "", cpu: 312, mb: 2150), usage("doc", "", cpu: 188, mb: 1230), usage("s-agy", "", cpu: 8, mb: 640)]
            s.processes = [
                usage("p1", "java", session: "run", cpu: 312, mb: 2150), usage("p2", "xcodebuild", session: "doc", cpu: 188, mb: 1230),
                usage("p3", "Google Chrome", cpu: 41, mb: 1640), usage("p4", "node", session: "s-agy", cpu: 12, mb: 520),
                usage("p5", "WindowServer", cpu: 9, mb: 410), usage("p6", "Slack", cpu: 4, mb: 760),
            ]
        } else {
            s.sessions = [usage("run", "", cpu: 141, mb: 2458), usage("doc", "", cpu: 62, mb: 1126), usage("s-agy", "", cpu: 3, mb: 380)]
            s.processes = [
                usage("p1", "go", session: "run", cpu: 141, mb: 2458), usage("p2", "node", session: "doc", cpu: 62, mb: 1126),
                usage("p3", "Google Chrome", cpu: 21, mb: 1640), usage("p4", "WindowServer", cpu: 9, mb: 410),
                usage("p5", "Slack", cpu: 3, mb: 380),
            ]
        }
        return s
    }
}
#endif
