import Connect
import Foundation
import RAProto

extension Remote {
    func refreshTrustedDirectory() async throws {
        directoryVersion += 1
        let version = directoryVersion
        let catalog = try await securitySession().catalog()
        let hash = try DeviceSecurity.stateHash(catalog.state)
        let hints = try await api.client.listDevices(
            request: .init(), headers: Api.headers(token: try await currentToken())
        ).value()
        var members: [String: DeviceSecurity.Member] = [:]
        var metadata: [String: PBEnvelope] = [:]
        var nextDevices: [String: DeviceView] = [:]
        var nextMachines = machines
        var bindings: [String: (state: Data, commit: Data)] = [:]
        for wire in catalog.entries {
            let h = try DeviceSecurity.parseHeader(wire.formed.header)
            let key: String
            switch h.purpose {
            case .machineMeta: key = "machine:" + h.machine
            case .deviceMeta: key = "device:" + h.request
            case .sessionMeta: key = "session:" + h.machine + ":" + h.session
            case .prefs: key = "prefs"
            default: continue
            }
            metadata[key] = try DeviceSecurity.envelope(wire)
        }
        for member in catalog.state.core.members {
            members[member.id] = member
            let hint = hints.devices.first(where: { $0.id == member.id })
            var view = DeviceView(
                id: member.id, kind: PBDeviceKind(rawValue: Int(member.kind))!,
                createdMs: hint?.createdMs ?? 0, lastSeenMs: hint?.lastSeenMs ?? 0)
            let env = metadata[(member.kind == 1 ? "machine:" : "device:") + member.id]
            let binding: Data
            if let env {
                binding = try DeviceSecurity.commitment(DeviceSecurity.fromEnvelope(env).formed)
                if member.kind == 2 {
                    let (h, meta) = try await openSecureMessage(
                        envelope: env, want: Place(.deviceMeta, requestId: member.id),
                        as: PBDeviceMeta.self)
                    view.meta = meta
                    view.metaRev = h.seq
                } else {
                    let (h, meta) = try await openSecureMessage(
                        envelope: env, want: Place(.machineMeta, machineId: member.id),
                        as: PBMachineMeta.self)
                    var machine =
                        machines[member.id]
                        ?? MachineView(id: member.id, online: false, lastSeenMs: 0)
                    machine.meta = meta
                    machine.metaRev = h.seq
                    nextMachines[member.id] = machine
                }
            } else {
                binding = try DeviceSecurity.unnamedMemberCommit(catalog.state, member: member)
                if member.kind == 1 && nextMachines[member.id] == nil {
                    nextMachines[member.id] = MachineView(
                        id: member.id, online: false, lastSeenMs: 0)
                }
            }
            nextDevices[member.id] = view
            bindings[member.id] = (hash, binding)
        }
        guard !closed, version == directoryVersion else { return }
        let installed = try await securitySession().identity().1
        guard try DeviceSecurity.stateHash(installed) == hash else {
            throw DeviceSecurity.Failure.stale
        }
        for (id, device) in nextDevices where device.kind == .client {
            if let old = devices[id], old.metaRev > device.metaRev {
                throw DeviceSecurity.Failure.stale
            }
        }
        for old in Array(trustedMembers.values) where old.kind == 1 && members[old.id] == nil {
            await dropMachine(old.id)
        }
        trustedMembers = members
        trustedMetadata = metadata
        deviceBindings = bindings
        devices = nextDevices
        authorityDeviceId = catalog.state.core.authorityID
        machines = nextMachines.filter { members[$0.key]?.kind == 1 }
        for session in Array(sessions.values) where members[session.machineId]?.kind != 1 {
            sessions[session.id] = nil
            sessionKeys[session.id] = nil
            listed.remove(session.id)
        }
        publish()
    }
    func startRemovalPolling() {
        guard removalJob == nil, !closed else { return }
        removalJob = Task { [weak self] in
            guard let self else { return }
            while !Task.isCancelled {
                await self.pollRemovals()
                if await self.removalPollingFinished() { break }
                do { try await Task.sleep(for: .seconds(2)) } catch { break }
            }
            await self.finishRemovalPolling()
        }
    }
    private func removalPollingFinished() -> Bool {
        closed || !running || removals.allSatisfy { $0.phase == "complete" && $0.serverCleaned }
    }
    private func finishRemovalPolling() { removalJob = nil }
    private func cleanupRemoval(_ op: DeviceSecurityClient.Removal) async
        -> DeviceSecurityClient.Removal
    {
        guard op.phase == "complete", !op.serverCleaned, let completion = op.completion else {
            return op
        }
        do {
            for target in op.targets {
                var request = PBRemoveDeviceRequest()
                request.deviceID = target
                request.securityCompletion = completion
                request.securityRequest = op.request
                request.securityChain = op.chain
                do {
                    _ = try await api.client.removeDevice(
                        request: request, headers: Api.headers(token: try await currentToken())
                    ).value()
                } catch let e as ConnectError { if e.code != .notFound { throw e } }
            }
            return try await securitySession().markServerCleaned(op)
        } catch { return op }
    }
    func pollRemovals() async {
        do {
            let client = try securitySession()
            let saved = try await client.removalOperations()
            var next: [DeviceSecurityClient.Removal] = []
            for op in saved {
                if op.phase == "complete" {
                    next.append(await cleanupRemoval(op))
                    continue
                }
                do { next.append(await cleanupRemoval(try await client.pollRemoval(op))) } catch {
                    if op.phase == "unconfirmed" {
                        next.append((try? await client.retryRemoval(op)) ?? op)
                    } else {
                        next.append(op)
                    }
                }
            }
            if !closed {
                removals = next
                publish()
            }
        } catch { report(error) }
    }
}
