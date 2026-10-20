import Observation
import RACore
import RAProto
import SwiftUI
import UIKit

@MainActor @Observable
final class AuthorityPairingModel {
    var initiation: PairInitiation?
    var candidate: PairCandidate?
    var approved = false
    var delivered = false
    var error = ""
    var busy = false

    static func ownsDelivery(_ operation: SecurityOperation?, pairingID: String) -> Bool {
        guard let operation, operation.kind == .state, !operation.delivery.isEmpty,
            let delivery = try? PBApprovePairingRequest(serializedBytes: operation.delivery)
        else { return false }
        return delivery.pairingID == pairingID
    }
    func start(_ coordinator: AuthorityCoordinator, replacing: String) async {
        guard !busy else { return }
        busy = true; error = ""
        defer { busy = false }
        do {
            if let initiation, !delivered, nowMs() < initiation.expiresMs { try await coordinator.cancelPairing(initiation) }
            initiation = try await coordinator.startPairing(replacesDeviceID: replacing)
            candidate = nil; approved = false; delivered = false
        } catch { self.error = errorText(error) }
    }
    func poll(_ coordinator: AuthorityCoordinator) async {
        guard let initiation, !busy, !delivered else { return }
        busy = true
        defer { busy = false }
        do {
            if approved {
                try await coordinator.resumeSecurityOperation()
                try await coordinator.finishPairingOperation(pairingID: initiation.pairingID)
                delivered = try await coordinator.pendingOperation() == nil
            } else if candidate == nil { candidate = try await coordinator.pairingCandidate(initiation) }
            error = ""
        } catch { self.error = errorText(error) }
    }
    func approve(_ coordinator: AuthorityCoordinator) async {
        guard let candidate, !busy else { return }
        busy = true; error = ""
        defer { busy = false }
        do {
            try await coordinator.approvePairing(candidate, sasConfirmed: true)
            approved = true
            delivered = try await coordinator.pendingOperation() == nil
        } catch {
            // A signed approval may already be published. Continue that receipt.
            approved = Self.ownsDelivery(try? await coordinator.pendingOperation(), pairingID: initiation?.pairingID ?? "")
            self.error = errorText(error)
        }
    }
}

@MainActor
final class AuthorityPairingSessions {
    private var flows: [String: AuthorityPairingModel] = [:]
    func discard(pairingID: String) {
        for flow in flows.values where flow.initiation?.pairingID == pairingID {
            flow.initiation = nil; flow.candidate = nil; flow.approved = false; flow.delivered = false
            flow.error = "配对已放弃，可以重新生成配对码。"
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
                GroupNote("电脑：运行 remote-agent account join，输入下面的配对码。客户端：扫二维码或输入配对码。")
                #if DEBUG
                if app.isDemo { AuthorityDemoPairCode(server: "https://relay.example.com") }
                #endif
                if flow.delivered {
                    GroupBanner("设备已加入。", tone: .plain)
                    Btn("添加另一台设备", kind: .primary, enabled: !flow.busy) {
                        if replacing.isEmpty { perform { await flow.start($0, replacing: "") } }
                        else { nav.open(.addDevice()) }
                    }.padding(16)
                }
                else if let candidate = flow.candidate {
                    GroupLabel("核对图案")
                    Text(candidate.emoji.joined(separator: " "))
                        .font(.system(size: 36)).minimumScaleFactor(0.5).lineLimit(1)
                        .frame(maxWidth: .infinity).padding(16)
                    GroupNote("核对新设备上的 4 个图案。先在新设备确认，再在这里允许。")
                    if !flow.approved {
                        Btn("允许加入", kind: .primary, enabled: !flow.busy) {
                            perform { await flow.approve($0) }
                        }.padding(.horizontal, 16)
                        Btn("不允许", kind: .negative, enabled: !flow.busy) { cancel() }
                            .padding(16)
                    } else { GroupNote("已批准，正在核实设备是否收到。请继续原操作。") }
                } else if let initiation = flow.initiation {
                    VStack(spacing: 12) {
                        QRCodeView(text: initiation.link.url(web: webOrigin(server: context.account.server, preferred: "")), label: "添加设备的配对二维码")
                        Text(String(initiation.link.code.prefix(4)) + " " + String(initiation.link.code.suffix(4))).type(T.code22, Theme.fg).textSelection(.enabled)
                        Text("有效期至 " + securityTime(UInt64(max(initiation.expiresMs, 0)), timeOnly: true))
                            .type(T.meta, Theme.muted)
                        Btn("复制配对码") { UIPasteboard.general.string = initiation.link.code }
                        Btn("取消配对", enabled: !flow.busy) { cancel() }
                    }.padding(16)
                } else if !app.isDemo {
                    Btn("生成配对码", kind: .primary, enabled: !flow.busy) {
                        perform { await flow.start($0, replacing: replacing) }
                    }.padding(16)
                }
                if !flow.error.isEmpty {
                    GroupBanner(flow.error, tone: .error, action: "重试") {
                        perform { c in
                            if flow.initiation == nil { await flow.start(c, replacing: replacing) }
                            else { await flow.poll(c) }
                        }
                    }
                }
                if !flow.approved && !flow.error.isEmpty && flow.initiation != nil {
                    TextAction("重新生成配对码", enabled: !flow.busy) {
                        perform { await flow.start($0, replacing: replacing) }
                    }.padding(.horizontal, 16)
                }
                if !action.error.isEmpty { GroupBanner(action.error, tone: .error) }
            }
        }
        .navBar("添加设备")
        .task {
            // Same in-memory SAS context when returning from another page.
            flow = context.memory.value { AuthorityPairingSessions() }.flow(replacing: replacing)
            guard !app.isDemo else { return }
            while !Task.isCancelled {
                if let c = try? await context.remote.authorityCoordinator() { await flow.poll(c) }
                do { try await Task.sleep(for: .seconds(2)) } catch { return }
            }
        }
    }
    private func perform(_ work: @escaping @MainActor (AuthorityCoordinator) async -> Void) {
        action.run { let c = try await context.remote.authorityCoordinator(); await work(c) }
    }
    private func cancel() {
        action.run {
            guard let initiation = flow.initiation else { return }
            let c = try await context.remote.authorityCoordinator()
            try await c.cancelPairing(initiation)
            flow.initiation = nil; flow.candidate = nil
        }
    }
}
