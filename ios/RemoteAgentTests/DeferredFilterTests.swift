import RACore
import SwiftUI
import UIKit
import Testing
@testable import RemoteAgent

@MainActor @Suite struct DeferredFilterTests {
    @Test func aRetainedFilterResolvesItsDefaultAtDisplayTime() {
        let filter = FilterField(text: .constant("draft"))
        let supplied = FilterField(text: .constant("draft"), prompt: "raw placeholder")
        let box = FilterBox(text: .constant("draft"))
        let loading = LoadingRow()
        let error = ErrorRow("raw detail", onRetry: {})
        for locale in ["zh-CN", "en", "zh-CN"] {
            MessageLocale.$overrideForTesting.withValue(locale) {
                #expect(filter.prompt == nil && filter.resolvedPrompt == L10n.homeFilterTitle())
                #expect(supplied.resolvedPrompt == "raw placeholder")
                #expect(filter.text == "draft")
                #expect(box.resolvedPlaceholder == L10n.homeFilterTitle())
                #expect(loading.resolvedText == L10n.projectsReadingDirectory())
                #expect(error.resolvedAction == L10n.commonRetry())
            }
        }
    }
    @Test func retainedUIKitDefaultLabelsResolveWhenAccessibilityReadsThem() {
        let terminal = TerminalHostView(preferred: 14)
        let message = MessageSelectionTextView()
        for locale in ["zh-CN", "en", "zh-CN"] {
            MessageLocale.$overrideForTesting.withValue(locale) {
                #expect(terminal.accessibilityLabel == L10n.terminalIosScreen())
                #expect(terminal.accessibilityHint == L10n.terminalIosScreenHint())
                #expect(message.accessibilityLabel == L10n.sessionIosMessageText())
            }
        }
        terminal.accessibilityLabel = "raw override"
        message.accessibilityLabel = "raw override"
        #expect(terminal.accessibilityLabel == "raw override" && message.accessibilityLabel == "raw override")
    }

}
