import Foundation
import RAProto
import Testing

@testable import RACore

/// A daemon per machine that takes uploads whole, and can be told to fail or to hold them.
private actor FakeMachines {
    struct Refused: UploadFailure, LocalizedError {
        let code: String
        let message: String
        var errorDescription: String? { message }
    }

    private(set) var begun: [String: [String]] = [:]
    private var stored: [String: Data] = [:]
    private var failing: Refused?
    /// Transfers the daemon no longer knows, once each.
    private var forgotten = 0
    private var held: [CheckedContinuation<Void, Never>] = []
    private var holding = false
    private var expiry = Int64.max
    func expires(_ value: Int64) { expiry = value }

    func fail(_ code: String, _ message: String) { failing = Refused(code: code, message: message) }
    func mend() { failing = nil }
    func forgetNext() { forgotten = 1 }
    func hold() { holding = true }
    func letGo() {
        holding = false
        for c in held { c.resume() }
        held = []
    }

    func transfers(_ machine: String) -> [String] { begun[machine] ?? [] }

    func begin(_ machine: String, _ id: String, total: Int64) async throws -> PBImageUploadStatus {
        if let failing { throw failing }
        begun[machine, default: []].append(id)
        if holding { await withCheckedContinuation { held.append($0) } }
        if forgotten > 0 {
            forgotten -= 1
            throw Refused(code: "not_found", message: "没有这个上传")
        }
        return .with {
            $0.transferID = id
            $0.totalBytes = total
            $0.state = .receiving
        }
    }

    func put(_ machine: String, _ id: String, _ data: Data) -> PBImageUploadStatus {
        stored[id, default: Data()].append(data)
        return .with {
            $0.transferID = id
            $0.persistedBytes = Int64(stored[id]?.count ?? 0)
            $0.state = .receiving
        }
    }

    func complete(_ machine: String, _ id: String) -> PBImageUploadStatus {
        .with {
            $0.transferID = id
            $0.state = .complete
            $0.expiresMs = expiry
            $0.imageID = "\(machine)-img-\(id.prefix(6))"
        }
    }
}

private struct FakeChannel: UploadChannel {
        func beginFile(transferId: String, name: String, total: Int64, sha256: Data) async throws -> PBImageUploadStatus {
            throw UserError("unexpected file upload in image test")
        }
        func abort(transferId: String) async throws {}

    let machines: FakeMachines
    let machine: String

    func begin(transferId: String, format: PBImageFormat, total: Int64, sha256: Data) async throws -> PBImageUploadStatus {
        try await machines.begin(machine, transferId, total: total)
    }
    func put(transferId: String, offset: Int64, data: Data) async throws -> PBImageUploadStatus {
        await machines.put(machine, transferId, data)
    }
    func status(transferId: String) async throws -> PBImageUploadStatus { PBImageUploadStatus() }
    func complete(transferId: String) async throws -> PBImageUploadStatus { await machines.complete(machine, transferId) }
}

/// What the picker gave, by URI, and how often each was prepared.
private final class FakeSources: @unchecked Sendable {
    private let lock = NSLock()
    private var counts: [String: Int] = [:]
    private var broken = Set<String>()

    /// The smallest file `detectFormat` takes for a PNG.
    static let png = Data([0x89]) + Data("PNG\r\n\u{1A}\n".utf8) + Data(repeating: 7, count: 40)

    func breakIt(_ uri: String) { lock.withLock { _ = broken.insert(uri) } }
    func mend(_ uri: String) { lock.withLock { _ = broken.remove(uri) } }
    func count(_ uri: String) -> Int { lock.withLock { counts[uri] ?? 0 } }

    func prepare(_ uri: String, _ original: Bool) throws -> Processed {
        try lock.withLock {
            counts[uri, default: 0] += 1
            if broken.contains(uri) { throw UserError("解不出这张图片") }
            return Processed(bytes: Self.png, format: .png, width: original ? 4000 : 2048, height: 10, note: original ? "原" : "")
        }
    }
}

/// Holds whoever waits at it until it is opened.
private actor Gate {
    private var open = false
    private var waiting: [CheckedContinuation<Void, Never>] = []

    func wait() async {
        if open { return }
        await withCheckedContinuation { waiting.append($0) }
    }

    func letThrough() {
        open = true
        for w in waiting { w.resume() }
        waiting = []
    }
}

