// What the sync engine publishes: the account's machines, sessions and
// devices as far as they are known, and how the connection stands. The
// same shapes as Android's core/Remote.kt, so the rules read alike.
import Foundation
import RAProto

public enum ConnState: Sendable { case connecting, online, offline }

/// Which stretch of the way to the computer is down, the nearest one only
/// (client-v30 「断线」): past it nothing is known. `waiting` is a connection
/// being made or just lost, which mostly comes back by itself.
public enum Link: Sendable { case online, waiting, noNetwork, noServer }

public struct MachineView: Equatable, Sendable, Identifiable {
    public var id: String
    public var online: Bool
    public var lastSeenMs: Int64
    public var meta: PBMachineMeta?
    public var metaRev: UInt64

    public init(
        id: String, online: Bool, lastSeenMs: Int64, meta: PBMachineMeta? = nil, metaRev: UInt64 = 0
    ) {
        self.id = id
        self.online = online
        self.lastSeenMs = lastSeenMs
        self.meta = meta
        self.metaRev = metaRev
    }
}

public struct SessionView: Equatable, Sendable, Identifiable {
    public var id: String
    public var machineId: String
    public var info: PBSession?
    public var metaRev: UInt64
    public var lastSeq: UInt64
    public var updatedMs: Int64
    /// When the session ended as the server lists it (RemoteSession.ended_ms),
    /// 0 while it has not. It only places the session among the pages of the
    /// listing; what is shown and sorted by comes from `info`.
    public var listedEndedMs: Int64

    public init(
        id: String, machineId: String, info: PBSession? = nil, metaRev: UInt64 = 0,
        lastSeq: UInt64 = 0,
        updatedMs: Int64 = 0, listedEndedMs: Int64 = 0
    ) {
        self.id = id
        self.machineId = machineId
        self.info = info
        self.metaRev = metaRev
        self.lastSeq = lastSeq
        self.updatedMs = updatedMs
        self.listedEndedMs = listedEndedMs
    }
}

/// A place in the server's listing of ended sessions: the latest ended first, by session ID where those are equal.
public struct SessionPlace: Equatable, Sendable {
    public var endedMs: Int64
    public var sessionId: String

    public init(endedMs: Int64, sessionId: String) {
        self.endedMs = endedMs
        self.sessionId = sessionId
    }
}

/// Whether a session ended at `endedMs` comes after `place` in the listing; one that has not ended is never there.
public func afterPlace(endedMs: Int64, sessionId: String, place: SessionPlace) -> Bool {
    endedMs != 0
        && (endedMs < place.endedMs || (endedMs == place.endedMs && sessionId > place.sessionId))
}

/// Ended sessions asked for per page of the listing.
public let sessionPage = 50

public struct RemoteState: Equatable, Sendable {
    public var machines: [String: MachineView] = [:]
    public var sessions: [String: SessionView] = [:]
    public var conn: ConnState = .connecting
    public var error = ""
    /// The lists have been fetched once since start, or read from the cache.
    public var loaded = false
    /// When the connection was last lost; 0 while online. Timers stop here: what happened since is not known.
    public var offlineSinceMs: Int64 = 0
    /// The account's project preferences; nil until read (from the server or the cache).
    public var prefs: PrefsObject?
    /// The account's devices, machines and clients (ListDevices).
    public var devices: [String: DeviceView] = [:]
    /// The server no longer knows this device: removed from the account, or the server's data is gone.
    public var authorityDeviceId = ""
    public var removed = false
    /// Where the next page of ended sessions starts (architecture §8
    /// 「会话列表分页」): `sessions` holds every session that has not ended and
    /// the ended ones up to here, besides any fetched on their own. Nil when
    /// every page was fetched, and before the first one (`sessionsListed`).
    public var sessionsNext: SessionPlace?
    /// A page of the listing is being fetched.
    public var loadingSessions = false
    /// Why the last page could not be fetched; empty once one was, or the first page came again.
    public var sessionsError = ""
    /// The listing's first page was fetched in this run. Before that the
    /// sessions held are what the cache kept of an earlier run, and how many
    /// pages there are is not known.
    public var sessionsListed = true
    public var link: Link = .waiting
    /// The lists came from the server since start: what they say of the computers is not from the cache.
    public var synced = false
    /// The failure behind `error` in its own words, for the details.
    public var errorDetail = ""
    /// The local clock is more than five minutes behind a signed state; new
    /// control stays unsent until it is corrected.
    public var clockWarning = ""

    public init() {}

    /// Nothing here is known to still hold: the server is out of reach, or
    /// has not been reached yet. A connection lost a moment ago does not count.
    public var unlinked: Bool {
        link == .noNetwork || link == .noServer || (link == .waiting && !synced)
    }

    /// The server has, or may have, ended sessions that were not fetched yet.
    public var moreSessions: Bool { !sessionsListed || sessionsNext != nil }
}

/// A device of the account; a client's name is in its sealed metadata.
public struct DeviceView: Equatable, Sendable, Identifiable {
    public var id: String
    public var kind: PBDeviceKind
    public var createdMs: Int64
    public var meta: PBDeviceMeta?
    public var metaRev: UInt64
    /// When it was last used, 0 without a record.
    public var lastSeenMs: Int64

    public init(
        id: String, kind: PBDeviceKind, createdMs: Int64, meta: PBDeviceMeta? = nil,
        metaRev: UInt64 = 0,
        lastSeenMs: Int64 = 0
    ) {
        self.id = id
        self.kind = kind
        self.createdMs = createdMs
        self.meta = meta
        self.metaRev = metaRev
        self.lastSeenMs = lastSeenMs
    }
}

/// A session's events, contiguous from seq 1. Each snapshot shares the
/// pages of the one before it (`PagedList`), so a long history is not copied
/// for every event that arrives, and `events.extends(previous.events)` tells
/// cheaply that it only grew.
public struct SessionEvents: Sendable {
    public var live: PBLiveSnapshot?
    public var liveError: String?
    public var unverifiedLegacy = false
    public var verifiedEvents = PagedList<PBEvent>()
    public var events: PagedList<PBEvent> = []
    public var loaded = false
    /// Set when an event does not open: history stops before it.
    public var error: String?
    /// Set when the last fetch failed; the next connection fetches again.
    public var fetchError: String?

    public init(
        events: PagedList<PBEvent> = [], loaded: Bool = false, error: String? = nil,
        fetchError: String? = nil
    ) {
        self.events = events
        self.loaded = loaded
        self.error = error
        self.fetchError = fetchError
    }

    public init(
        events: [PBEvent], loaded: Bool = false, error: String? = nil, fetchError: String? = nil
    ) {
        self.init(events: PagedList(events), loaded: loaded, error: error, fetchError: fetchError)
    }
}
