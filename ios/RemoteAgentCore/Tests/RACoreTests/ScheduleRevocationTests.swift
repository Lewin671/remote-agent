import Foundation
import RAProto
import Testing
@testable import RACore

extension ServerTests {
    @Suite(.serialized) struct ScheduleRevocationTests {
        func snapshot(_ w: World) throws -> Data {
            var h=PBHeader();h.kind = .commandResult;h.machineID=w.daemon.id;h.sender=w.daemon.id;h.requestID="00112233445566778899aabbccddeeff"
            var result=PBCommandResult()
            result.schedule.resultJson=Data(#"{"tasks":[{"id":"task","spec":{"title":"private scheduled task","message":"check","trigger":{"type":"interval"},"target":{"type":"new_session"}}}]}"#.utf8)
            return try w.phone.fixture.seal(w.daemon.account,h,result).serializedData()
        }
        @Test func revokedArchiveNeverRestoresAndLiveAndColdCachesArePruned() async throws {
            try await World.run(setUp:{w in try await w.daemon.putMachine("test-computer")}) {w in
                let cache=MemoryCache(), r=try await w.client(cache:cache), id=w.daemon.id
                _ = await r.syncSecuritySnapshot(opId: 1, networkGeneration: 0, touch: false)
                let bytes=try snapshot(w);await cache.putBlob(name:"schedules/"+id,data:bytes)
                #expect(await r.cachedScheduleResultJson(id) != nil)
                try await w.phone.remove([id])
                let snap = await r.syncSecuritySnapshot(opId: 2, networkGeneration: 0, touch: false)
                #expect(!snap.trustedMachineIds.contains(id) && snap.archiveMachineIds.contains(id))
                #expect(await cache.blob(name:"schedules/"+id) == nil)
                // A stale cache copied from an older run must also be rejected cold.
                await cache.putBlob(name:"schedules/"+id,data:bytes)
                let cold=try await w.remote(r.account,cache:cache)
                #expect(await cold.cachedScheduleResultJson(id) == nil)
                #expect(await cache.blob(name:"schedules/"+id) == nil)
                // Cold bootstrap read prunes it even when no task screen is opened.
                await cache.putBlob(name:"schedules/"+id,data:bytes)
                let reading=Signal(), hooked=HookedCache(cache)
                hooked.hookMachines { reading.set() }
                let restoring=try await w.remote(r.account,cache:hooked)
                _ = await restoring.executeCacheRead(PBCoreCacheReadEffect.with {
                    $0.bootstrap = PBCoreCacheReadBootstrap.with { $0.opID = 1 }
                })
                #expect(await reading.wait())
                #expect(await cache.blob(name:"schedules/"+id) == nil)
            }
        }
        @Test func revokedDuringCachedReadCannotRestoreHistoricalReply() async throws {
            try await World.run(setUp:{w in try await w.daemon.putMachine("test-computer")}) {w in
                let cache=HookedCache(), r=try await w.client(cache:cache), id=w.daemon.id
                _ = await r.syncSecuritySnapshot(opId: 1, networkGeneration: 0, touch: false)
                await cache.putBlob(name:"schedules/"+id,data:try snapshot(w))
                let entered=Signal(), release=Signal();w.onClose { release.set() }
                cache.hookBlob { name,value in if name=="schedules/"+id { entered.set();await release.wait() };return value }
                let read=Task { await r.cachedScheduleResultJson(id) }
                #expect(await entered.wait())
                try await w.phone.remove([id])
                _ = await r.syncSecuritySnapshot(opId: 2, networkGeneration: 0, touch: false)
                release.set();#expect(await read.value == nil)
                #expect(await cache.blob(name:"schedules/"+id) == nil)
            }
        }
        @Test func revokedDuringSnapshotWriteCannotPutBackTheCache() async throws {
            try await World.run(setUp:{w in try await w.daemon.putMachine("test-computer")}) {w in
                let cache=HookedCache(), r=try await w.client(cache:cache), id=w.daemon.id
                _ = await r.syncSecuritySnapshot(opId: 1, networkGeneration: 0, touch: false)
                let entered=Signal(), release=Signal();w.onClose { release.set() }
                cache.hookPutBlob { name,_ in if name=="schedules/"+id { entered.set();await release.wait() } }
                let bytes=try snapshot(w)
                let write=Task { await r.keepScheduleSnapshot(id,data:bytes) }
                #expect(await entered.wait())
                try await w.phone.remove([id])
                _ = await r.syncSecuritySnapshot(opId: 2, networkGeneration: 0, touch: false)
                release.set();await write.value
                #expect(await cache.blob(name:"schedules/"+id) == nil)
            }
        }
    }
}
