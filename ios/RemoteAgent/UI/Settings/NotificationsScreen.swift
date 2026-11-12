import RACore
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
            GroupLabel(L10n.settingsNotificationsOnThisClient())
            NotificationsNote()
            GroupLabel(L10n.settingsIosHowToKnowSomethingNeedsAttentionNow())
            GroupCard {
                Fact(L10n.settingsIosWhileTheAppIsOpen(), L10n.settingsIosApprovalsQuestionsAndPlansWaitingForYouAppearAt())
                Fact(L10n.settingsIosTheNumberOnTheSessionsTab(), L10n.settingsIosHowManySessionsAreWaitingForYou())
                Fact(L10n.settingsIosOtherClients(), L10n.settingsIosTheAndroidAppAndHomeScreenWebAppCan())
            }
            GroupNote(L10n.settingsIosComputerSessionsDoNotDependOnAlertsUnansweredApprovals())
        }
        .navBar(L10n.settingsIosNotifications())
    }
}

/// The explanation as 设置 shows it under the group's label, in the grey of anything that is only said.
struct NotificationsNote: View {
    var body: some View {
        GroupNote(L10n.settingsIosThisIosVersionCannotSendAlertsYetYouWill())
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
            Text(title).font(.body).ink(DS.ink)
            Text(text).font(.subheadline).ink(DS.ink2)
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(.horizontal, 14).padding(.vertical, 10)
        .accessibilityElement(children: .combine)
    }
}
