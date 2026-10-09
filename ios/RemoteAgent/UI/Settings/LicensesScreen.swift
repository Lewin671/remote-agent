import SwiftUI

/// 开源许可: the licences of what the app is built with (Android's
/// LicensesPage), each with its full text. No fonts are listed because none
/// is bundled: on Apple devices the design uses the system's own
/// (design-system §3).
struct LicensesScreen: View {
    @State private var open: License?

    var body: some View {
        let used = Licenses.all.filter(\.linked)
        let resolved = Licenses.all.filter { !$0.linked }
        SettingsPage {
            GroupLabel("图标和库")
            GroupCard {
                for license in used {
                    GRow(license.name, sub: license.use, value: license.kind, chevron: true) { open = license }
                }
            }
            GroupNote("字体用的是系统自带的 SF Pro、苹方和 SF Mono，App 里没有打包字体。")
            GroupLabel("随依赖一起解析的库")
            GroupCard {
                for license in resolved {
                    GRow(license.name, value: license.kind, chevron: true) { open = license }
                }
            }
            GroupNote("这些是 connect-swift 和 SwiftTerm 的可选部分用到的库，没有编进这个 App；许可证一并附上。")
        }
        .navBar("开源许可")
        .kitSheet(item: $open, size: .large) { license in
            SheetHead(license.name, action: "完成") { open = nil }
            Text(Licenses.text(license).map(Licenses.display) ?? "许可证文本没有随 App 打包").type(T.meta, Theme.fg2)
                .textSelection(.enabled)
                .frame(maxWidth: .infinity, alignment: .leading)
        }
        .onAppear {
            #if DEBUG
            if let id = SettingsDemo.state, open == nil { open = Licenses.all.first { $0.id == id } }
            #endif
        }
    }
}
