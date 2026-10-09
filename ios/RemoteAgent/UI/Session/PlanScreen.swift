import RACore
import RAMarkdownUI
import SwiftUI

/// 计划: a proposed plan on a page of its own, whole, with the choices under
/// it while it still waits for one (Android's PlanPage).
struct PlanScreen: View {
    let session: String

    @Environment(AppModel.self) private var model
    @Environment(Navigator.self) private var nav
    @State private var chat = SessionModel()
    @State private var finder = SessionFinder()

    var body: some View {
        let s = model.state.sessions[session]
        Page {
            VStack(spacing: 0) {
                ConnectionBar()
                if let s, let plan = chat.derived.plan {
                    ScrollView {
                        MarkdownView(plan.text, bleed: 16)
                            .padding(.horizontal, 16).padding(.top, 8).padding(.bottom, 16)
                            .readableColumn()
                    }
                    // Answered already, here or elsewhere: the plan stays to be read, without its choices.
                    if s.info?.planOpen == true {
                        VStack(alignment: .leading, spacing: 0) {
                            PlanButtons(session: s, full: true) { nav.back() }
                        }
                        .padding(.horizontal, 14).padding(.top, 8).padding(.bottom, 8)
                        .readableColumn()
                        .topLine()
                    }
                } else {
                    PageNote(s == nil ? lookupNote(finder.lookup) ?? "读取中…" : "读取中…")
                }
            }
        }
        .navBar("计划", subtitle: s.map { titleOf($0) })
        .followSession(session, chat: chat, finder: finder)
    }
}
