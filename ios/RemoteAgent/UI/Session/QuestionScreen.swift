import RACore
import SwiftUI

/// 回答提问: a long question on a page of its own; the answer button stays
/// at hand (Android's QuestionPage). Which question of several the user is
/// at, and what is ticked, are shared with the dock (`SessionStore`), so
/// going between the two keeps both.
struct QuestionScreen: View {
    let session: String
    let question: String

    @Environment(AppModel.self) private var model
    @Environment(Navigator.self) private var nav
    @Environment(\.page) private var page
    @State private var chat = SessionModel()
    @State private var finder = SessionFinder()
    @State private var act = ActionRunner()

    var body: some View {
        let s = model.state.sessions[session]
        let d = chat.derived
        Page {
            VStack(spacing: 0) {
                ConnectionBar()
                if let s, let q = d.questions[question] {
                    let toTerminal = {
                        nav.back()
                        nav.open(.session(id: session, terminal: true))
                    }
                    ScrollView {
                        VStack(alignment: .leading, spacing: 0) {
                            QuestionBody(session: s, question: q, part: .content, full: true, act: act, onAnswered: { nav.back(from: page) })
                        }
                        .frame(maxWidth: .infinity, alignment: .leading)
                        .padding(.horizontal, 14).padding(.bottom, 12)
                        .readableColumn()
                    }
                    VStack(alignment: .leading, spacing: 0) {
                        QuestionBody(
                            session: s, question: q, part: .actions, full: true, act: act, onTerminal: toTerminal,
                            onAnswered: { nav.back(from: page) })
                    }
                    .padding(.horizontal, 14).padding(.top, 8).padding(.bottom, 8)
                    .readableColumn()
                    .topLine()
                } else {
                    // Answered already, here, on another client or in the terminal: the card is withdrawn everywhere.
                    PageNote(d.answered[question] != nil ? "这个问题已经回答了。" : s == nil ? lookupNote(finder.lookup) ?? "读取中…" : "读取中…")
                }
            }
        }
        .navBar("回答提问", subtitle: s.map { titleOf($0) + " · " + agentName($0.agent) })
        .followSession(session, chat: chat, finder: finder)
    }
}
