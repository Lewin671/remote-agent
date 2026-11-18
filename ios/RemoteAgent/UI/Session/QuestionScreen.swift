import RACore
import RAProto
import SwiftUI

/// 回答提问: a long question on a page of its own, rendered directly from
/// Core's `PBSessionViewModel.dock.question`.
struct QuestionScreen: View {
    let session: String
    let question: String

    @Environment(AccountContext.self) private var context
    @Environment(Navigator.self) private var nav
    @Environment(\.page) private var page

    var body: some View {
        let vm = context.core.sessions[session]
        let header = vm?.header
        let dock = vm?.dock
        let activeQuestion = dock.flatMap {
            $0.hasItem_p && $0.kind == .question && $0.question.questionID == question ? $0 : nil
        }
        let parts = [header?.title.rendered(), header?.agentName].compactMap { ($0?.isEmpty == false) ? $0 : nil }
        let subtitle: String? = header?.found == true && !parts.isEmpty ? parts.joined(separator: " \u{00B7} ") : nil
        Page {
            VStack(spacing: 0) {
                ConnectionBar()
                if let dock = activeQuestion {
                    let machineID = header?.machineID ?? ""
                    let toTerminal = {
                        nav.back()
                        nav.open(.session(id: session, terminal: true))
                    }
                    ScrollView {
                        VStack(alignment: .leading, spacing: 0) {
                            QuestionBody(
                                sessionID: session,
                                machineID: machineID,
                                dock: dock,
                                part: .content,
                                full: true,
                                onAnswered: { nav.back(from: page) }
                            )
                        }
                        .frame(maxWidth: .infinity, alignment: .leading)
                        .padding(.horizontal, 14).padding(.bottom, 12)
                        .readableColumn()
                    }
                    VStack(alignment: .leading, spacing: 0) {
                        QuestionBody(
                            sessionID: session,
                            machineID: machineID,
                            dock: dock,
                            part: .actions,
                            full: true,
                            onTerminal: toTerminal,
                            onAnswered: { nav.back(from: page) }
                        )
                    }
                    .padding(.horizontal, 14).padding(.top, 8).padding(.bottom, 8)
                    .readableColumn()
                    .topLine()
                } else if header?.missing == true {
                    PageNote(header?.missingText.rendered() ?? L10n.sessionIosMissing())
                } else if vm?.timeline.loaded == true {
                    PageNote(L10n.dockIosQuestionAnswered())
                } else {
                    PageNote(L10n.projectsReadingDirectory())
                }
            }
        }
        .navBar(L10n.dockAnswerTitle(), subtitle: subtitle)
        .followCoreSession(session)
        .sessionSamples(session)
    }
}
