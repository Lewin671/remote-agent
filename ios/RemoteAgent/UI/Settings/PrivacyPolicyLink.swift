import SwiftUI

enum PublicInformation {
    static let privacy = URL(string: "https://github.com/Lewin671/remote-agent/blob/main/docs/privacy.md")!
    static let support = URL(string: "https://github.com/Lewin671/remote-agent/issues")!
}

struct PrivacyPolicyLink: View {
    var body: some View {
        Link("隐私政策", destination: PublicInformation.privacy)
            .font(.footnote).frame(minHeight: 44)
    }
}
