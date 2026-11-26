// What waits for you on the home page (design-system §6 「等你的事」「终端卡」):
// the most urgent approval as the dark card, decided right there, and the
// rest one to a row of the grey list, which open the session where its dock
// handles them.
import RACore
import RAProto
import SwiftUI

/// One row of the grey list: a question, a plan, a popup, or an approval after the first.
struct HomePendingRow: View {
    let row: PBCoreAttentionRowView
    var first = true
    var last = true
    @Environment(Navigator.self) private var nav

    var body: some View {
        let title = row.title.rendered()
        let kind = row.kindText.rendered()
        // The summary repeats the kind for most rows; it is only the line when neither is given.
        let detail = row.detail.rendered()
        let summary = kind.isEmpty && detail.isEmpty ? row.summary.rendered() : detail
        let spoken = [title, kind, summary].filter { !$0.isEmpty }.joined(separator: L10n.commonIosSpokenSeparator())
        Button {
            nav.open(.session(id: row.sessionID))
        } label: {
            PendingRowLayout(title: title, kind: kind, summary: summary, first: first, last: last)
        }
        .buttonStyle(RowHighlight())
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(spoken)
        .accessibilityAddTraits(.isButton)
    }
}

/// Compact confirmation strip shown briefly after a home approval or question resolves.
struct HomeResolvedLine: View {
    let text: String
    let checked: Bool

    var body: some View {
        HStack(spacing: 8) {
            if checked {
                Image(systemName: "checkmark")
                    .font(.footnote.weight(.semibold))
                    .foregroundStyle(DS.orangeInk)
                    .accessibilityHidden(true)
            }
            Text(text)
                .font(.footnote)
                .foregroundStyle(DS.ink2)
                .lineLimit(2)
                .frame(maxWidth: .infinity, alignment: .leading)
        }
        .padding(.horizontal, 16)
        .padding(.vertical, 12)
        .frame(minHeight: 48, alignment: .leading)
        .background(DS.tc, in: RoundedRectangle(cornerRadius: 24, style: .continuous))
        .overlay {
            RoundedRectangle(cornerRadius: 24, style: .continuous)
                .strokeBorder(DS.tcLine, lineWidth: 1)
        }
        .environment(\.colorScheme, .dark)
        .accessibilityElement(children: .combine)
    }
}

/// Shared title header for home dark cards (`HomeApprovalCard` and `HomeQuestionCard`).
private struct HomeCardTitle: View {
    let sessionID: String
    let titleText: String
    let placeText: String
    @Environment(Navigator.self) private var nav

    var body: some View {
        let spoken = [
            titleText,
            placeText.replacingOccurrences(of: " · ", with: L10n.commonIosSpokenSeparator()),
        ].filter { !$0.isEmpty }.joined(separator: L10n.commonIosSpokenSeparator())
        Button {
            nav.open(.session(id: sessionID))
        } label: {
            VStack(alignment: .leading, spacing: 2) {
                HStack(spacing: 4) {
                    Text(titleText).font(.title3.bold()).foregroundStyle(DS.ink).lineLimit(2)
                    Image(systemName: "chevron.right").font(.subheadline.weight(.semibold)).foregroundStyle(DS.ink3)
                }
                if !placeText.isEmpty {
                    Text(placeText).font(.footnote).foregroundStyle(DS.ink2).lineLimit(1)
                }
            }
            .frame(maxWidth: .infinity, alignment: .leading)
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .padding(.top, 12)
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(spoken)
        .accessibilityHint(L10n.homeIosOpenSession())
        .accessibilityAddTraits(.isButton)
    }
}

/// The most urgent approval as the dark card, rendered purely from Core's `PBCoreAttentionCardView`.
struct HomeApprovalCard: View {
    let card: PBCoreAttentionCardView
    @State private var full = HomeLaunch.value("-homeCard") == "full"
    @State private var more = 0
    @State private var total = 0

    /// Whole lines of the command before 看完整命令.
    static let lines = 5