/// Android's `ImageDraft` jobs: prepare, send, send again (ui/ImageDrafts.kt).
@MainActor @Suite struct ImageSendingTests {
    private let machines = FakeMachines()
    private let sources = FakeSources()
    private let files = MemoryFiles()

    private func uploads(readable: @escaping @MainActor (String) -> Bool = { _ in true }) -> ImageUploads {
        let (machines, sources) = (machines, sources)
        return ImageUploads(
            prepare: { uri, original in try sources.prepare(uri, original) },
            channel: { FakeChannel(machines: machines, machine: $0) }, readable: readable)
    }

    private func draft(_ key: String = "session:s") -> ImageDraft { ImageDraft(key: key, files: files, saves: nil) }

    /// Waits until nothing works on the image any more.
    private func settled(_ img: PendingImage) async {
        for _ in 0..<2000 {
            switch img.state {
            case .ready, .failed: return
            default: try? await Task.sleep(nanoseconds: 1_000_000)
            }
        }
        Issue.record("the image never settled: \(img.state)")
    }

    @Test func aPickedImageIsPreparedThenSentAndKept() async throws {
        let (u, d) = (uploads(), draft())
        var prepared: [String] = []
        u.onPrepared = { prepared.append($0.id) }
        let added = u.add(d, uris: ["picked:a", "picked:b"], machine: { "mac" })
        #expect(d.images.map(\.uri) == ["picked:a", "picked:b"])
        #expect(added.allSatisfy { $0.state == .processing })
        // Not ready until every one of them is on the computer.
        #expect(!d.ready(machineId: "mac"))
        for img in added { await settled(img) }
        #expect(d.ready(machineId: "mac"))
        #expect(d.ids(machineId: "mac").count == 2 && Set(d.ids(machineId: "mac")).count == 2)
        #expect(Set(prepared) == Set(added.map(\.id)))
        // The prepared bytes are on disk for a reclaimed process.
        #expect(try files.get(folder: "session:s", name: added[0].id + ".img") == FakeSources.png)
        #expect(await machines.transfers("mac").count == 2)
    }

    @Test func withoutAMachineTheImageWaitsForOne() async {
        let (u, d) = (uploads(), draft())
        let img = u.add(d, uris: ["picked:a"], machine: { nil })[0]
        await settled(img)
        #expect(img.state == .failed(error: "先选一台电脑"))
        #expect(img.processed != nil)
        u.retry(d, img, machine: { "mac" })
        await settled(img)
        #expect(img.readyFor("mac"))
        // Sent, not prepared a second time.
        #expect(sources.count("picked:a") == 1)
    }

    @Test func anImageThatCouldNotBePreparedIsPreparedAgainOnRetry() async {
        let (u, d) = (uploads(), draft())
        sources.breakIt("picked:a")
        let img = u.add(d, uris: ["picked:a"], machine: { "mac" })[0]
        await settled(img)
        #expect(img.state == .failed(error: "解不出这张图片"))
        #expect(img.processed == nil)
        sources.mend("picked:a")
        u.retry(d, img, machine: { "mac" })
        await settled(img)
        #expect(img.readyFor("mac") && sources.count("picked:a") == 2)
    }

    @Test func aRefusedUploadSaysWhyAndGoesAgainWithTheSameBytes() async {
        let (u, d) = (uploads(), draft())
        await machines.fail("resource_exhausted", "电脑上传目录已满")
        let img = u.add(d, uris: ["picked:a"], machine: { "mac" })[0]
        await settled(img)
        #expect(img.state == .failed(error: "电脑上传目录已满"))
        #expect(!d.ready(machineId: "mac"))
        await machines.mend()
        u.retry(d, img, machine: { "mac" })
        await settled(img)
        #expect(img.readyFor("mac") && sources.count("picked:a") == 1)
    }

    @Test func aTransferTheDaemonForgotStartsOver() async {
        let (u, d) = (uploads(), draft())
        await machines.forgetNext()
        let img = u.add(d, uris: ["picked:a"], machine: { "mac" })[0]
        await settled(img)
        #expect(img.readyFor("mac"))
        // Under a transfer of its own, not the one that expired.
        let ids = await machines.transfers("mac")
        #expect(ids.count == 2 && ids[0] != ids[1] && img.transferId == ids[1])
    }

