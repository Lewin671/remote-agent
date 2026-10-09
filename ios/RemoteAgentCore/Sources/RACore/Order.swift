// The orders sessions are listed in (architecture §8 「会话列表排序」). Each is a
// "comes before" for sorted(by:), and each ends on the session ID, so two
// sessions never tie and a list never reshuffles between two readings.
import Foundation
import RAProto
import SwiftProtobuf

func timestampMs(_ t: Google_Protobuf_Timestamp) -> Int64 { t.seconds * 1000 + Int64(t.nanos) / 1_000_000 }

// A session that has not ended or is not waiting has no such timestamp and
// falls back to creation time; incomplete metadata uses zero, never the
// changing relay timestamp.
public func createdMs(_ s: SessionView) -> Int64 {
    guard let i = s.info, i.hasCreatedAt else { return 0 }
    return timestampMs(i.createdAt)
}

public func endedMs(_ s: SessionView) -> Int64 {
    guard let i = s.info, i.hasEndedAt else { return createdMs(s) }
    return timestampMs(i.endedAt)
}

public func attentionMs(_ s: SessionView) -> Int64 {
    guard let i = s.info, i.hasAttentionSince else { return createdMs(s) }
    return timestampMs(i.attentionSince)
}

/// Sessions that go on: the newest created first.
public func liveOrder(_ a: SessionView, _ b: SessionView) -> Bool {
    let x = createdMs(a), y = createdMs(b)
    return x != y ? x > y : jvmLess(a.id, b.id)
}

/// Ended sessions: the latest ended first.
public func endedOrder(_ a: SessionView, _ b: SessionView) -> Bool {
    let x = endedMs(a), y = endedMs(b)
    return x != y ? x > y : jvmLess(a.id, b.id)
}

/// The order of the listing: the sessions that go on, then the ended ones.
public func sessionOrder(_ a: SessionView, _ b: SessionView) -> Bool {
    let ea = exited(a), eb = exited(b)
    if ea != eb { return !ea }
    return ea ? endedOrder(a, b) : liveOrder(a, b)
}

/// 需要处理: by what kind of thing waits, then the longest wait first.
public func attentionOrder(_ state: RemoteState) -> (SessionView, SessionView) -> Bool {
    { a, b in
        let sa = statusOf(a, machine: state.machines[a.machineId]).rawValue
        let sb = statusOf(b, machine: state.machines[b.machineId]).rawValue
        if sa != sb { return sa < sb }
        let x = attentionMs(a), y = attentionMs(b)
        return x != y ? x < y : jvmLess(a.id, b.id)
    }
}
