import Connect
import Foundation
import RAProto

extension Remote {
    /// The stored envelope's epoch is the resumable republish marker. Only a
    /// retained device that can verify and read it may publish the snapshot.
    func republishRecoveryObjects(_ record: DeviceSecurity.Record) async throws {
        guard let epoch = record.recovery.keys.max(),
              let boundary = record.states[epoch]?.core.contentEpoch else { return }
        let security = try securitySession()
        let headers = Api.headers(token: try await currentToken())
        try await retryRecoveryPublication {
            var get = PBGetAccountPrefsRequest(); get.objectID = projectPrefsID
            let object = try await api.client.getAccountPrefs(request: get, headers: headers).value().object
            if !object.hasEnvelope || object.revision == 0 { return true }
            let h = try DeviceSecurity.parseHeader(object.envelope.securityHeader)
            if h.contentEpoch >= boundary { return true }
            let snapshot: (DeviceSecurity.Header, PBProjectPrefs)
            do { snapshot = try await security.openMessage(object.envelope, as: PBProjectPrefs.self, history: true) }
            catch DeviceSecurity.Failure.unavailable { return true }
            try DeviceSecurity.check(
                snapshot.0.purpose == .prefs && snapshot.0.seq == object.revision && snapshot.0.request == projectPrefsID,
                .untrusted)
            var put = PBPutAccountPrefsRequest(); put.objectID = projectPrefsID; put.expectedRevision = object.revision
            put.envelope = try await security.seal(
                purpose: .prefs, request: projectPrefsID, seq: object.revision + 1,
                plaintext: snapshot.1.serializedData())
            return try await api.client.putAccountPrefs(request: put, headers: headers).value().applied
        }
        try await retryRecoveryPublication {
            let listed = try await api.client.listDevices(request: .init(), headers: headers).value()
            guard let own = listed.devices.first(where: { $0.id == account.deviceId }), own.hasMeta else { return true }
            let h = try DeviceSecurity.parseHeader(own.meta.securityHeader)
            if h.contentEpoch >= boundary { return true }
            let snapshot = try await security.openMessage(own.meta, as: PBDeviceMeta.self, history: true)
            try DeviceSecurity.check(
                snapshot.0.purpose == .deviceMeta && snapshot.0.sender == account.deviceId && snapshot.0.seq == own.metaRevision,
                .untrusted)
            var put = PBPutDeviceMetaRequest(); put.deviceID = account.deviceId; put.expectedRevision = own.metaRevision
            put.meta = try await security.seal(
                purpose: .deviceMeta, request: account.deviceId, seq: own.metaRevision + 1,
                plaintext: snapshot.1.serializedData())
            return try await api.client.putDeviceMeta(request: put, headers: headers).value().applied
        }
    }
}
