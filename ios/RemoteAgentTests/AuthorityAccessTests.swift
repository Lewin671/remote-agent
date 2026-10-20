import Foundation
import RACore
import RAProto
import Testing
@testable import RemoteAgent

@MainActor @Suite struct AuthorityAccessTests {
    @Test func rolesFollowTheDirectoryRatherThanPlatformMetadata() {
        for platform in ["ios", "web", "android", "", "unknown"] {
            var state = RemoteState()
            state.authorityDeviceId = "authority"
            state.devices["authority"] = DeviceView(id: "authority", kind: .client, createdMs: 1,
                meta: .with { $0.platform = platform; $0.name = "Studio" })
            state.devices["member"] = DeviceView(id: "member", kind: .client, createdMs: 1)
            #expect(AuthorityAccess(state: state, me: "authority").isAuthority)
            #expect(!AuthorityAccess(state: state, me: "member").isAuthority)
            #expect(AuthorityAccess(state: state, me: "member").addHint == "要添加电脑或客户端，在管理端（Studio）上打开「设置 › 添加设备」。")
            #expect(AuthorityAccess(state: state, me: "member").emptyHint == "还没有电脑。在管理端（Studio）上打开「设置 › 添加设备」，把电脑加进来。")
            state.devices["authority"]!.kind = .machine
            #expect(!AuthorityAccess(state: state, me: "authority").isAuthority)
            #expect(!AuthorityAccess(state: state, me: "authority").isClient)
            state.devices["authority"]!.kind = .client; state.removed = true
            #expect(!AuthorityAccess(state: state, me: "authority").isAuthority)
        }
    }
    @Test func logoutPrioritizesUnresolvedEvidenceAndWarnsTheOnlyAuthority() {
        #expect(AuthorityAccess.exitAdvice(authority: true, unresolved: true) == "安全操作结果尚未核实，请先继续原操作；退出会删除唯一完成材料。")
        #expect(AuthorityAccess.exitAdvice(authority: true, unresolved: false).contains("唯一管理端"))
        #expect(AuthorityAccess.exitAdvice(authority: true, unresolved: false).contains("没有恢复码备份将丢失管理权"))
        #expect(AuthorityAccess.exitAdvice(authority: false, unresolved: false) == "退出会删除本机密钥，需要重新配对。")
    }
    @Test func unrelatedPendingCannotBecomeAPairingSuccessAfterRetry() throws {
        let lease = SecurityOperation(kind: .lease, parent: Data(count: 32), signed: Data())
        #expect(!AuthorityPairingModel.ownsDelivery(lease, pairingID: "pair-one"))
        #expect(!AuthorityPairingModel.ownsDelivery(nil, pairingID: "pair-one"))
        var request = PBApprovePairingRequest(); request.pairingID = "pair-one"
        let pair = SecurityOperation(kind: .state, parent: Data(count: 32), signed: Data(), delivery: try request.serializedData())
        #expect(AuthorityPairingModel.ownsDelivery(pair, pairingID: "pair-one"))
        #expect(!AuthorityPairingModel.ownsDelivery(pair, pairingID: "pair-two"))
        let malformed = SecurityOperation(kind: .state, parent: Data(count: 32), signed: Data(), delivery: Data([255]))
        #expect(!AuthorityPairingModel.ownsDelivery(malformed, pairingID: "pair-one"))
    }

}
