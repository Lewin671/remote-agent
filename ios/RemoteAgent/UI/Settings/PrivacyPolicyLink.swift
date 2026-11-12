import RACore
import SwiftUI

enum PublicInformation {
    static let privacy = URL(string: "https://github.com/Lewin671/remote-agent/blob/main/docs/privacy.md")!
    static let support = URL(string: "https://github.com/Lewin671/remote-agent/issues")!
}

struct PrivacyPolicyLink: View {
    var body: some View {
        Link(L10n.settingsIosPrivacyPolicy(), destination: PublicInformation.privacy)
            .font(.footnote).frame(minHeight: 44)
    }
}
