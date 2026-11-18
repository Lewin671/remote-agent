import RACore
import RAProto
import SwiftUI

/// 审批详情: an approval on a page of its own, as opened from a link, rendered
/// from Core's `PBSessionViewModel.dock` and decided via `CoreHost.submitSessionCommand`.
struct ApprovalScreen: View {
    let session: String
    let approval: String

    @Environment(AppModel.self) private var model
    @Environment(AccountContext.self) private var context
    @State private var denying = false
    @State private var chosen: Bool?

    var body: some View {
        let vm = context.core.sessions[session]
        let header = vm?.header
        let dock = vm?.dock
        let matchingDock = dock.flatMap { $0.hasItem_p && $0.kind == .approval && $0.approval.approvalID == approval ? $0 : nil }
        let titleText = header.flatMap { $0.found ? $0.title.rendered() : nil } ?? L10n.approvalDetails()
        Ticking { now in
            let urgent = (matchingDock?.deadlineMs ?? 0) > 0 && (matchingDock?.deadlineMs ?? 0) - now < 60_000
            let leftText = matchingDock.flatMap { $0.hasTimeLeft ? $0.timeLeft.rendered() : nil } ?? ""
            let subtitle = header?.found == true
                ? titleText + (leftText.isEmpty ? "" : " · \(leftText)")
                : nil
            Page {
                VStack(spacing: 0) {
                    ConnectionBar()
                    if let dock = matchingDock {
                        if dock.approval.isSkeleton {
                            PageNote(L10n.sessionSkeleton())
                        } else {
                            let target = ApprovalTarget(
                                sessionId: session,
                                machineId: header?.machineID ?? model.state.sessions[session]?.machineId ?? "",
                                dock: dock
                            )
                            let choice = context.sessionStore.approvalChoice(approval)
                            ScrollView {
                                Group {
                                    if target.isSubmitted(choice: choice) {
                                        ApprovalSubmitted(target: target, chosen: chosen, floating: false)
                                    } else {
                                        DarkCard {
                                            DarkCardHead(kind: dock.approval.commandPreview.isEmpty ? dock.approval.toolName : dock.approval.commandPreview)
                                            ApprovalContent(approval: dock.approval, denying: denying).textSelection(.enabled).padding(.top, 14)
                                            ApprovalDecision(target: target, denying: $denying, chosen: $chosen)
                                                .padding(.top, 16)
                                        }
                                    }
                                }
                                .padding(.horizontal, DS.Space.card).padding(.vertical, 12)
                                .readableColumn()
                            }
                            .scrollDismissesKeyboard(.interactively)
                            .onAppear { if SessionDebug.shows("reason") { denying = true } }
                        }
                    } else if header?.missing == true {
                        PageNote(header?.missingText.rendered() ?? L10n.sessionIosMissing())
                    } else if vm?.timeline.loaded == true || (dock != nil && vm?.timeline.loading == false) {
                        handled
                    } else {
                        PageNote(L10n.projectsReadingDirectory())
                    }
                }
            }
            .navBar(
                L10n.approvalDetails(),
                subtitle: subtitle,
                subtitleColor: urgent ? DS.orangeInk : nil
            )
        }
        .followCoreSession(session)
        .sessionSamples(session)
    }

    @ViewBuilder private var handled: some View {
        let kept = context.sessionStore.reason(approval)
        VStack(spacing: 0) {
            Text(L10n.sessionApprovalResolved()).font(.subheadline).ink(DS.ink2).multilineTextAlignment(.center)
            if !kept.isEmpty {
                // What was being typed is the user's: kept to be copied, not sent anywhere.
                Code(kept).textSelection(.enabled).padding(.top, 16)
                TextAction(L10n.commonCopy(), alignment: .center) { UIPasteboard.general.string = kept }
            }
        }
        .frame(maxWidth: .infinity)
        .padding(24)
        .readableColumn()
    }
}
