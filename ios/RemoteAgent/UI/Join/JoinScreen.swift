// 加入账户, what a device shows until it has an account (mockup 「设置与
// 配对」, Android's ui/Join.kt): the pairing code and the server, scanning
// the other device's QR code, comparing the four pictures, and the account
// The rules are in `JoinModel`; this file draws them.
import RACore
import SwiftUI

/// First run: create, join, or recover an account.
struct JoinScreen: View {
    @Environment(AppModel.self) private var app
    @State private var model: JoinModel?
    @State private var selection: String?
    @State private var setup: AuthoritySetupModel?

    private func begin(_ mode: AuthoritySetupModel.Mode) {
        setup = AuthoritySetupModel(mode: mode, bootstrap: app.authorityBootstrap, server: app.lastServer)
    }
    var body: some View {
        Group {
            if let setup {
                AuthoritySetupScreen(flow: setup, back: { self.setup = nil })
            } else if selection == nil && app.securitySetup == nil {
                Page {
                    ScrollView {
                        VStack(alignment: .leading, spacing: 16) {
                            LargeTitle("加入账户")
                            Btn("新建账户", kind: .primary) { begin(.create) }
                            Btn("加入已有账户") { selection = "join" }
                            Btn("用恢复码找回管理权") { begin(.recover) }
                        }.padding(16).frame(maxWidth: 480).frame(maxWidth: .infinity)
                    }
                }
            } else if let model {
                JoinFlow(model: model, discard: { await app.logout() })
                    .safeAreaInset(edge: .bottom) {
                        if model.step == .form && !model.retainedIdentity {
                            TextAction("返回", alignment: .center) { selection = nil }
                        }
                    }
            } else {
                Page { Color.clear }
            }
        }
        .task {
            guard model == nil else { return }
            if let pending = app.authorityBootstrapPending {
                setup = AuthoritySetupModel(mode: pending.kind == .creation ? .create : .recover,
                    bootstrap: app.authorityBootstrap, server: app.lastServer, status: pending)
                return
            }
            #if DEBUG
                if LaunchDriver.joinStep != nil { selection = "join" }
            #endif
            let app = app
            let made = JoinModel(
                lastServer: app.lastServer, calls: app.joiningCalls,
                retainedIdentity: app.securitySetup != nil
            ) { account in
                try await app.joined(account)
            }
            LaunchDriver.drive(made)
            model = made
        }
    }
}

struct JoinFlow: View {
    @Bindable var model: JoinModel
    var discard: (@MainActor () async -> Void)? = nil

    var body: some View {
        if model.step == .scan {
            QRScanner(onResult: model.scanned, onCancel: model.showForm)
        } else {
            Page {
                ScrollView {
                    VStack(alignment: .leading, spacing: 0) {
                        if model.retainedIdentity {
                            JoinText(
                                text:
                                    "原设备身份、密钥和历史保留。在管理端打开「设置 › 添加设备」，扫码或填配对码后核对四个图案完成绑定。日常使用不需要重复设置。"
                            )
                        }
                        if model.noticeIsError, let discard { SetupDiscardButton(discard: discard) }
                        switch model.step {
                        case .form, .scan: JoinForm(model: model)
                        case .secret: JoinForm(model: model)
                        case .working: Working(model: model)
                        case .emoji(let emoji): Compare(model: model, emoji: emoji)
                        }
                    }
                    // A phone's width on a wide screen: the page is a form, not a table.
                    .frame(maxWidth: 480)
                    .frame(maxWidth: .infinity)
                }
                .scrollDismissesKeyboard(.interactively)
                .scrollBounceBehavior(.basedOnSize)
            }
            .debugSignal("join.cancel") { model.cancel() }
            .debugSignal("join.confirm") { model.confirmPictures() }
        }
    }
}

private struct JoinTitle: View {
    let text: String

    var body: some View {
        Text(text).type(T.title30, Theme.fg)
            .frame(maxWidth: .infinity, alignment: .leading)
            .padding(.init(top: 24, leading: 16, bottom: 6, trailing: 16))
            .accessibilityAddTraits(.isHeader)
    }
}

private struct JoinText: View {
    let text: String

    var body: some View {
        Text(text).type(T.sm, Theme.muted)
            .frame(maxWidth: .infinity, alignment: .leading)
            .padding(.horizontal, 16).padding(.bottom, 16)
    }
}

/// A way to another page of the flow: a line of blue text, as tall as anything else to tap.
private struct JoinLink: View {
    let text: String
    let action: () -> Void

    var body: some View {
        TextAction(text, style: T.sm, alignment: .center, action: action).frame(maxWidth: .infinity)
    }
}

private struct JoinNotice: View {
    let model: JoinModel

    var body: some View {
        if !model.notice.isEmpty {
            Banner(model.notice, tone: model.noticeIsError ? .error : .plain)
                .padding(.horizontal, 16).padding(.bottom, 10)
        }
    }
}

