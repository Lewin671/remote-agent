import RACore
import SwiftUI

/// 添加设备: this client is an ordinary member. Adding and removing devices
/// is the phone app's job (architecture §9 「配对」 and 「删除与执行隔离」).
struct AddDeviceScreen: View {
    @Environment(AppModel.self) private var model

    var body: some View {
        Page {
            ScrollView {
                VStack(alignment: .leading, spacing: 0) {
                    ConnectionBar()
                    GroupNote(
                        "添加和移除设备只能在手机 App 上进行。在手机上打开 remote-agent，进入「设备」，点「添加设备」生成二维码或 8 位配对码，再回到这台设备扫码或输入。"
                    )
                    .padding(.top, 14)
                    if !model.settings.settings.webOrigin.isEmpty {
                        GroupNote("网页也可以用同一个配对码加入：\(model.settings.settings.webOrigin)")
                            .padding(.top, 10)
                    }
                }
                .padding(.horizontal, Theme.Space.page)
                .frame(maxWidth: 480)
                .frame(maxWidth: .infinity)
            }
            .scrollBounceBehavior(.basedOnSize)
        }
        .navBar("添加设备")
    }
}
