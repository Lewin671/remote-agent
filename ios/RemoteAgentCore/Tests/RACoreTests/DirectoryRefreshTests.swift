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