private struct JoinForm: View {
    @Bindable var model: JoinModel
    /// What the code field shows: the model's digits in two groups.
    @State private var typed = ""

    var body: some View {
        JoinTitle(text: model.retainedIdentity ? "一次性安全设置" : "加入账户")
        JoinText(text: "在管理端打开「设置 › 添加设备」，扫它显示的二维码，或把 8 位配对码填在这里。")
        JoinNotice(model: model)
        InputField(
            text: $typed, placeholder: "输入 8 位配对码", look: .code, keyboard: .numberPad,
            submitLabel: .next
        )
        .padding(.horizontal, 16).padding(.bottom, 10)
        // Anything but digits is dropped as it is typed, and the groups are the field's doing.
        .onChange(of: typed) { _, text in
            model.typeCode(text)
            if typed != model.shownCode { typed = model.shownCode }
        }
        .onChange(of: model.code, initial: true) { _, _ in
            if typed != model.shownCode { typed = model.shownCode }
        }
        .accessibilityValue(model.code.map(String.init).joined(separator: " "))
        InputField(
            "服务器", text: $model.server, placeholder: "https://", keyboard: .URL, contentType: .URL,
            submitLabel: .join
        ) {
            if model.canJoin { model.joinWithCode() }
        }
        .padding(.horizontal, 16).padding(.bottom, 10)
        .onChange(of: model.server) { _, text in
            let trimmed = text.trimmingCharacters(in: .whitespacesAndNewlines)
            if trimmed != text { model.server = trimmed }
        }
        Btn("加入", kind: .primary, enabled: model.canJoin, action: model.joinWithCode)
            .padding(.horizontal, 16).padding(.top, 6)
        JoinLink(text: "扫描二维码", action: model.showScanner).padding(.top, 8)
    }
}

private struct Working: View {
    let model: JoinModel

    var body: some View {
        JoinTitle(text: model.retainedIdentity ? "一次性安全设置" : "加入账户")
        IconText(Lucide.loaderCircle, "正在连接…")
            .frame(maxWidth: .infinity, alignment: .leading)
            .padding(.horizontal, 16).padding(.top, 4)
        if model.canCancel {
            Btn("取消", action: model.cancel).padding(.horizontal, 16).padding(.top, 24)
        }
    }
}

/// 核对图案: the four pictures, as large as they can be compared at a glance, and the ways out.
private struct Compare: View {
    let model: JoinModel
    let emoji: [String]
    @ScaledMetric(relativeTo: .largeTitle) private var size: CGFloat = 40

    var body: some View {
        ZStack {
            Text("核对图案").type(T.bodyB, Theme.fg).accessibilityAddTraits(.isHeader)
            HStack {
                Spacer()
                TextAction("取消", style: T.body, alignment: .center, action: model.cancel).padding(
                    .trailing, 8)
            }
        }
        .frame(maxWidth: .infinity, minHeight: Theme.minHit)
        HStack(spacing: 12) {
            ForEach(Array(emoji.enumerated()), id: \.offset) { _, e in
                Text(e).font(.system(size: min(size, 64)))
            }
        }
        .lineLimit(1).minimumScaleFactor(0.5)
        .frame(maxWidth: .infinity)
        .padding(.top, 40).padding(.bottom, 12)
        .accessibilityElement(children: .combine)
        .accessibilityLabel("4 个图案：" + emoji.joined(separator: "，"))
        Text("核对管理端的 4 个图案；一致后先在这里确认，再在管理端上允许。")
            .type(T.sm, Theme.muted).multilineTextAlignment(.center)
            .frame(maxWidth: .infinity).padding(.horizontal, 24)
        if model.comparisonPending {
            Btn("图案一致，信任并继续", kind: .primary, action: model.confirmPictures).padding(
                .horizontal, 16
            ).padding(.top, 16)
        }
        IconText(Lucide.loaderCircle, model.comparisonPending ? "请先在本机确认图案" : "等待管理端批准并完成绑定…")
            .frame(maxWidth: .infinity).padding(.top, 24)
        if !model.notice.isEmpty {
            // Registering has begun: the join can no longer be left.
            Banner(model.notice).padding(.horizontal, 16).padding(.top, 16)
        }
        VStack(spacing: 0) {
            Btn("图案不一致，返回填码", action: model.cancel).disabled(!model.canCancel)
            Hint("返回后停止这次等待；请在管理端拒绝旧请求，再生成新的配对码。")
        }
        .padding(.init(top: 24, leading: 16, bottom: 16, trailing: 16))
    }
}

#if DEBUG
    #Preview("加入账户") {
        JoinFlow(
            model: JoinModel(lastServer: "https://ra.example.com", calls: .live(http: HTTP())) {
                _ in
            })
    }
#endif
