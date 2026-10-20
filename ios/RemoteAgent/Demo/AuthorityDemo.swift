#if DEBUG
import Foundation
import RACore
import RAProto
import SwiftUI

/// Synthetic UI-only states. None of these paths read a real account or make an RPC.
enum AuthorityDemo {
    static let other = "b71c00000000b71c"
    static var mode: String? {
        let args = ProcessInfo.processInfo.arguments
        guard let i = args.firstIndex(of: "-authorityDemo"), i + 1 < args.count else { return nil }
        return args[i + 1]
    }
    static var code: String { try! RecoveryCode.encode(seed: Data(repeating: 7, count: 32), genesis: Data(repeating: 8, count: 32)) }
    static func apply(_ state: inout RemoteState) {
        state.authorityDeviceId = ["authority", "pending", "outgoing", "pair", "rotate"].contains(mode ?? "") ? Demo.me : other
        state.recoveryStatusStale = false
        state.leaseEndMs = UInt64(nowMs() + 48 * 3_600_000)
        if mode == "pending" { state.recoveryPending = true; state.recoveryReadyMs = UInt64(nowMs() + 24 * 3_600_000) }
        if mode == "outgoing" {
            state.devices[other]?.meta?.handoffRecoveryEd = Data(repeating: 4, count: 32)
        }
        if mode == "machine" {
            state.devices[Demo.me]?.kind = .machine
            state.authorityDeviceId = Demo.me // Even this malformed role combination exposes no management controls.
        }
    }
}

struct AuthorityDemoCode: View {
    let handoff: Bool
    @State private var saved = false
    var body: some View {
        RecoveryCodeFigure(code: AuthorityDemo.code, saved: $saved)
        Btn(handoff ? "我已保存，通知管理端" : "我已保存，发布换码", kind: .primary, enabled: saved) {}
            .padding(.horizontal, 16)
    }
}

struct AuthorityDemoPairCode: View {
    let server: String
    var body: some View {
        VStack(spacing: 12) {
            QRCodeView(text: PairLink(code: "12345678", server: server, commit: Data(repeating: 5, count: 32))
                .url(web: "https://app.example.com"), label: "添加设备的配对二维码")
            Text("1234 5678").type(T.code22, Theme.fg)
            Text("有效期至 " + securityTime(UInt64(nowMs() + 5 * 60_000), timeOnly: true)).type(T.meta, Theme.muted)
            Btn("复制配对码") {}
            Btn("取消配对") {}
        }.padding(16)
    }
}
#endif