    @Test func theLatestMachineWins() async {
        let (u, d) = (uploads(), draft())
        let img = u.add(d, uris: ["picked:a"], machine: { nil })[0]
        await settled(img)
        await machines.hold()
        u.upload(img, machineId: "mac")
        let first = img.transferId
        #expect(img.state == .processing && img.uploadTarget == "mac" && img.machineId == nil)
        // A second ask for the same machine adds nothing.
        u.upload(img, machineId: "mac")
        #expect(img.transferId == first)
        // The new-session card changed machines: the upload under way is dropped.
        u.upload(img, machineId: "vps")
        #expect(img.transferId != first && img.uploadTarget == "vps" && img.machineId == nil && img.imageId == nil)
        await machines.letGo()
        await settled(img)
        #expect(img.readyFor("vps") && !img.readyFor("mac"))
        #expect(img.imageId?.hasPrefix("vps-img-") == true)
    }

    @Test func everyImageGoesToTheNewMachineOnce() async {
        let (u, d) = (uploads(), draft())
        let added = u.add(d, uris: ["picked:a", "picked:b"], machine: { "mac" })
        for img in added { await settled(img) }
        u.sendAll(d, machineId: "mac")
        #expect(await machines.transfers("mac").count == 2)
        u.sendAll(d, machineId: "vps")
        for img in added { await settled(img) }
        #expect(d.ready(machineId: "vps") && !d.ready(machineId: "mac"))
        #expect(await machines.transfers("vps").count == 2)
    }

    @Test func imagesKeptFromAnEarlierRunGoToTheComputerAgain() async throws {
        let (u, first) = (uploads(), draft())
        let img = u.add(first, uris: ["picked:a"], machine: { "mac" })[0]
        await settled(img)
        // The process was reclaimed: what is on disk is all there is.
        let d = draft()
        try d.restore()
        #expect(d.images.map(\.id) == [img.id])
        #expect(d.images[0].state == .failed(error: "需要重新传到电脑"))
        u.sendAll(d, machineId: "mac")
        await settled(d.images[0])
        #expect(d.ready(machineId: "mac"))
    }

    @Test func changingModeRequiresEverySourceSoOldBytesAreNeverSentAsOriginals() async {
        let u = uploads(readable: { $0 != "picked:old" })
        let d = draft()
        let added = u.add(d, uris: ["picked:old", "picked:new"], machine: { "mac" })
        for img in added { await settled(img) }
        #expect(!u.canReprepare(d))
        u.setOriginal(d, true, machine: { "mac" })
        #expect(!d.original)
        #expect(sources.count("picked:new") == 1)
        d.remove(added[0])
        #expect(u.canReprepare(d))
        u.setOriginal(d, true, machine: { "mac" })
        await added[1].job?.value
        await added[1].job?.value
        #expect(d.original && added[1].processed?.note == "原" && added[1].readyFor("mac"))
    }

    @Test func failedModePreparationRetriesTheSourceAndNeverUploadsOldBytes() async {
        let (u, d) = (uploads(), draft())
        let img = u.add(d, uris: ["picked:a"], machine: { "mac" })[0]
        await img.job?.value
        await img.job?.value
        let old = img.processed
        sources.breakIt("picked:a")
        u.setOriginal(d, true, machine: { "mac" })
        await img.job?.value
        #expect(img.processed == old && img.needsProcessing)
        u.sendAll(d, machineId: "vps")
        #expect(await machines.transfers("vps").isEmpty)
        u.retry(d, img, machine: { "mac" })
        await img.job?.value
        #expect(sources.count("picked:a") == 3)
        #expect(await machines.transfers("mac").count == 1)
        sources.mend("picked:a")
        u.retry(d, img, machine: { "mac" })
        await img.job?.value
        await img.job?.value
        #expect(!img.needsProcessing && img.processed?.note == "原" && img.readyFor("mac"))
    }

