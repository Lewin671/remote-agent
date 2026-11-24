import Observation
import RACore
import RAProto
import SwiftUI
import UIKit

@MainActor @Observable
final class AuthorityPairingModel {
    var initiation: PBSecurityPairingInfo?
    var approved = false
    var delivered = false
    var error = ""
    var busy = false

    static func ownsDelivery(_ pending: PBSecurityPendingOpSummary?, pairingID: String) -> Bool {
        guard let pending, !pairingID.isEmpty, pending.kind == "state", pending.hasDelivery_p else { return false }
        return pending.deliveryPairingID == pairingID
    }

    func start(_ remote: Remote, replacing: String) async {
        guard !busy else { return }
        busy = true; error = ""
        defer { busy = false }
        do {
            try await remote.ensureSecurityBootstrapped()
            if let initiation, !delivered, nowMs() < initiation.expiresMs {
                try await remote.securityClient.cancelPairing(pairingId: initiation.pairingID)
            }
            initiation = try await remote.securityClient.startPairing(
                webOrigin: webOrigin(server: remote.account.server, preferred: ""),
                replacesDeviceId: replacing
            )
            approved = false; delivered = false
        } catch { self.error = errorText(error) }
    }

    func poll(_ remote: Remote) async {
        guard let initiation, !busy, !delivered else { return }
        busy = true
        defer { busy = false }
        do {
            try await remote.ensureSecurityBootstrapped()
            if approved {
                try await remote.resumeSecurityOperation()
                let res = try await remote.finishPairingDelivery(pairingId: initiation.pairingID)
                self.initiation = res
                if res.delivered {
                    delivered = true
                } else {
                    let st = try await remote.getSecurityStatus()
                    delivered = !st.hasPendingOp
                }
            } else if !initiation.joined {
                self.initiation = try await remote.securityClient.pollPairing(pairingId: initiation.pairingID)
            }
            error = ""
        } catch { self.error = errorText(error) }
    }

    func approve(_ remote: Remote) async {
        guard let initiation, initiation.joined, !busy else { return }
        busy = true; error = ""
        defer { busy = false }
        do {
            try await remote.ensureSecurityBootstrapped()
            let res = try await remote.securityClient.approvePairing(pairingId: initiation.pairingID, sasConfirmed: true)
            self.initiation = res
            approved = true
            if res.delivered {
                delivered = true
            } else {
                let st = try await remote.getSecurityStatus()
                delivered = !st.hasPendingOp
            }
            if delivered { await remote.refreshDevices() }
        } catch {
            // A signed approval may already be published. Continue that receipt.
            let st = try? await remote.getSecurityStatus()
            approved = Self.ownsDelivery(st?.hasPendingOp == true ? st?.pendingOp : nil, pairingID: initiation.pairingID)
            self.error = errorText(error)
        }
    }
}

@MainActor
final class AuthorityPairingSessions {
    private var flows: [String: AuthorityPairingModel] = [:]
    func discard(pairingID: String) {
        for flow in flows.values where flow.initiation?.pairingID == pairingID {
            flow.initiation = nil; flow.approved = false; flow.delivered = false
            flow.error = L10n.securityIosPairingAbandonedYouCanGenerateANewPairingCode()
        }
    }
    func flow(replacing: String) -> AuthorityPairingModel {
        if let flow = flows[replacing] { return flow }
        let flow = AuthorityPairingModel(); flows[replacing] = flow; return flow
    }
}

struct AddDeviceScreen: View {
    var replacing = ""
    @Environment(AppModel.self) private var app
    @Environment(AccountContext.self) private var context
    @Environment(Navigator.self) private var nav
    @State private var flow = AuthorityPairingModel()
    @State private var action = ActionRunner()

