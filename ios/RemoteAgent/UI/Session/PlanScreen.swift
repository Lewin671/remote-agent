import RACore
import RAMarkdownUI
import RAProto
import SwiftUI

/// 计划: a proposed plan on a page of its own, whole, with the choices under
/// it while Core reports `dock.kind == .plan` (Android's PlanPage).
struct PlanScreen: View {
    let session: String

    @Environment(AccountContext.self) private var context
    @Environment(Navigator.self) private var nav
    @Environment(\.page) private var page

    var body: some View {
        let vm = context.core.sessions[session]
        let header = vm?.header
        let dock = vm?.dock
        let openPlan = dock.flatMap { $0.hasItem_p && $0.kind == .plan ? $0 : nil }
        let timelinePlan: String? = vm?.timeline.items.reversed().compactMap {
            if case .plan(let p)? = $0.kind, !p.markdown.isEmpty { return p.markdown }
            return nil
        }.first
        let markdown = openPlan?.plan.markdown.nilIfEmpty ?? timelinePlan
        Page {
            VStack(spacing: 0) {
                ConnectionBar()
                if let md = markdown, !md.isEmpty {
                    ScrollView {
                        MarkdownView(md, bleed: 16)
                            .padding(.horizontal, 16).padding(.top, 8).padding(.bottom, 16)
                            .readableColumn()
                    }
                    if let dock = openPlan {
                        VStack(alignment: .leading, spacing: 0) {
                            PlanButtons(
                                sessionID: session,
                                dock: dock,
                                full: true
                            ) {
                                nav.back(from: page)
                            }
                        }
                        .padding(.horizontal, 14).padding(.top, 8).padding(.bottom, 8)
                        .readableColumn()
                        .topLine()
                    }
                } else if header?.missing == true {
                    PageNote(header?.missingText.rendered() ?? L10n.sessionIosMissing())
                } else {
                    PageNote(L10n.projectsReadingDirectory())
                }
            }
        }
        .navBar(L10n.modesPlan(), subtitle: header?.found == true ? header?.title.rendered() : nil)
        .followCoreSession(session)
        .sessionSamples(session)
    }
}

private extension String {
    var nilIfEmpty: String? { isEmpty ? nil : self }
}
