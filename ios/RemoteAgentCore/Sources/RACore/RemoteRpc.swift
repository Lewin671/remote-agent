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
                    loaded.wireMachines.append(m)
                }
                loaded.wireSessions = await cache.sessions()
                if let blob = await cache.blob(name: projectPrefsID),
                   let obj = try? PBAccountPrefsObject(serializedBytes: blob) {
                    loaded.wirePrefs = obj
                }
            }
            event.cacheBootstrapLoaded = loaded
        case .events(let req):
            var loaded = PBCoreCacheEventsLoadedEvent()
            loaded.opID = req.opID
            loaded.sessionID = req.sessionID
            if let cache {
                loaded.wireEvents = req.newest
                    ? await cache.newestEvents(sessionId: req.sessionID)
                    : await cache.eventPage(sessionId: req.sessionID, afterSeq: req.afterSeq)
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
        case .writeMachine(let m):
            await cache.putMachine(m)
        case .writeSession(let rs):
            await cache.putSession(rs)
        case .keepSessionOwner(let rs):
            await cache.keepOwner(rs)
        case .writeStoredEvents(let batch):
            if !batch.events.isEmpty {
                await cache.putEvents(sessionId: batch.sessionID, events: batch.events)
            }
        case .writePrefs(let obj):
            if let data = try? obj.serializedData() {
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