    var body: some View {
        let forceDeny = HomeLaunch.value("-homeCard") == "deny"
        let target = ApprovalTarget(card: card)
        let commandBody = card.body.trimmingCharacters(in: .whitespacesAndNewlines)
        let desc = card.description_p.trimmingCharacters(in: .whitespacesAndNewlines)
        Group {
            if card.phase == .resolvedThisClient || card.phase == .resolvedElsewhere {
                HomeResolvedLine(
                    text: card.resolutionText.rendered(),
                    checked: card.phase == .resolvedThisClient
                )
                .padding(.horizontal, DS.Space.card)
            } else if card.isSkeleton {
                DarkCard {
                    DarkCardHead(kind: card.kindText.rendered())
                    Text(L10n.sessionSkeleton())
                        .font(.subheadline)
                        .foregroundStyle(DS.ink2)
                        .padding(.top, 12)
                }
                .padding(.horizontal, DS.Space.card)
            } else if target.showCompactSubmitted {
                ApprovalSubmitted(target: target, floating: false)
                    .padding(.horizontal, DS.Space.card)
            } else {
                DarkCard {
                    DarkCardHead(kind: card.kindText.rendered(), right: card.time.rendered(), hot: card.hot)
                    HomeCardTitle(
                        sessionID: card.sessionID,
                        titleText: card.title.rendered(),
                        placeText: card.`where`.rendered()
                    )
                    if !commandBody.isEmpty {
                        command
                    }
                    if !(target.denyingOpen || forceDeny) && !desc.isEmpty {
                        Text(desc)
                            .font(.subheadline)
                            .foregroundStyle(DS.ink2)
                            .padding(.top, 12)
                    }
                    ApprovalDecision(
                        target: target,
                        cut: !commandBody.isEmpty && more > 0 && !full,
                        onFull: { withAnimation(Motion.enter()) { full = true } },
                        // Until the command is laid out it is not known to be whole: nothing is allowed before.
                        measured: commandBody.isEmpty || total > 0,
                        forceDenying: forceDeny,
                        signals: ("home.allow", "home.deny")
                    )
                    .padding(.top, 16)
                }
                .padding(.horizontal, DS.Space.card)
            }
        }
    }

    @ViewBuilder private var command: some View {
        let cut = more > 0 && !full
        CommandBlock(card.body, lines: full ? nil : Self.lines, more: $more, total: $total)
            .padding(.top, 14)
            .accessibilityLabel(L10n.listStateIosCommandLabel(command: card.body))
        if cut {
            Text(L10n.markdownIosHiddenLines(count: more)).font(.footnote).foregroundStyle(DS.ink2).padding(.top, 8)
        } else if full && total > Self.lines {
            HStack(spacing: 4) {
                Text(L10n.markdownLineCount(count: total)).font(.footnote).foregroundStyle(DS.ink2)
                LinkButton(L10n.markdownCollapse()) { withAnimation(Motion.enter()) { full = false } }.font(.footnote)
            }
            .padding(.vertical, -8).padding(.top, 8)
        }
        if card.truncated {
            Text(L10n.approvalTruncated()).font(.footnote).foregroundStyle(DS.red).padding(.top, 8)
        }
    }
}

/// Home's inline single-select question dark card, rendered purely from Core's `PBCoreHomeQuestionCardView`.
struct HomeQuestionCard: View {
    let question: PBCoreHomeQuestionCardView
    @Environment(Navigator.self) private var nav
    @Environment(AccountContext.self) private var context
    @State private var showSpinner = false

    private var sending: Bool { question.phase == .submitting }

    var body: some View {
        Group {
            if question.phase == .resolvedThisClient || question.phase == .resolvedElsewhere {
                HomeResolvedLine(
                    text: question.resolutionText.rendered(),
                    checked: question.phase == .resolvedThisClient
                )
                .padding(.horizontal, DS.Space.card)
            } else {
                DarkCard {
                    DarkCardHead(kind: question.badgeText.rendered(), right: question.timeText.rendered())
                    HomeCardTitle(
                        sessionID: question.sessionID,
                        titleText: question.title.rendered(),
                        placeText: question.`where`.rendered()
                    )
                    if !question.header.isEmpty {
                        Text(question.header)
                            .font(.caption.weight(.semibold))
                            .foregroundStyle(DS.ink2)
                            .padding(.top, 12)
                    }
                    Text(question.questionText)
                        .font(.body.weight(.semibold))
                        .foregroundStyle(DS.ink)
                        .padding(.top, question.header.isEmpty ? 12 : 4)
                        .accessibilityAddTraits(.isHeader)
                    optionsList
                    failureNote
                    offlineNote
                    DockFoot(L10n.dockNoneFit(), link: L10n.homeQuestionAnswerInTerminal(), arrow: true) {
                        nav.open(.session(id: question.sessionID, terminal: true))
                    }
                }
                .padding(.horizontal, DS.Space.card)
                .task(id: "\(sending):\(question.sessionID):\(question.questionID):\(question.pickedOptionNum)") {
                    showSpinner = false
                    guard sending else { return }
                    try? await Task.sleep(for: .milliseconds(150))
                    if !Task.isCancelled { showSpinner = true }
                }
            }
        }
    }

