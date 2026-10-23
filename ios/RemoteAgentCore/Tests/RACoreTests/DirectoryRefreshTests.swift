import Connect
import Foundation
import RAProto
import Testing

@testable import RACore

private extension Remote {
    func pendDirectoryMaintenance() { recoveryObjectsPending = true }
}

extension ServerTests {
    @Suite struct DirectoryRefreshTests {
        private func world(_ body: (World) async throws -> Void) async throws {
            try await World.run(setUp: { w in
                try await w.daemon.putMachine("test-machine")
                try await w.daemon.putSession("s1", session("/test-project", "test session"))
            }, body)
        }

        @Test(arguments: ["machine", "session"])
        func knownMachineUpdatesSyncSecurityWithoutReloadingDirectory(kind: String) async throws {
            try await world { w in
                let lists = Counter(), prefs = Counter(), pulls = Counter()
                let http = Intercept.http(for: w.server.url) { request in
                    let path = request.url?.path ?? ""
                    if path.hasSuffix("/ListDevices") { lists.increment() }
                    if path.hasSuffix("/GetAccountPrefs") { prefs.increment() }
                    if path.hasSuffix("/GetSecurityStates") { pulls.increment() }
                    return request
                }
                let r = try w.remote(try await w.join(), http: http)
                try await r.refreshTrustedDirectory()
                let token = try await r.currentToken()
                let machine = try #require(try await r.api.client.listMachines(request: .init(),
                    headers: Api.headers(token: token)).value().machines.first)
                var envelopes = [machine.meta]
                if kind == "session" {
                    let session = try #require(try await r.api.client.listRemoteSessions(request: .init(),
                        headers: Api.headers(token: token)).value().sessions.first)
                    envelopes = [session.key, session.meta]
                }
                let before = (lists.value, prefs.value, pulls.value)
                for _ in 0..<3 { try await r.checkDirectory(machine.id, envelopes: envelopes) }
                #expect(lists.value == before.0)
                #expect(prefs.value == before.1)
                #expect(pulls.value >= before.2 + 3)
            }
        }

        @Test(arguments: ["sender", "epoch", "malformed", "member", "maintenance", "head"])
        func uncertainUpdatesReloadDirectory(reason: String) async throws {
            try await world { w in
                let lists = Counter()
                let http = Intercept.http(for: w.server.url) { request in
                    if request.url?.path.hasSuffix("/ListDevices") == true { lists.increment() }
                    return request
                }
                let r = try w.remote(try await w.join(), http: http)
                let other = try await w.join()
                try await r.refreshTrustedDirectory()
                let token = try await r.currentToken()
                var machine = try #require(try await r.api.client.listMachines(request: .init(),
                    headers: Api.headers(token: token)).value().machines.first)
                switch reason {
                case "sender", "epoch":
                    var header = try DeviceSecurity.parseHeader(machine.meta.securityHeader)
                    if reason == "sender" { header.sender = r.account.deviceId } else { header.epoch += 100 }
                    machine.meta.securityHeader = try DeviceSecurity.headerBytes(header)
                case "malformed": machine.meta.securityHeader = Data([0])
                case "member": machine.id = "unknown-machine"
                case "maintenance": await r.pendDirectoryMaintenance()
                case "head": try await w.phone.remove([other.deviceId])
                default: break
                }
                let before = lists.value
                try await r.checkDirectory(machine.id, envelopes: [machine.meta])
                #expect(lists.value == before + 1)
                if reason == "head" { #expect(r.current.devices[other.deviceId] == nil) }
            }
        }

        @Test func pendingSecurityReceiptRequiresAFullRead() async throws {
            try await world { w in
                let account = try await w.join()
                let vault = AuthorityTestVault()
                let source = try #require(TestSecurity.find(w.server.url)?.store(account.deviceId))
                try vault.initializeSecurity(source.readSecurity())
                let lists = Counter()
                let http = Intercept.http(for: w.server.url) { request in
                    if request.url?.path.hasSuffix("/ListDevices") == true { lists.increment() }
                    return request
                }
                let r = try Remote(account: account, http: http, securityStorage: vault)
                defer { r.close() }
                try await r.refreshTrustedDirectory()
                let token = try await r.currentToken()
                let machine = try #require(try await r.api.client.listMachines(request: .init(),
                    headers: Api.headers(token: token)).value().machines.first)
                _ = try vault.prepareOperation(expectedHead: nil) { _ in
                    SecurityOperation(kind: .lease, parent: Data(), signed: Data())
                }
                let before = lists.value
                try await r.checkDirectory(machine.id, envelopes: [machine.meta])
                #expect(lists.value == before + 1)
            }
        }

        @Test func overlappingDirectoryLoadsShareOneRead() async throws {
            try await world { w in
                let lists = Counter()
                let entered = Signal(), release = Signal(), secondSync = Signal()
                w.onClose { release.set() }
                let http = Intercept.http(for: w.server.url, answered: { request in
                    if request.url?.path.hasSuffix("/ListDevices") == true {
                        entered.set()
                        await release.wait()
                    }
                }) { request in
                    if request.url?.path.hasSuffix("/ListDevices") == true { lists.increment() }
                    if entered.isSet && request.url?.path.hasSuffix("/GetSecurityStates") == true { secondSync.set() }
                    return request
                }
                let r = try w.remote(try await w.join(), http: http)
                let first = Task { try await r.refreshTrustedDirectory() }
                #expect(await entered.wait())
                let second = Task { try await r.refreshTrustedDirectory() }
                #expect(await secondSync.wait())
                release.set()
                try await first.value
                try await second.value
                #expect(lists.value == 1)
                // Self-description can refresh again after the shared read is cleared.
                try await r.describeSelf(platform: "test", description: "test client")
                #expect(r.current.devices[r.account.deviceId]?.meta?.platform == "test")
            }
        }

        @Test(arguments: ["device", "machine"])
        func metadataPushAfterDirectoryResponseRequiresAFreshRead(kind: String) async throws {
            try await world { w in
                let writer = try await w.client()
                let deviceID = writer.account.deviceId
                try await writer.changeDeviceMeta(deviceID) { $0.name = "old name" }
                let hold = Signal(), entered = Signal(), release = Signal(), updateSync = Signal()
                let lists = Counter()
                w.onClose { release.set() }
                let http = Intercept.http(for: w.server.url, answered: { request in
                    if hold.isSet && !entered.isSet && request.url?.path.hasSuffix("/ListDevices") == true {
                        entered.set()
                        await release.wait()
                    }
                }) { request in
                    if request.url?.path.hasSuffix("/ListDevices") == true { lists.increment() }
                    if entered.isSet && request.url?.path.hasSuffix("/GetSecurityStates") == true { updateSync.set() }
                    return request
                }
                let r = try w.remote(try await w.join(), http: http)
                try await r.refreshTrustedDirectory()
                let head = await r.directoryHead
                hold.set()
                let reading = Task { try await r.refreshTrustedDirectory() }
                #expect(await entered.wait())
                let readsWhileHeld = lists.value
                if kind == "device" {
                    try await writer.changeDeviceMeta(deviceID) {
                        $0.name = "new name"
                        $0.description_p = "new description"
                    }
                } else { try await w.daemon.putMachine("new-machine-name") }
                let headers = Api.headers(token: try await writer.currentToken())
                let device = try #require(try await writer.api.client.listDevices(request: .init(), headers: headers)
                    .value().devices.first { $0.id == deviceID })
                let machine = try #require(try await writer.api.client.listMachines(request: .init(), headers: headers)
                    .value().machines.first)
                // Feed the relay's signed update to the same handlers as the stream.
                let updating = Task {
                    if kind == "device" { await r.putDevice(device) }
                    else { await r.machineUpdated(machine) }
                }
                #expect(await updateSync.wait())
                release.set()
                try await reading.value
                await updating.value
                if kind == "device" {
                    #expect(r.current.devices[deviceID]?.meta?.name == "new name")
                    #expect(r.current.devices[deviceID]?.meta?.description_p == "new description")
                    #expect(r.current.devices[deviceID]?.metaRev == device.metaRevision)
                } else { #expect(r.current.machines[machine.id]?.meta?.hostname == "new-machine-name") }
                #expect(lists.value > readsWhileHeld)
                #expect(await r.directoryHead == head)
            }
        }

        private func publishPrefs(_ w: World, revision: UInt64, path: String) async throws -> PBAccountPrefsObject {
            var header = PBHeader()
            header.kind = .accountPrefs
            header.requestID = projectPrefsID
            header.seq = revision
            var prefs = PBProjectPrefs()
            prefs.schema = 1
            prefs.pinned = [.with { $0.path = path }]
            var request = PBPutAccountPrefsRequest()
            request.objectID = projectPrefsID
            request.expectedRevision = revision - 1
            request.envelope = try w.phone.fixture.seal(w.phone.account, header, prefs)
            let result = try await w.phone.api.client.putAccountPrefs(request: request,
                headers: Api.headers(token: w.phone.token)).value()
            #expect(result.applied)
            return result.object
        }

        @Test(arguments: [false, true])
        func prefsPushAfterDirectoryResponseRequiresAFreshRead(existing: Bool) async throws {
            try await world { w in
                if existing { _ = try await publishPrefs(w, revision: 1, path: "/old-project") }
                let hold = Signal(), entered = Signal(), release = Signal(), updateSync = Signal()
                let reads = Counter()
                w.onClose { release.set() }
                let http = Intercept.http(for: w.server.url, answered: { request in
                    if hold.isSet && !entered.isSet && request.url?.path.hasSuffix("/GetAccountPrefs") == true {
                        entered.set()
                        await release.wait()
                    }
                }) { request in
                    if request.url?.path.hasSuffix("/GetAccountPrefs") == true { reads.increment() }
                    if entered.isSet && request.url?.path.hasSuffix("/GetSecurityStates") == true { updateSync.set() }
                    return request
                }
                let r = try w.remote(try await w.join(), http: http)
                try await r.refreshTrustedDirectory()
                try await r.readTrustedPrefs()
                let head = await r.directoryHead
                hold.set()
                let reading = Task { try await r.refreshTrustedDirectory() }
                #expect(await entered.wait())
                let readsWhileHeld = reads.value
                let object = try await publishPrefs(w, revision: existing ? 2 : 1, path: "/updated-project")
                let updating = Task { await r.putPrefs(object) }
                #expect(await updateSync.wait())
                release.set()
                try await reading.value
                await updating.value
                #expect(r.current.prefs?.revision == object.revision)
                #expect(r.current.prefs?.pinned == ["/updated-project"])
                let metadata = try #require(await r.trustedMetadata["prefs"])
                #expect(try DeviceSecurity.parseHeader(metadata.securityHeader).seq == object.revision)
                #expect(reads.value > readsWhileHeld)
                #expect(await r.directoryHead == head)
            }
        }

        @Test func machineRemovalInvalidatesAnOlderDirectoryResponseWithoutChangingHead() async throws {
            try await world { w in
                try await w.phone.remove([w.daemon.id])
                let hold = Signal(), entered = Signal(), release = Signal(), updateSync = Signal()
                let reads = Counter()
                w.onClose { release.set() }
                let http = Intercept.http(for: w.server.url, answered: { request in
                    if hold.isSet && !entered.isSet && request.url?.path.hasSuffix("/ListMachines") == true {
                        entered.set()
                        await release.wait()
                    }
                }) { request in
                    if request.url?.path.hasSuffix("/ListDevices") == true { reads.increment() }
                    if entered.isSet && request.url?.path.hasSuffix("/GetSecurityStates") == true { updateSync.set() }
                    return request
                }
                let r = try w.remote(try await w.join(), http: http)
                #expect(await r.refresh())
                #expect(r.current.machines[w.daemon.id] != nil)
                let head = await r.directoryHead
                hold.set()
                let reading = Task { try await r.refreshTrustedDirectory() }
                #expect(await entered.wait())
                let readsWhileHeld = reads.value
                var deletion = PBDeleteMachineRecordsRequest()
                deletion.machineID = w.daemon.id
                _ = try await w.phone.api.client.deleteMachineRecords(request: deletion,
                    headers: Api.headers(token: w.phone.token)).value()
                let updating = Task { await r.machineRemoved() }
                #expect(await updateSync.wait())
                release.set()
                try await reading.value
                await updating.value
                #expect(r.current.machines[w.daemon.id] == nil)
                #expect(r.current.sessions["s1"] == nil)
                #expect(await r.trustedMetadata["machine:" + w.daemon.id] == nil)
                #expect(reads.value > readsWhileHeld)
                #expect(await r.directoryHead == head)
            }
        }

        @Test func deviceRemovalCannotJoinASecuritySyncThatPredatesTheNotification() async throws {
            try await world { w in
                let victim = try await w.join()
                let holdDirectory = Signal(), directoryEntered = Signal(), releaseDirectory = Signal()
                let holdSecurity = Signal(), securityEntered = Signal(), releaseSecurity = Signal()
                w.onClose { releaseDirectory.set(); releaseSecurity.set() }
                let http = Intercept.http(for: w.server.url, answered: { request in
                    let path = request.url?.path ?? ""
                    if holdDirectory.isSet && !directoryEntered.isSet && path.hasSuffix("/ListDevices") {
                        directoryEntered.set()
                        await releaseDirectory.wait()
                    }
                    if holdSecurity.isSet && !securityEntered.isSet && path.hasSuffix("/GetSecurityStates") {
                        securityEntered.set()
                        await releaseSecurity.wait()
                    }
                }) { $0 }
                let r = try w.remote(try await w.join(), http: http)
                try await r.refreshTrustedDirectory()
                #expect(r.current.devices[victim.deviceId] != nil)
                let head = await r.directoryHead
                holdDirectory.set()
                let reading = Task { try await r.refreshTrustedDirectory() }
                #expect(await directoryEntered.wait())
                holdSecurity.set()
                let security = try await r.securitySession()
                let syncing = Task { try await security.sync() }
                #expect(await securityEntered.wait())
                try await w.phone.remove([victim.deviceId])
                let updating = Task { await r.deviceRemoved(victim.deviceId) }
                try await eventually("removal notification received") { await r.devicePushes[victim.deviceId] == 1 }
                releaseSecurity.set()
                _ = try await syncing.value
                releaseDirectory.set()
                try await reading.value
                await updating.value
                #expect(r.current.devices[victim.deviceId] == nil)
                #expect(await r.directoryHead != head)
            }
        }

        @Test func olderDirectoryReadCannotUndoNewerMachineMetadata() async throws {
            try await world { w in
                let hold = Signal(), entered = Signal(), release = Signal()
                w.onClose { release.set() }
                let http = Intercept.http(for: w.server.url, answered: { request in
                    if hold.isSet && !entered.isSet && request.url?.path.hasSuffix("/ListMachines") == true {
                        entered.set()
                        await release.wait()
                    }
                }) { $0 }
                let r = try w.remote(try await w.join(), http: http)
                try await r.refreshTrustedDirectory()
                hold.set()
                let load = Task { try await r.refreshTrustedDirectory() }
                #expect(await entered.wait())
                try await w.daemon.putMachine("newer-machine-name")
                let token = try await r.currentToken()
                let machine = try #require(try await r.api.client.listMachines(request: .init(),
                    headers: Api.headers(token: token)).value().machines.first)
                await r.putMachine(machine, save: false)
                let revision = try #require(r.current.machines[machine.id]).metaRev
                release.set()
                try await load.value
                #expect(r.current.machines[machine.id]?.meta?.hostname == "newer-machine-name")
                #expect(r.current.machines[machine.id]?.metaRev == revision)
                let retained = try #require(await r.trustedMetadata["machine:" + machine.id])
                #expect(try DeviceSecurity.parseHeader(retained.securityHeader).seq == revision)
            }
        }
    }
}
