import Foundation
import RAProto
import Testing
@testable import RACore

extension ServerTests {
    @Suite struct ScheduleRevocationTests {
        func snapshot(_ w: World) throws -> Data {
            var h=PBHeader();h.kind = .commandResult;h.machineID=w.daemon.id;h.sender=w.daemon.id;h.requestID="00112233445566778899aabbccddeeff"
            var result=PBCommandResult()
            let task=ScheduleTask(id:"task",spec:ScheduleSpec(title:"private scheduled task",message:"check",trigger:ScheduleTrigger(type:"interval"),target:ScheduleTarget(type:"new_session")))
            result.schedule.resultJson=try JSONSerialization.data(withJSONObject:["tasks":JSONSerialization.jsonObject(with:JSONEncoder().encode([task]))])
            return try w.phone.fixture.seal(w.daemon.account,h,result).serializedData()
        }
        @Test func revokedArchiveNeverRestoresAndLiveAndColdCachesArePruned() async throws {
            try await World.run(setUp:{w in try await w.daemon.putMachine("test-computer")}) {w in
                let cache=MemoryCache(), r=try await w.client(cache:cache), id=w.daemon.id
                try await r.refreshTrustedDirectory()
                let bytes=try snapshot(w);await cache.putBlob(name:"schedules/"+id,data:bytes)
                #expect(await r.cachedSchedules(id)?.count == 1)
                try await w.phone.remove([id]);try await r.refreshTrustedDirectory()
                #expect(r.current.machines[id] != nil && !r.current.currentMachines.contains(id))
                #expect(await cache.blob(name:"schedules/"+id) == nil)
                // A stale cache copied from an older run must also be rejected before start().
                await cache.putBlob(name:"schedules/"+id,data:bytes)
                let cold=try w.remote(r.account,cache:cache)
                #expect(await cold.cachedSchedules(id) == nil)
                #expect(await cache.blob(name:"schedules/"+id) == nil)
                // Cold restoration prunes it even when no task screen is opened.
                await cache.putBlob(name:"schedules/"+id,data:bytes)
                let reading=Signal(), hooked=HookedCache(cache)
                hooked.hookMachines { reading.set() }
                let restoring=try w.remote(r.account,cache:hooked);restoring.start()
                #expect(await reading.wait())
                #expect(await cache.blob(name:"schedules/"+id) == nil)
            }
        }
        @Test func revokedDuringCachedReadCannotRestoreHistoricalReply() async throws {
            try await World.run(setUp:{w in try await w.daemon.putMachine("test-computer")}) {w in
                let cache=HookedCache(), r=try await w.client(cache:cache), id=w.daemon.id
                try await r.refreshTrustedDirectory()
                await cache.putBlob(name:"schedules/"+id,data:try snapshot(w))
                let entered=Signal(), release=Signal();w.onClose { release.set() }
                cache.hookBlob { name,value in if name=="schedules/"+id { entered.set();await release.wait() };return value }
                let read=Task { await r.cachedSchedules(id) }
                #expect(await entered.wait())
                try await w.phone.remove([id]);try await r.refreshTrustedDirectory()
                release.set();#expect(await read.value == nil)
                #expect(await cache.blob(name:"schedules/"+id) == nil)
            }
        }
        @Test func revokedDuringSnapshotWriteCannotPutBackTheCache() async throws {
            try await World.run(setUp:{w in try await w.daemon.putMachine("test-computer")}) {w in
                let cache=HookedCache(), r=try await w.client(cache:cache), id=w.daemon.id
                try await r.refreshTrustedDirectory()
                let entered=Signal(), release=Signal();w.onClose { release.set() }
                cache.hookPutBlob { name,_ in if name=="schedules/"+id { entered.set();await release.wait() } }
                let bytes=try snapshot(w)
                let write=Task { await r.keepScheduleSnapshot(id,data:bytes) }
                #expect(await entered.wait())
                try await w.phone.remove([id]);try await r.refreshTrustedDirectory()
                release.set();await write.value
                #expect(await cache.blob(name:"schedules/"+id) == nil)
            }
        }
    }
}