    var body: some View {
        let settings = context.core.settings
        let addHint = settings.addDeviceHint.rendered().or(L10n.securityAddOnAuthority(name: settings.authorityName))
        SettingsPage {
            if !settings.isManager { GroupNote(addHint) }
            else {
                GroupNote(L10n.securityComputerRunRemoteAgentAccountJoinAndEnterThe())
                #if DEBUG
                if app.isDemo { AuthorityDemoPairCode(server: "https://relay.example.com") }
                #endif
                if flow.delivered {
                    GroupBanner(L10n.securityIosDeviceJoined(), tone: .plain)
                    Btn(L10n.securityIosAddAnotherDevice(), kind: .primary, enabled: !flow.busy) {
                        if replacing.isEmpty { perform { await flow.start($0, replacing: "") } }
                        else { nav.open(.addDevice()) }
                    }.padding(16)
                }
                else if let initiation = flow.initiation, initiation.joined {
                    GroupLabel(L10n.securityComparePatterns())
                    Text(initiation.sasEmoji.joined(separator: " "))
                        .font(.system(size: 36)).minimumScaleFactor(0.5).lineLimit(1)
                        .frame(maxWidth: .infinity).padding(16)
                    GroupNote(L10n.securityCompareThe4PatternsOnTheNewDeviceConfirm())
                    if !flow.approved {
                        Btn(L10n.securityAllowToJoin(), kind: .primary, enabled: !flow.busy) {
                            perform { await flow.approve($0) }
                        }.padding(.horizontal, 16)
                        Btn(L10n.approvalDeny(), kind: .negative, enabled: !flow.busy) { cancel() }
                            .padding(16)
                    } else { GroupNote(L10n.securityIosApprovedVerifyingDeliveryToTheDeviceContinueTheOriginal()) }
                } else if let initiation = flow.initiation {
                    VStack(spacing: 12) {
                        QRCodeView(text: initiation.url, label: L10n.securityQrCodeForAddingADevice())
                        Text(String(initiation.code.prefix(4)) + " " + String(initiation.code.suffix(4))).font(.title2.monospaced().weight(.semibold)).ink(DS.ink).textSelection(.enabled)
                        Text(L10n.securityPairValidUntil(time: String(describing: securityTime(UInt64(max(initiation.expiresMs, 0)), timeOnly: true))))
                            .font(.footnote).ink(DS.ink2)
                        Btn(L10n.securityIosCopyPairingCode()) { UIPasteboard.general.string = initiation.code }
                        Btn(L10n.securityCancelPairing(), enabled: !flow.busy) { cancel() }
                    }.padding(16)
                } else if !app.isDemo {
                    Btn(L10n.securityGeneratePairingCode(), kind: .primary, enabled: !flow.busy) {
                        perform { await flow.start($0, replacing: replacing) }
                    }.padding(16)
                }
                if !flow.error.isEmpty {
                    GroupBanner(flow.error, tone: .error, action: L10n.commonRetry()) {
                        perform { r in
                            if flow.initiation == nil { await flow.start(r, replacing: replacing) }
                            else { await flow.poll(r) }
                        }
                    }
                }
                if !flow.approved && !flow.error.isEmpty && flow.initiation != nil {
                    TextAction(L10n.securityIosGenerateAnotherPairingCode(), enabled: !flow.busy) {
                        perform { await flow.start($0, replacing: replacing) }
                    }.padding(.horizontal, 16)
                }
                if !action.error.isEmpty { GroupBanner(action.error, tone: .error) }
            }
        }
        .navBar(L10n.homeAddDevice())
        .debugSignal("schedule.pair.start") {
            #if DEBUG
            guard URL(string: context.account.server)?.host == "127.0.0.1" else { return }
            perform { r in
                await flow.start(r, replacing: replacing)
                if let initiation = flow.initiation { ScheduleAcceptance.write("pair-code", initiation.code) }
            }
            #endif
        }
        .debugSignal("schedule.pair.approve") {
            #if DEBUG
            guard URL(string: context.account.server)?.host == "127.0.0.1" else { return }
            perform { r in
                await flow.poll(r)
                if let initiation = flow.initiation, initiation.joined {
                    ScheduleAcceptance.write("pair-sas", initiation.sasEmoji.joined(separator: " "))
                    await flow.approve(r)
                }
            }
            #endif
        }
        .task {
            await context.coreHost.openSettings()
            // Same in-memory SAS context when returning from another page.
            flow = context.memory.value { AuthorityPairingSessions() }.flow(replacing: replacing)
            guard !app.isDemo else { return }
            while !Task.isCancelled {
                await flow.poll(context.remote)
                do { try await Task.sleep(for: .seconds(2)) } catch { return }
            }
        }
    }
    private func perform(_ work: @escaping @MainActor (Remote) async -> Void) {
        action.run { await work(context.remote) }
    }
    private func cancel() {
        action.run {
            guard let initiation = flow.initiation else { return }
            try await context.remote.ensureSecurityBootstrapped()
            try await context.remote.securityClient.cancelPairing(pairingId: initiation.pairingID)
            flow.initiation = nil
        }
    }
}
