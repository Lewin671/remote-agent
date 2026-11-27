import Foundation
import RAProto
import SwiftProtobuf

extension Remote {
    // MARK: - Cache I/O Execution

    public func executeCacheRead(_ effect: PBCoreCacheReadEffect) async -> PBCoreEvent {
        var event = PBCoreEvent()
        switch effect.kind {
        case .bootstrap(let req):
            var loaded = PBCoreCacheBootstrapLoadedEvent()
            loaded.opID = req.opID
            if let cache {
                for var m in await cache.machines() {
                    m.online = false
                    if let raw = try? m.serializedData() {
                        loaded.rawMachines.append(raw)
                    }
                }
                loaded.rawSessions = await cache.sessions().compactMap { try? $0.serializedData() }
                if let blob = await cache.blob(name: projectPrefsID) {
                    loaded.rawPrefs = blob
                }
            }
            event.cacheBootstrapLoaded = loaded
        case .events(let req):
            var loaded = PBCoreCacheEventsLoadedEvent()
            loaded.opID = req.opID
            loaded.sessionID = req.sessionID
            if let cache {
                let evs = req.newest
                    ? await cache.newestEvents(sessionId: req.sessionID)
                    : await cache.eventPage(sessionId: req.sessionID, afterSeq: req.afterSeq)
                loaded.rawEvents = evs.compactMap { try? $0.serializedData() }
            }
            event.cacheEventsLoaded = loaded
        case nil:
            break
        }
        return event
    }

    public func executeCacheWrite(_ effect: PBCoreCacheWriteEffect) async {
        guard let cache else { return }
        switch effect.kind {
        case .rawWriteMachine(let raw):
            if let m = try? PBMachine(serializedBytes: raw) {
                await cache.putMachine(m)
            }
        case .rawWriteSession(let raw):
            if let rs = try? PBRemoteSession(serializedBytes: raw) {
                await cache.putSession(rs)
            }
        case .rawKeepSessionOwner(let raw):
            if let rs = try? PBRemoteSession(serializedBytes: raw) {
                await cache.keepOwner(rs)
            }
        case .writeStoredEvents(let batch):
            let parsed = batch.rawEvents.compactMap { try? PBStoredEvent(serializedBytes: $0) }
            if !parsed.isEmpty {
                await cache.putEvents(sessionId: batch.sessionID, events: parsed)
            }
        case .rawWritePrefs(let data):
            if !data.isEmpty {
                await cache.putBlob(name: projectPrefsID, data: data)
            }
        case .setSessionOpen, nil:
            break
        }
    }

    public func executeCacheDelete(
        _ effect: PBCoreCacheDeleteEffect,
        onSessionRemoved: (@Sendable (String) async -> Void)? = nil
    ) async {
        switch effect.kind {
        case .dropEventsSessionID(let sid):
            await cache?.dropEvents(sessionId: sid)
        case .removeSessionID(let sid):
            await cache?.removeSession(id: sid)
            await onSessionRemoved?(sid)
        case .unlistSessionID(let sid):
            await cache?.unlistSession(id: sid)
        case .removeMachineID(let mid):
            await cache?.removeMachine(id: mid)
            await cache?.removeBlob(name: "schedules/" + mid)
        case nil:
            break
        }
    }
}
