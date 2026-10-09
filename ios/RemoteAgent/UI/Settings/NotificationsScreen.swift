import SwiftUI

/// 本客户端的通知. This version of the iOS app cannot send any: a push to
/// an iPhone goes through Apple's service, which takes an Apple Developer
/// account and a relay that speaks to it, and neither is decided yet
/// (architecture §12 knows Web Push and FCM only). So the page says so,
/// quietly, and offers nothing to switch: a switch that does nothing would
/// promise a reminder that never comes. Nor is the system asked for the
/// permission, which would be asking for nothing.
struct NotificationsScreen: View {
    var body: some View {
        SettingsPage {
            GroupLabel("本客户端的通知")
            NotificationsNote()
            GroupLabel("现在怎么知道有事要处理")
            GroupCard {
                Fact("打开 App 时", "等你处理的审批、提问和计划排在「会话」页最上面")
                Fact("「会话」标签上的数字", "有几个会话在等你")
                Fact("其他客户端", "Android App 和添加到主屏幕的网页可以在各自的设置里开启提醒")
            }
            GroupNote("电脑上的会话不依赖提醒：没人处理的审批到时间按拒绝算，之后仍可以在会话里继续。")
        }
        .navBar("通知")
    }
}

/// The explanation as 设置 shows it under the group's label, in the grey of anything that is only said.
struct NotificationsNote: View {
    var body: some View {
        GroupNote("这个 iOS 版本还不能发提醒。有会话等你处理时不会收到系统通知，打开 App 后会显示在「会话」页最上面。")
    }
}

/// A row that only says something: what, and a line under it.
private struct Fact: View {
    private let title: String
    private let text: String

    init(_ title: String, _ text: String) {
        self.title = title
        self.text = text
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 2) {
            Text(title).type(T.body, Theme.fg)
            Text(text).type(T.sm, Theme.muted)
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(.horizontal, 14).padding(.vertical, 10)
        .accessibilityElement(children: .combine)
    }
}