    @Test func expiryRenewsTheTransferAtTheExactDeadlineAndDetachingCancelsTheTimer() async {
        let gate = Gate()
        let machines = machines
        var now: Int64 = 1000
        await machines.expires(1100)
        let u = ImageUploads(
            prepare: { _, _ in Processed(bytes: FakeSources.png, format: .png, width: 1, height: 1) },
            channel: { FakeChannel(machines: machines, machine: $0) }, clock: { now }, sleep: { _ in await gate.wait() })
        let d = draft()
        let img = u.add(d, uris: ["picked:a"], machine: { "mac" })[0]
        await img.job?.value
        await img.job?.value
        let transfer = img.transferId
        #expect(img.expiresMs == 1100 && img.readyFor("mac", now: 1099))
        #expect(!img.readyFor("mac", now: 1100))
        now = 1100
        await machines.expires(1200)
        let timer = img.refreshJob
        await gate.letThrough()
        await timer?.value
        await img.job?.value
        #expect(img.transferId != transfer && img.expiresMs == 1200)
        #expect(await machines.transfers("mac").count == 2)
        let refresh = img.refreshJob
        d.detach([img])
        #expect(refresh?.isCancelled == true && img.refreshJob == nil)
        #expect(img.imageId == nil && img.machineId == nil && img.expiresMs == 0)
    }

    @Test func anImageStillBeingPreparedAgainIsNotTakenForSaved() async throws {
        let gate = Gate()
        let machines = machines
        let u = ImageUploads(
            prepare: { uri, original in
                if original, uri == "picked:b" { await gate.wait() }
                return Processed(bytes: Data("\(uri) \(original)".utf8), format: .png, width: 1, height: 1)
            },
            channel: { FakeChannel(machines: machines, machine: $0) })
        let d = draft()
        let added = u.add(d, uris: ["picked:a", "picked:b"], machine: { "mac" })
        for img in added { await settled(img) }
        func kept(_ img: PendingImage) throws -> String {
            String(decoding: try #require(try files.get(folder: "session:s", name: img.id + ".img")), as: UTF8.self)
        }
        #expect(try kept(added[0]) == "picked:a false" && kept(added[1]) == "picked:b false")
        // 原图: the first is ready and saved while the second is still being read.
        u.setOriginal(d, true, machine: { "mac" })
        await settled(added[0])
        #expect(added[1].state == .processing)
        #expect(try kept(added[0]) == "picked:a true")
        await gate.letThrough()
        await settled(added[1])
        // What a reclaimed process would find is what the draft says it sends.
        #expect(try kept(added[1]) == "picked:b true")
    }

    @Test func anImageRemovedWhileItIsOnItsWayStaysGone() async {
        let (u, d) = (uploads(), draft())
        let img = u.add(d, uris: ["picked:a"], machine: { nil })[0]
        await settled(img)
        await machines.hold()
        u.upload(img, machineId: "mac")
        d.remove(img)
        await machines.letGo()
        try? await Task.sleep(nanoseconds: 30_000_000)
        #expect(d.images.isEmpty && img.imageId == nil && img.processed == nil)
        #expect(img.imageId == nil && img.machineId == nil && !img.readyFor("mac"))
    }
}

extension ImageSendingTests {
    @Test func aRestoredDraftKeepsOldModeBytesForRecoveryAndDoesNotUploadThem() async throws {
        let (u, first) = (uploads(), draft())
        let img = u.add(first, uris: ["picked:a"], machine: { "mac" })[0]
        await img.job?.value
        await img.job?.value
        sources.breakIt("picked:a")
        u.setOriginal(first, true, machine: { "mac" })
        await img.job?.value
        let restored = draft()
        try await restored.restoreAsync()
        let back = try #require(restored.images.first)
        #expect(restored.original && back.needsProcessing && back.processed?.note == "")
        u.sendAll(restored, machineId: "vps")
        #expect(await machines.transfers("vps").isEmpty)
        sources.mend("picked:a")
        u.retry(restored, back, machine: { "vps" })
        await back.job?.value
        await back.job?.value
        #expect(back.processed?.note == "原" && back.readyFor("vps"))
        let again = draft()
        try await again.restoreAsync()
        #expect(again.original && again.images.first?.needsProcessing == false && again.images.first?.processed?.note == "原")
    }
}
