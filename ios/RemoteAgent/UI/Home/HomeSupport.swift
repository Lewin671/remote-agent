// Tail row view for pagination/retry on lists backed by core ViewModels.
import RACore
import RAProto
import SwiftUI

/// The line that ends a list whose earlier sessions are wanted; after a failure a tap tries again.
struct EarlierTail: View {
    let text: String
    let retry: Bool
    var onRetry: () -> Void = {}

    init(_ text: String, retry: Bool, onRetry: @escaping () -> Void = {}) {
        self.text = text
        self.retry = retry
        self.onRetry = onRetry
    }

    var body: some View {
        let line = Text(text).font(.footnote).ink(DS.ink2).frame(maxWidth: .infinity, alignment: .leading)
        if retry {
            Button(action: onRetry) {
                line.hitTarget(alignment: .leading)
            }
            .buttonStyle(PressStyle())
        } else {
            line
        }
    }
}
