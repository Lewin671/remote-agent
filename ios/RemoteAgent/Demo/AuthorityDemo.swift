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
    static var code: String { "TEST-RECOVERY-CODE-DEMO" }
    static func apply(_ world: inout DemoWorld) {
        world.authorityDeviceId = ["authority", "pending", "outgoing", "pair", "rotate"].contains(mode ?? "") ? Demo.me : other
        world.leaseEndMs = nowMs() + 48 * 3_600_000
        if mode == "pending" { world.recoveryPending = true; world.recoveryReadyMs = nowMs() + 24 * 3_600_000 }
        if mode == "outgoing" {
            world.devices[other]?.meta.handoffRecoveryEd = Data(repeating: 4, count: 32)
        }
        if mode == "machine" {
            world.devices[Demo.me]?.kind = .machine
            world.authorityDeviceId = Demo.me // Even this malformed role combination exposes no management controls.
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
    private var qrPayload: String {
        let commit = Data(repeating: 5, count: 32).base64URL
        let encodedServer = server.addingPercentEncoding(withAllowedCharacters: .urlQueryAllowed) ?? server
        return "https://app.example.com/#/pair?code=12345678&c=" + commit + "&s=" + encodedServer
    }
    var body: some View {
        VStack(spacing: 12) {
            QRCodeView(text: qrPayload, label: L10n.securityQrCodeForAddingADevice())
            Text("1234 5678").font(.title2.monospaced().weight(.semibold)).ink(DS.ink)
            Text("有效期至 " + securityTime(UInt64(nowMs() + 5 * 60_000), timeOnly: true)).font(.footnote).ink(DS.ink2)
            Btn("复制配对码") {}
            Btn("取消配对") {}
        }.padding(16)
    }
}
#endif
