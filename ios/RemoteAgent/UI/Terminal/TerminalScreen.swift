// The terminal's UIKit view in SwiftUI. The view is the terminal's own
// (`RemoteTerminal.host`): it outlives a redraw of the pane and is gone
// with the terminal.
import SwiftUI

struct TerminalScreen: UIViewRepresentable {
    let terminal: RemoteTerminal

    func makeUIView(context: Context) -> TerminalHostView {
        terminal.host.below = TerminalKeyBar.height
        return terminal.host
    }

    func updateUIView(_ view: TerminalHostView, context: Context) {}

    static func dismantleUIView(_ view: TerminalHostView, coordinator: ()) {
        view.hideKeyboard()
    }
}
