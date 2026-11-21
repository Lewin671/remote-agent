import Connect
import Foundation
import RAProto

extension Remote {
    /// The stored envelope's epoch is the resumable republish marker. Only a
    /// retained device that can verify and read it may publish the snapshot.
    func republishRecoveryObjects(_ record: DeviceSecurity.Record) async throws {
        guard let epoch = record.recovery.keys.max(),
              let boundary = record.states[epoch]?.core.contentEpoch else { return }
        let headers = Api.headers(token: try await currentToken())
        try await retryRecoveryPublication {
            var get = PBGetAccountPrefsRequest(); get.objectID = projectPrefsID
            let object = try await api.client.getAccountPrefs(request: get, headers: headers).value().object
            if !object.hasEnvelope || object.revision == 0 { return true }
            let opened: PBSecurityOpenEnvelopeResult
            do {
                opened = try await openEnvelopeOp(
                    envelope: object.envelope,
                    expectedPurpose: .prefs,
                    requestId: projectPrefsID,
                    history: true
                )
            } catch DeviceSecurity.Failure.unavailable { return true }
            if opened.contentEpoch >= boundary { return true }
            try DeviceSecurity.check(
                opened.purpose == DeviceSecurity.Purpose.prefs.rawValue
                    && opened.seq == object.revision
                    && opened.requestID == projectPrefsID,
                .untrusted)
            let prefs = try PBProjectPrefs(serializedBytes: opened.plaintext)
            var put = PBPutAccountPrefsRequest(); put.objectID = projectPrefsID; put.expectedRevision = object.revision
            put.envelope = try await sealEnvelopeOp(
                purpose: .prefs,
                requestId: projectPrefsID,
                seq: object.revision + 1,
                plaintext: prefs.serializedData()
            ).envelope
            return try await api.client.putAccountPrefs(request: put, headers: headers).value().applied
        }
        try await retryRecoveryPublication {
            let listed = try await api.client.listDevices(request: .init(), headers: headers).value()
            guard let own = listed.devices.first(where: { $0.id == account.deviceId }), own.hasMeta else { return true }
            let opened = try await openEnvelopeOp(
                envelope: own.meta,
                expectedPurpose: .deviceMeta,
                requestId: account.deviceId,
                history: true
            )
            if opened.contentEpoch >= boundary { return true }
            try DeviceSecurity.check(
                opened.purpose == DeviceSecurity.Purpose.deviceMeta.rawValue
                    && opened.sender == account.deviceId
                    && opened.seq == own.metaRevision,
                .untrusted)
            let meta = try PBDeviceMeta(serializedBytes: opened.plaintext)
            var put = PBPutDeviceMetaRequest(); put.deviceID = account.deviceId; put.expectedRevision = own.metaRevision
            put.meta = try await sealEnvelopeOp(
                purpose: .deviceMeta,
                requestId: account.deviceId,
                seq: own.metaRevision + 1,
                plaintext: meta.serializedData()
            ).envelope
            return try await api.client.putDeviceMeta(request: put, headers: headers).value().applied
        }
    }
}
