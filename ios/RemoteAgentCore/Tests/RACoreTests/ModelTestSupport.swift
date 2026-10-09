import Foundation
import RAProto
import SwiftProtobuf

@testable import RACore

/// Builders for the tests ported from Android's core and app modules.
enum Mk {
    static let shanghai = TimeZone(identifier: "Asia/Shanghai")!
    static let utc = TimeZone(identifier: "UTC")!

    /// The epoch milliseconds of a wall-clock time in a zone.
    static func at(_ y: Int, _ mo: Int, _ d: Int, _ h: Int = 12, _ mi: Int = 0, zone: TimeZone = shanghai) -> Int64 {
        var cal = Calendar(identifier: .gregorian)
        cal.timeZone = zone
        let date = cal.date(from: DateComponents(year: y, month: mo, day: d, hour: h, minute: mi))!
        return Int64(date.timeIntervalSince1970 * 1000)
    }

    /// The epoch milliseconds of an ISO-8601 instant.
    static func instant(_ iso: String) -> Int64 {
        Int64(ISO8601DateFormatter().date(from: iso)!.timeIntervalSince1970 * 1000)
    }

    /// A timestamp to the millisecond.
    static func ts(_ ms: Int64) -> Google_Protobuf_Timestamp {
        .with {
            $0.seconds = ms / 1000
            $0.nanos = Int32(ms % 1000 * 1_000_000)
        }
    }

    static func ev(_ seq: UInt64, _ kind: PBEvent.OneOf_Kind, sec: Int64 = 0, origin: PBOrigin? = nil) -> PBEvent {
        .with {
            $0.seq = seq
            $0.kind = kind
            if sec > 0 { $0.time = .with { $0.seconds = sec } }
            if let origin { $0.origin = origin }
        }
    }

    static func session(
        _ id: String = "s", machine: String = "m", updatedMs: Int64 = 0, listedEndedMs: Int64 = 0,
        _ fill: (inout PBSession) -> Void = { _ in }
    ) -> SessionView {
        var info = PBSession()
        fill(&info)
        return SessionView(id: id, machineId: machine, info: info, updatedMs: updatedMs, listedEndedMs: listedEndedMs)
    }

    static func state(
        sessions: [SessionView] = [], machines: [MachineView] = [], prefs: PBProjectPrefs? = nil,
        _ more: (inout RemoteState) -> Void = { _ in }
    ) -> RemoteState {
        var st = RemoteState()
        for s in sessions { st.sessions[s.id] = s }
        for m in machines { st.machines[m.id] = m }
        if let prefs { st.prefs = PrefsObject(revision: 1, prefs: prefs) }
        more(&st)
        return st
    }

    static func inputState(_ id: String, _ status: PBInputStatus, reason: String = "", _ more: (inout PBInputState) -> Void = { _ in }) -> PBEvent.OneOf_Kind {
        .inputState(
            .with {
                $0.requestID = id
                $0.status = status
                $0.reason = reason
                more(&$0)
            })
    }

    static func user(_ text: String) -> PBEvent.OneOf_Kind { .userMessage(.with { $0.text = text }) }
    static func agent(_ text: String) -> PBEvent.OneOf_Kind { .agentMessage(.with { $0.text = text }) }
    static let turnStarted = PBEvent.OneOf_Kind.turnStarted(PBTurnStarted())

    static func turnEnded(_ outcome: PBTurnOutcome = .unspecified, durationMs: Int64 = 0) -> PBEvent.OneOf_Kind {
        .turnEnded(
            .with {
                $0.outcome = outcome
                $0.durationMs = durationMs
            })
    }

    static let exited = PBEvent.OneOf_Kind.sessionState(.with { $0.status = .exited })
}
