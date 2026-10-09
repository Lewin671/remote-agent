// The seam for a session's terminal (architecture §11; Android's
// term/TerminalPane.kt, term/RemoteTerminal.kt).
//
// NOT IMPLEMENTED HERE. The session screen has the 聊天 / 终端 tabs, opens on
// the terminal for `Route.session(id:terminal: true)`, remembers the tab on
// this device, and puts this view under the tabs with the page in the
// terminal's own dark colours (`Theme.Term`). Whoever writes the terminal
// replaces this view's body and keeps its initialiser.
//
// What the real one needs, and where it is:
//
//   - `sessionId`, and `running`: the session has not ended (the screen
//     shows the chat instead once it has);
//   - the connection: `AccountContext.remote.openTerminal(sessionId,
//     listener:)` gives a `TermConn`; frames are `PBTerminalFrame`;
//   - when to connect: only while `AppModel.state.conn == .online` and
//     `AppModel.foreground` (architecture §10 「后台和内存」). A connection
//     that is open must be registered with `AppModel.holdTerminal`, which
//     closes it when the app goes to the background, and let go with
//     `releaseTerminal`; the pane reopens it when `foreground` comes back;
//   - the size of the area it is given: the pane fills what is under the
//     tabs, down to the keyboard, and the page behind it is `Theme.Term.bg`;
//   - `onChat`: goes back to the chat, for whatever in the pane asks to.
import SwiftUI

struct TerminalPane: View {
    let sessionId: String
    /// The session has not ended.
    let running: Bool
    /// Shows the chat instead.
    var onChat: () -> Void = {}

    var body: some View {
        VStack(spacing: 8) {
            Text("终端").type(T.h17, Theme.Term.fg).accessibilityAddTraits(.isHeader)
            Text("这个页面还没有做").type(T.sm, Theme.Term.dim)
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .background(Theme.Term.bg)
    }
}
