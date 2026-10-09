import Foundation
import RAProto

/// An upload goes on after a command whose outcome is unknown, by asking the daemon where it stands.
extension CommandError: UploadFailure {}

extension Remote {
    /// The image upload commands on one machine.
    public nonisolated func uploadChannel(machineId: String) -> any UploadChannel {
        MachineUploads(remote: self, machineId: machineId)
    }
}

private struct MachineUploads: UploadChannel {
    let remote: Remote
    let machineId: String

    private func run(_ fill: (inout PBCommand) -> Void) async throws -> PBCommandResult {
        var cmd = PBCommand()
        fill(&cmd)
        return try await remote.machineCommand(machineId, cmd)
    }

    func begin(transferId: String, format: PBImageFormat, total: Int64, sha256: Data) async throws -> PBImageUploadStatus {
        try await run {
            $0.beginImageUpload.transferID = transferId
            $0.beginImageUpload.format = format
            $0.beginImageUpload.totalBytes = total
            $0.beginImageUpload.sha256 = sha256
        }.beginImageUpload.status
    }

    func put(transferId: String, offset: Int64, data: Data) async throws -> PBImageUploadStatus {
        try await run {
            $0.putImageChunk.transferID = transferId
            $0.putImageChunk.offset = offset
            $0.putImageChunk.data = data
        }.putImageChunk.status
    }

    func status(transferId: String) async throws -> PBImageUploadStatus {
        try await run { $0.getImageUploadStatus.transferID = transferId }.getImageUploadStatus.status
    }

    func complete(transferId: String) async throws -> PBImageUploadStatus {
        try await run { $0.completeImageUpload.transferID = transferId }.completeImageUpload.status
    }
}
