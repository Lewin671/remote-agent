import Foundation
import RAProto
import Testing

@testable import RACore

/// Block planning against the shared vectors and Core local-state mapping.
/// Shared file display/classification/menu rules are tested in `clientcore/domain/sharedfiles`.
@Suite(.timeLimit(.minutes(2))) struct SharedFilesTests {
    @Test func theBlocksAreThoseOfTheSharedVectors() throws {
        let v = try loadJSON("testdata/shared-files/blocks.json")
        let block = Int(v.s("blockBytes"))!
        #expect(block == defaultSharedFileBlockBytes)
        for c in v["cases"] as! [[String: Any]] {
            let size = Int64(c.s("sizeBytes"))!
            let plan = planSharedFileBlocks(size: size, blockBytes: block)
            #expect(plan.offsets == (c["offsets"] as! [String]).map { Int64($0)! }, "\(c.s("name"))")
            #expect(plan.lastBlockFileBytes == Int(c.s("lastBlockFileBytes"))!, "\(c.s("name"))")
            for o in plan.offsets { #expect(isValidSharedFileOffset(o, size: size, blockBytes: block)) }
            for o in (c["invalidOffsets"] as! [String]).map({ Int64($0)! }) {
                #expect(!isValidSharedFileOffset(o, size: size, blockBytes: block), "\(c.s("name")) \(o)")
            }
        }
    }

    @Test func theMachineNamesItsBlockSize() {
        var m = MachineView(id: "m", online: true, lastSeenMs: 0, meta: PBMachineMeta())
        #expect(sharedFileBlockBytes(m) == 786_432)
        m.meta?.capabilities = .with { $0.sharedFiles = true; $0.sharedFileChunkBytes = 4096 }
        #expect(sharedFileBlockBytes(m) == 4096)
    }

    @Test func localStateMappingMatchesProtoContract() {
        let key = SharedFileKey(sessionId: "s1", fileId: "f1")
        let idle = sharedFileLocalStateUpdate(key: key, transfer: .idle)
        #expect(idle.status == .unspecified && !idle.metadataOnly)

        let dl = sharedFileLocalStateUpdate(key: key, transfer: .downloading(done: 512, total: 1024, etaSeconds: 9, waitingReconnect: true))
        #expect(dl.status == .downloading && dl.downloadedBytes == 512 && dl.hasEta_p && dl.etaSeconds == 9 && dl.waitingReconnect)

        let paused = sharedFileLocalStateUpdate(key: key, transfer: .paused(done: 512, total: 1024))
        #expect(paused.status == .paused && paused.downloadedBytes == 512)

        let done = sharedFileLocalStateUpdate(key: key, transfer: .downloaded(url: URL(fileURLWithPath: "/tmp/a.mp4"), durationMs: 102_000))
        #expect(done.status == .downloaded && done.hasDuration_p && done.durationMs == 102_000)

        let diskFull = sharedFileLocalStateUpdate(key: key, transfer: .failed(.diskFull))
        #expect(diskFull.status == .failed && diskFull.failureKind == .phoneStorageFull)

        let other = sharedFileLocalStateUpdate(key: key, transfer: .failed(.other("io err")))
        #expect(other.status == .failed && other.failureKind == .other && other.failureDetail == "io err")

        let cleaned = sharedFileLocalStateUpdate(key: key, transfer: .cleanedUp)
        #expect(cleaned.status == .cleanedUp)
    }
}
