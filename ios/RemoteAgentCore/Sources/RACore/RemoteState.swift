// What the sync engine publishes: the account's machines, sessions and
// devices as far as they are known, and how the connection stands. The
// same shapes as Android's core/Remote.kt, so the rules read alike.
import Foundation
import RAProto

public enum ConnState: Sendable { case connecting, online, offline }

/// Which stretch of the way to the computer is down, the nearest one only
/// (client-v30 「断线」): past it nothing is known. `waiting` is a connection
/// being made or just lost, which mostly comes back by itself.
public enum ServerLink: Sendable { case online, waiting, noNetwork, noServer }

public struct MachineView: Equatable, Sendable, Identifiable {
    public var id: String
    public var online: Bool
    public var lastSeenMs: Int64
    public var meta: PBMachineMeta?
    public var metaRev: UInt64
    /// Runtime projection only; never persisted or read from server metadata.
    public var linkState: PeerState?
    public var pathState: PeerState { linkState ?? RelayPath.discovery(self) }

    public init(
        id: String, online: Bool, lastSeenMs: Int64, meta: PBMachineMeta? = nil, metaRev: UInt64 = 0, linkState: PeerState? = nil
    ) {
        self.id = id
        self.online = online
        self.lastSeenMs = lastSeenMs
        self.meta = meta
        self.metaRev = metaRev
        self.linkState = linkState
    }
}

public struct SessionView: Equatable, Sendable, Identifiable {
    public var historyError: String?
    public var id: String
    public var machineId: String
    public var checkpoint: PBLiveSnapshot?
    public var checkpointStatus: String = "waiting"
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
        self.checkpoint = nil
        self.metaRev = metaRev
        self.lastSeq = lastSeq
        self.updatedMs = updatedMs
        self.listedEndedMs = listedEndedMs
    }
}

public struct RemoteState: Equatable, Sendable {
    public var machines: [String: MachineView] = [:]
    /// Current signed machine members; `machines` also contains readable archives.
    public var currentMachines: Set<String> = []
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
    public var recoveryPending = false
    public var recoveryReadyMs: UInt64 = 0
    public var recoveryStatusStale = true
    public var leaseEndMs: UInt64 = 0
    public var removed = false
    public var link: ServerLink = .waiting
    /// The lists came from the server since start: what they say of the computers is not from the cache.
    public var synced = false
    /// The failure behind `error` in its own words, for the details.
    public var errorDetail = ""
    /// The local clock is more than five minutes behind a signed state; new
    /// control stays unsent until it is corrected.
    public var clockWarning = ""
    public var displayGrace = false
    public var reachabilityUnknown = false

    public init() {}

    public func peer(_ machineId: String?) -> PeerState {
        let machine = machineId.flatMap { machines[$0] }
        return machine?.linkState ?? RelayPath.discovery(machine, ready: conn == .online, authorized: !removed, unlinked: unlinked)
    }

    /// Nothing here is known to still hold: the server is out of reach, or
    /// has not been reached yet. A connection lost a moment ago does not count.
    public var unlinked: Bool {
        link == .noNetwork || link == .noServer || (link == .waiting && !synced)
    }
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
