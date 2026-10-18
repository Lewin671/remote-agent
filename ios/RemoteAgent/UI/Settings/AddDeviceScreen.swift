import RACore
import SwiftUI

/// 添加设备: show a code and QR, compare the four pictures, then hand over
/// the root key (mockup 「设置与配对」, Android's PairScreen). The steps are in
/// `AddDeviceModel`.
struct AddDeviceScreen: View {
    @Environment(AppModel.self) private var app
    @Environment(AccountContext.self) private var context
    @Environment(Navigator.self) private var nav
    @State private var model: AddDeviceModel?

    var body: some View {
        Page {
            ScrollView {
                VStack(spacing: 0) {
                    ConnectionBar()
                    if let model {
                        AddDeviceSteps(
                            model: model,
                            web: webOrigin(server: context.account.server, preferred: app.settings.settings.webOrigin),
                            onDone: nav.back)
                    }
                }
            }
            .scrollBounceBehavior(.basedOnSize)
        }
        .navBar("添加设备")
        .task {
            guard model == nil else { return }
            let remote = context.remote
            let made = AddDeviceModel { try await Initiation.start(remote: remote) }
            model = made
            if !LaunchDriver.preview(made) { made.start() }
        }
        // Left before the end: the code stops working at once, not in five minutes.
        .onDisappear { model?.leave() }
        .debugSignal("pair.approve") { model?.approve() }
        .debugSignal("pair.reject") {
            model?.leave()
            nav.back()
        }
    }
}

struct AddDeviceSteps: View {
    let model: AddDeviceModel
    let web: String
    let onDone: () -> Void

    var body: some View {
        VStack(spacing: 0) {
            switch model.phase {
            case .starting:
                LoadingRow()
            case .waiting(let code, let link):
                QRCodeView(text: link.url(web: web), label: "配对二维码")
                    .padding(.top, 14).padding(.bottom, 10)
                Text(code.prefix(4) + " " + code.dropFirst(4))
                    .type(T.code22.with(lineHeight: 1.2), Theme.fg)
                    .lineLimit(1).minimumScaleFactor(0.6)
                    .accessibilityLabel("配对码 " + code.map(String.init).joined(separator: " "))
                    .textSelection(.enabled)
                Text("在新设备上扫码，或输入这 8 位配对码。5 分钟内有效。")
                    .type(T.sm, Theme.muted).multilineTextAlignment(.center).padding(.top, 10)
                IconText(Lucide.loaderCircle, "等待新设备…").padding(.top, 10)
            case .compare(let emoji, let machine):
                Request(model: model, emoji: emoji, machine: machine, sending: false, onDone: onDone)
            case .sending(let emoji, let machine):
                Request(model: model, emoji: emoji, machine: machine, sending: true, onDone: onDone)
            case .done:
                Banner("新设备已加入", action: "完成", onAction: onDone, icon: Lucide.check).padding(.top, 14)
            case .failed(let why, let uncertain):
                Banner(why, tone: uncertain ? .warn : .error, action: model.canRetryApproval ? "重试" : "换一个", onAction: model.retry).padding(.top, 14)
            }
        }
        .padding(.horizontal, Theme.Space.page)
        .frame(maxWidth: 480)
        .frame(maxWidth: .infinity)
    }
}

/// A device asking to join: who, its four pictures, and the two answers.
private struct Request: View {
    let model: AddDeviceModel
    let emoji: [String]
    let machine: Bool
    let sending: Bool
    let onDone: () -> Void
    @ScaledMetric(relativeTo: .title) private var size: CGFloat = 30

    var body: some View {
        VStack(spacing: 0) {
            (Text(machine ? "新电脑" : "新设备").fontWeight(.semibold).foregroundStyle(Theme.fg)
                + Text(" 请求加入").foregroundStyle(Theme.muted))
                .type(T.sm)
                .frame(maxWidth: .infinity, alignment: .leading)
            HStack(spacing: 12) {
                ForEach(Array(emoji.enumerated()), id: \.offset) { _, e in
                    Text(e).font(.system(size: min(size, 52)))
                }
            }
            .lineLimit(1).minimumScaleFactor(0.5)
            .padding(.top, 10).padding(.bottom, 12)
            .accessibilityElement(children: .combine)
            .accessibilityLabel("4 个图案：" + emoji.joined(separator: "，"))
            Text("和新设备上的 4 个图案一模一样才允许")
                .type(T.sm, Theme.muted).multilineTextAlignment(.center).padding(.bottom, 10)
            Btns {
                Btn("拒绝", kind: .negative, enabled: !sending, background: Theme.card) {
                    model.leave()
                    onDone()
                }
                Btn(sending ? "正在交给新设备…" : "图案一致，允许", kind: .primary, enabled: !sending, action: model.approve)
            }
        }
        .padding(14)
        .background(Theme.soft, in: RoundedRectangle(cornerRadius: Theme.Radius.md, style: .continuous))
        .padding(.top, 14)
    }
}
