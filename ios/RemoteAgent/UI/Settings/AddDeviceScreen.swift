import Observation
import RACore
import RAProto
import SwiftUI
import UIKit

@MainActor @Observable
final class AuthorityPairingModel {
    static let maxMetaPolls = 15

    var initiation: PBSecurityPairingInfo?
    var approved = false
    var delivered = false
    var selectedRemoveIDs: Set<String> = []
    var error = ""
    var busy = false

    @ObservationIgnored private var removeSelectionTouched = false
    @ObservationIgnored private var metaPolls = 0

    var needsMetaPoll: Bool {
        guard let initiation, initiation.joined, !approved, !delivered else { return false }
        return !initiation.joinerMetaReceived && metaPolls < Self.maxMetaPolls
    }

    var selectedRemoveDeviceIDs: [String] {
        guard let initiation else { return [] }
        return initiation.replacementCandidates
            .map(\.deviceID)
            .filter { selectedRemoveIDs.contains($0) }
    }

    func setRemoveCandidate(_ deviceID: String, selected: Bool) {
        removeSelectionTouched = true
        if selected {
            selectedRemoveIDs.insert(deviceID)
        } else {
            selectedRemoveIDs.remove(deviceID)
        }
    }

    func applyPolled(_ next: PBSecurityPairingInfo) {
        let newlyJoined = initiation?.joined != true && next.joined
        if newlyJoined { metaPolls = 0 }
        initiation = next
        if !next.replacementCandidates.isEmpty && !removeSelectionTouched {
            selectedRemoveIDs = Set(
                next.replacementCandidates.filter(\.defaultSelected).map(\.deviceID)
            )
        }
    }

    func resetState() {
        initiation = nil
        approved = false
        delivered = false
        selectedRemoveIDs = []
        removeSelectionTouched = false
        metaPolls = 0
    }

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
            selectedRemoveIDs = []; removeSelectionTouched = false; metaPolls = 0
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
                let next = try await remote.securityClient.pollPairing(pairingId: initiation.pairingID)
                applyPolled(next)
            } else if !initiation.joinerMetaReceived && metaPolls < Self.maxMetaPolls {
                metaPolls += 1
                let next = try await remote.securityClient.pollPairing(pairingId: initiation.pairingID)
                applyPolled(next)
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
            let res = try await remote.securityClient.approvePairing(
                pairingId: initiation.pairingID,
                sasConfirmed: true,
                removeDeviceIds: selectedRemoveDeviceIDs
            )
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
            flow.resetState()
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
        let access = AuthorityAccess(state: app.state, me: context.me)
        SettingsPage {
            if !access.isAuthority { GroupNote(access.addHint) }
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
                        if !initiation.replacementCandidates.isEmpty {
                            let headerLabel = initiation.hasReplaceOldLabel
                                ? initiation.replaceOldLabel.rendered().or(L10n.securityAlsoRemoveOldDevices())
                                : L10n.securityAlsoRemoveOldDevices()
                            let hintText = initiation.hasReplaceOldHint ? initiation.replaceOldHint.rendered() : ""
                            GroupCard {
                                Text(headerLabel)
                                    .font(.body.weight(.medium))
                                    .ink(DS.ink)
                                    .frame(maxWidth: .infinity, alignment: .leading)
                                    .padding(.horizontal, 14)
                                    .padding(.top, 12)
                                    .padding(.bottom, hintText.isEmpty ? 6 : 4)
                                if !hintText.isEmpty {
                                    Text(hintText)
                                        .font(.subheadline)
                                        .ink(DS.ink2)
                                        .frame(maxWidth: .infinity, alignment: .leading)
                                        .padding(.horizontal, 14)
                                        .padding(.bottom, 8)
                                }
                                ForEach(initiation.replacementCandidates, id: \.deviceID) { cand in
                                    let name = cand.displayName.or(cand.description_p).or(cand.shortID)
                                    let lastActive = cand.hasLastActiveText ? cand.lastActiveText.rendered() : ""
                                    let detail = [
                                        cand.description_p != name ? cand.description_p : "",
                                        cand.shortID,
                                        lastActive,
                                    ].filter { !$0.isEmpty }.joined(separator: " · ")
                                    Toggle(
                                        isOn: Binding(
                                            get: { flow.selectedRemoveIDs.contains(cand.deviceID) },
                                            set: { flow.setRemoveCandidate(cand.deviceID, selected: $0) }
                                        )
                                    ) {
                                        VStack(alignment: .leading, spacing: 2) {
                                            Text(name).font(.subheadline.weight(.semibold)).ink(DS.ink).lineLimit(1)
                                            if !detail.isEmpty {
                                                Text(detail).font(.footnote).ink(DS.ink2).lineLimit(2)
                                            }
                                        }
                                    }
                                    .padding(.horizontal, 14)
                                    .padding(.vertical, 8)
                                }
                            }
                            .padding(.bottom, 12)
                        }
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
            // Same in-memory SAS context when returning from another page.
            flow = context.memory.value { AuthorityPairingSessions() }.flow(replacing: replacing)
            guard !app.isDemo else { return }
            while !Task.isCancelled {
                await flow.poll(context.remote)
                let delaySeconds = flow.needsMetaPoll ? 1 : 2
                do { try await Task.sleep(for: .seconds(delaySeconds)) } catch { return }
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
            flow.resetState()
        }
    }
}