    private var optionsList: some View {
        let recWord = L10n.dockRecommended()
        return VStack(spacing: 8) {
            ForEach(question.options, id: \.optionNum) { opt in
                let isPicked = question.pickedOptionNum == opt.optionNum
                let dimmed = sending && !isPicked
                let enabled = question.canAnswer && !sending
                let shape = RoundedRectangle(cornerRadius: 14, style: .continuous)
                let ink: Color = isPicked ? .black : .white
                let subInk: Color = isPicked ? Color.black.opacity(0.6) : DS.ink2
                Button {
                    SessionHaptics.tap()
                    let host = context.coreHost
                    let sid = question.sessionID
                    let qid = question.questionID
                    let num = opt.optionNum
                    Task {
                        await host.homeAnswerQuestion(sessionID: sid, questionID: qid, optionNum: num)
                    }
                } label: {
                    HStack(alignment: .center, spacing: 10) {
                        VStack(alignment: .leading, spacing: 2) {
                            Text(opt.label)
                                .font(.body.weight(.semibold))
                                .foregroundStyle(ink)
                            if !opt.description_p.isEmpty {
                                Text(opt.description_p)
                                    .font(.footnote)
                                    .foregroundStyle(subInk)
                            }
                        }
                        .frame(maxWidth: .infinity, alignment: .leading)
                        if opt.recommended && !recWord.isEmpty {
                            Text(recWord)
                                .font(.caption.weight(.semibold))
                                .foregroundStyle(isPicked ? Color.black.opacity(0.62) : DS.blue)
                        }
                        if sending && isPicked && showSpinner {
                            ProgressView()
                                .controlSize(.small)
                                .tint(.black)
                        }
                    }
                    .multilineTextAlignment(.leading)
                    .padding(.horizontal, 14)
                    .padding(.vertical, 11)
                    .frame(maxWidth: .infinity, minHeight: DS.minHit, alignment: .leading)
                    .background(isPicked ? Color.white : Color.white.opacity(0.1), in: shape)
                    .contentShape(Rectangle())
                }
                .buttonStyle(PressStyle())
                .opacity(dimmed ? 0.45 : 1)
                .disabled(!enabled)
                .accessibilityElement(children: .combine)
                .accessibilityAddTraits(isPicked ? .isSelected : [])
            }
        }
        .padding(.top, 14)
    }

    @ViewBuilder private var failureNote: some View {
        if question.phase == .failed {
            let rawErr = question.hasCommand ? question.command.renderedErrorText : ""
            let errMsg = rawErr.isEmpty ? L10n.approvalIosUnknownResult() : rawErr
            HStack(alignment: .firstTextBaseline, spacing: 8) {
                Text(L10n.homeQuestionSendFailed(reason: errMsg))
                    .font(.footnote)
                    .foregroundStyle(DS.red)
                    .frame(maxWidth: .infinity, alignment: .leading)
                if question.command.canRetry || question.pickedOptionNum > 0 {
                    LinkButton(L10n.commonRetry()) {
                        SessionHaptics.tap()
                        let host = context.coreHost
                        let sid = question.sessionID
                        let qid = question.questionID
                        Task {
                            await host.homeRetryAttention(sessionID: sid, itemID: qid)
                        }
                    }
                    .font(.footnote.weight(.semibold))
                }
            }
            .padding(.top, 10)
        }
    }

    @ViewBuilder private var offlineNote: some View {
        if !question.canAnswer && question.hasOfflineNote {
            let text = question.offlineNote.rendered()
            if !text.isEmpty {
                Text(text)
                    .font(.footnote)
                    .foregroundStyle(DS.ink2)
                    .padding(.top, 8)
            }
        }
    }
}

/// How a row answers a touch: its ground darkens under the finger, as a system list row's does.
struct RowHighlight: ButtonStyle {
    func makeBody(configuration: Configuration) -> some View {
        configuration.label
            .opacity(configuration.isPressed ? 0.6 : 1)
            .animation(Motion.enter(Motion.fast), value: configuration.isPressed)
    }
}
