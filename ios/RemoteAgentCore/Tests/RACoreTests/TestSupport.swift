import Foundation
import RAProto
@testable import RACore

/// A one-shot gate for holding async work until the test releases it.
actor TestLatch {
    private var released = false
    private var waiting: [CheckedContinuation<Void, Never>] = []

    func wait() async {
        if released { return }
        await withCheckedContinuation { waiting.append($0) }
    }

    func open() {
        released = true
        for waiter in waiting { waiter.resume() }
        waiting = []
    }
}

/// Image-only upload fakes reject file reservations and have nothing to abort.
protocol ImageTestChannel: UploadChannel {}

extension ImageTestChannel {
    func beginFile(transferId: String, name: String, total: Int64, sha256: Data) async throws -> PBImageUploadStatus {
        throw UserError("unexpected file upload in image test")
    }
    func abort(transferId: String) async throws {}
}
