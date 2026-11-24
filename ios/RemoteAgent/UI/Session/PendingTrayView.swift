// Docked pending-message tray above the composer input inside the unified
// glass card (`docs/design-system.md` §6 「输入框 / 待发区」). Driven by Core's
// `PBSessionPendingTrayView`; shell only renders, tracks UI fold/expand state,
// and forwards actions to `CoreHost`.
import RACore
import RAProto
import SwiftUI

struct PendingTrayView: View {
    let sessionID: String
    let tray: PBSessionPendingTrayView
    let keyboardOpen: Bool
    var maxHeight: CGFloat = 340
    var onTerminal: () -> Void = {}

    @Environment(AccountContext.self) private var context
    @State private var openID: String?
    @State private var showAll = false
    @State private var forceExpanded = false

    private var hasItems: Bool { tray.totalCount > 0 }
    private var showUndo: Bool { tray.hasUndo && tray.undo.visible }
    private var compact: Bool { keyboardOpen && hasItems && !forceExpanded }

    var body: some View {
        Group {
            if hasItems || showUndo {
                VStack(alignment: .leading, spacing: 0) {
                    if compact {
                        compactRow
                    } else {
                        if hasItems {
                            MaxHeightProposal(maxHeight: maxHeight) {
                                ViewThatFits(in: .vertical) {
                                    groupsContent
                                        .frame(maxWidth: .infinity, alignment: .leading)
                                        .fixedSize(horizontal: false, vertical: true)
                                    ScrollView {
                                        groupsContent
                                            .frame(maxWidth: .infinity, alignment: .leading)
                                    }
                                    .scrollBounceBehavior(.basedOnSize)
                                    .scrollIndicators(.hidden)
                                }
                            }
                        }
                        if showUndo {
                            undoBar
                        }
                    }
                    Rectangle()
                        .fill(DS.sep)
                        .frame(height: 0.5)
                }
                .clipped()
                .animation(Motion.enter(Motion.fast), value: compact)
                .animation(Motion.enter(), value: openID)
                .animation(Motion.enter(), value: showAll)
            }
        }
        .onChange(of: keyboardOpen) { old, new in
            if old != new {
                forceExpanded = false
                if new { openID = nil }
            }
        }
        .onChange(of: tray.orderedItems.map(\.requestID)) { _, ids in
            if let id = openID, !ids.contains(id) {
                openID = nil
            }
        }
        .onChange(of: tray.totalCount) { _, count in
            if count <= 3 { showAll = false }
        }
        .debugSignal("session.cancel") {
            if let target = tray.orderedItems.first(where: { $0.canCancel && !$0.networkActionsDisabled }) {
                Task { await context.coreHost.cancelTrayItem(sessionID: sessionID, requestID: target.requestID) }
            }
        }
        .debugSignal("session.resume") {
            if let target = tray.orderedItems.first(where: { $0.canResume && !$0.networkActionsDisabled }) {
                Task { await context.coreHost.resumeTrayItem(sessionID: sessionID, requestID: target.requestID) }
            }
        }
        .debugSignal("session.expand") {
            if let target = tray.orderedItems.last(where: { $0.canPromote || $0.canRestore }) {
                openID = openID == target.requestID ? nil : target.requestID
            }
        }
    }

    private var compactRow: some View {
        Button {
            withAnimation(Motion.enter()) { forceExpanded = true }
        } label: {
            HStack(spacing: 6) {
                HStack(spacing: 0) {
                    Text(L10n.queueKeyboardSummary(count: Int(tray.totalCount)))
                        .foregroundStyle(DS.ink2)
                    if tray.failedCount > 0 {
                        (Text(" · ") + Text(L10n.queueKeyboardFailed(count: Int(tray.failedCount))))
                            .foregroundStyle(DS.red)
                    }
                }
                .font(.footnote)
                .lineLimit(1)
                Spacer(minLength: 8)
                Image(systemName: "chevron.up")
                    .font(.caption.weight(.semibold))
                    .foregroundStyle(DS.ink2)
            }
            .padding(.horizontal, 14)
            .padding(.vertical, 8)
            .frame(maxWidth: .infinity, minHeight: 36, alignment: .leading)
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .accessibilityIdentifier("pending-tray-compact")
    }

    private var groupsContent: some View {
        let ordered = tray.orderedItems
        let shown = showAll ? ordered : shownPending(ordered, expanded: false)
        let shownIDs = Set(shown.map(\.requestID))
        let hiddenCount = ordered.count - shown.count

        return VStack(alignment: .leading, spacing: 0) {
            ForEach(Array(tray.groups.enumerated()), id: \.offset) { _, group in
                let items = group.items.filter { shownIDs.contains($0.requestID) }
                if !items.isEmpty {
                    groupHeader(group)
                    ForEach(items, id: \.requestID) { item in
                        rowView(item)
                    }
                }
            }
            if !showAll && hiddenCount > 0 {
                Button {
                    withAnimation(Motion.enter()) { showAll = true }
                } label: {
                    HStack(spacing: 4) {
                        Text(L10n.queueMoreItems(count: hiddenCount))
                        Image(systemName: "chevron.down")
                            .font(.caption2.weight(.semibold))
                    }
                    .font(.footnote)
                    .foregroundStyle(DS.blue)
                    .padding(.leading, 40)
                    .padding(.trailing, 14)
                    .padding(.vertical, 7)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .contentShape(Rectangle())
                }
                .buttonStyle(.plain)
                .accessibilityIdentifier("pending-tray-more")
            } else if showAll && ordered.count > 3 {
                Button {
                    withAnimation(Motion.enter()) { showAll = false }
                } label: {
                    HStack(spacing: 4) {
                        Text(L10n.queueCollapse())
                        Image(systemName: "chevron.up")
                            .font(.caption2.weight(.semibold))
                    }
                    .font(.footnote)
                    .foregroundStyle(DS.blue)
                    .padding(.leading, 40)
                    .padding(.trailing, 14)
                    .padding(.vertical, 7)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .contentShape(Rectangle())
                }
                .buttonStyle(.plain)
                .accessibilityIdentifier("pending-tray-collapse")
            }
        }
        .padding(.top, 4)
        .padding(.bottom, 4)
    }

    private func groupHeader(_ group: PBSessionTrayGroupView) -> some View {
        let color = toneColor(group.tone)
        return HStack(alignment: .firstTextBaseline, spacing: 8) {
            Text(group.hasHeaderText ? group.headerText.rendered() : group.title.rendered())
                .font(.caption.weight(.medium))
                .foregroundStyle(color)
                .lineLimit(1)
            Spacer(minLength: 8)
            if group.canCancelAll {
                Button {
                    Task { await context.coreHost.cancelAllPaused(sessionID: sessionID) }
                } label: {
                    Text(L10n.queueCancelAll())
                        .font(.caption)
                        .foregroundStyle(DS.blue)
                        .padding(.vertical, 4)
                        .padding(.horizontal, 4)
                        .contentShape(Rectangle())
                }
                .buttonStyle(.plain)
                .disabled(tray.offline)
                .opacity(tray.offline ? 0.35 : 1)
                .accessibilityIdentifier("pending-tray-cancel-all")
            }
        }
        .padding(.top, 8)
        .padding(.bottom, 2)
        .padding(.horizontal, 14)
    }

    @ViewBuilder
    private func rowView(_ item: PBSessionTrayItemView) -> some View {
        if item.isSkeleton {
            HStack(alignment: .center, spacing: 8) {
                Color.clear.frame(width: 18, height: 1)
                Text(L10n.sessionSkeleton())
                    .font(.subheadline)
                    .foregroundStyle(DS.ink2)
                    .frame(maxWidth: .infinity, alignment: .leading)
            }
            .padding(.leading, 14)
            .padding(.trailing, 14)
            .padding(.vertical, 6)
            .frame(minHeight: 38)
            .accessibilityIdentifier("pending-tray-row-\(item.requestID)")
        } else {
            normalRowView(item)
        }
    }

    private func normalRowView(_ item: PBSessionTrayItemView) -> some View {
        let expanded = openID == item.requestID
        let reasonText = item.hasReasonText ? item.reasonText.rendered() : ""
        let rColor = toneColor(item.tone)
        let actionsDisabled = item.networkActionsDisabled

        return HStack(alignment: expanded || !reasonText.isEmpty ? .top : .center, spacing: 8) {
            leadSlot(item)
                .frame(width: 18, alignment: .center)
                .padding(.top, expanded || !reasonText.isEmpty ? 3 : 0)

            VStack(alignment: .leading, spacing: 0) {
                Button {
                    withAnimation(Motion.enter()) {
                        openID = expanded ? nil : item.requestID
                    }
                } label: {
                    VStack(alignment: .leading, spacing: 2) {
                        rowTitle(item, expanded: expanded)
                        if !reasonText.isEmpty {
                            Text(reasonText)
                                .font(.caption)
                                .foregroundStyle(item.tone == .neutral ? DS.ink2 : rColor)
                                .lineLimit(1)
                                .truncationMode(.tail)
                        }
                    }
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .contentShape(Rectangle())
                }
                .buttonStyle(.plain)

                if expanded && !item.localAttachments.isEmpty {
                    OutboxAttachmentPreviews(
                        center: ImageCenter.of(context.remote),
                        attachments: item.localAttachments
                    )
                    .padding(.top, 6)
                }

                if expanded && (item.canPromote || item.canRestore) {
                    HStack(spacing: 18) {
                        if item.canPromote {
                            Button {
                                SessionHaptics.tap()
                                Task { await context.coreHost.promoteTrayItem(sessionID: sessionID, requestID: item.requestID) }
                            } label: {
                                Text(L10n.queueSendNow())
                                    .font(.subheadline)
                                    .foregroundStyle(DS.blue)
                                    .padding(.vertical, 6)
                                    .contentShape(Rectangle())
                            }
                            .buttonStyle(.plain)
                            .disabled(actionsDisabled)
                            .opacity(actionsDisabled ? 0.35 : 1)
                        }
                        if item.canRestore {
                            let restoreDisabled = item.serverBacked && actionsDisabled
                            Button {
                                SessionHaptics.tap()
                                Task {
                                    await context.coreHost.restoreTrayItem(sessionID: sessionID, requestID: item.requestID)
                                }
                            } label: {
                                Text(L10n.composerRestore())
                                    .font(.subheadline)
                                    .foregroundStyle(DS.blue)
                                    .padding(.vertical, 6)
                                    .contentShape(Rectangle())
                            }
                            .buttonStyle(.plain)
                            .disabled(restoreDisabled)
                            .opacity(restoreDisabled ? 0.35 : 1)
                        }
                    }
                    .padding(.top, 4)
                    .padding(.bottom, 4)
                }
            }

            trailingActions(item, expanded: expanded)
        }
        .padding(.leading, 14)
        .padding(.trailing, 4)
        .padding(.vertical, expanded || !reasonText.isEmpty ? 6 : 2)
        .frame(minHeight: 38)
        .background(expanded ? DS.fill : Color.clear)
        .accessibilityIdentifier("pending-tray-row-\(item.requestID)")
    }

    @ViewBuilder
    private func leadSlot(_ item: PBSessionTrayItemView) -> some View {
        if item.showSpinner {
            ProgressView()
                .controlSize(.mini)
                .tint(DS.ink2)
        } else if item.queueNumber > 0 {
            Text(verbatim: String(item.queueNumber))
                .font(.caption.monospacedDigit())
                .foregroundStyle(DS.ink2)
        } else {
            Color.clear.frame(width: 18, height: 1)
        }
    }

    private func rowTitle(_ item: PBSessionTrayItemView, expanded: Bool) -> some View {
        let sourceText = item.hasSourceLabel ? item.sourceLabel.rendered() : ""
        let trimmedText = item.text.trimmingCharacters(in: .whitespacesAndNewlines)
        let bodyWord: String = {
            if !trimmedText.isEmpty { return item.text }
            if item.imageCount > 0 { return L10n.imageCount(count: Int(item.imageCount)) }
            return L10n.queueOtherMessage()
        }()
        let textColor = item.foreignDimmed ? DS.ink2 : DS.ink

        return HStack(alignment: .firstTextBaseline, spacing: 6) {
            Group {
                if !sourceText.isEmpty {
                    Text(sourceText + " · ").foregroundStyle(DS.ink2) + Text(bodyWord).foregroundStyle(textColor)
                } else {
                    Text(bodyWord).foregroundStyle(textColor)
                }
            }
            .font(.subheadline)
            .lineLimit(expanded ? 6 : 1)
            .truncationMode(.tail)
            .multilineTextAlignment(.leading)

            if item.attachmentCount > 0 {
                HStack(spacing: 2) {
                    Image(systemName: "paperclip")
                        .font(.caption2)
                    Text(verbatim: String(item.attachmentCount))
                        .font(.caption)
                }
                .foregroundStyle(DS.ink2)
                .fixedSize()
                .accessibilityLabel(L10n.queueAttachmentsA11Y(count: Int(item.attachmentCount)))
            }
        }
    }

    @ViewBuilder
    private func trailingActions(_ item: PBSessionTrayItemView, expanded: Bool) -> some View {
        let disabled = item.networkActionsDisabled
        switch item.activity {
        case .canceling:
            HStack(spacing: 4) {
                ProgressView().controlSize(.mini).tint(DS.ink2)
                Text(L10n.queueCanceling())
                    .font(.caption)
                    .foregroundStyle(DS.ink2)
                    .lineLimit(1)
            }
            .padding(.horizontal, 8)
        case .sending:
            HStack(spacing: 4) {
                ProgressView().controlSize(.mini).tint(DS.ink2)
                Text(L10n.queueResuming())
                    .font(.caption)
                    .foregroundStyle(DS.ink2)
                    .lineLimit(1)
            }
            .padding(.horizontal, 8)
        case .unspecified, .idle, .UNRECOGNIZED:
            HStack(spacing: 0) {
                if item.canOpenTerminal {
                    Button {
                        onTerminal()
                    } label: {
                        Text(L10n.sessionGoTerminal())
                            .font(.subheadline)
                            .foregroundStyle(DS.blue)
                            .padding(.horizontal, 6)
                            .padding(.vertical, 8)
                            .contentShape(Rectangle())
                    }
                    .buttonStyle(.plain)
                }
                if item.canIgnore {
                    Button {
                        Task { await context.coreHost.ignoreTrayItem(sessionID: sessionID, requestID: item.requestID) }
                    } label: {
                        Text(L10n.queueIgnore())
                            .font(.subheadline)
                            .foregroundStyle(DS.blue)
                            .padding(.horizontal, 6)
                            .padding(.vertical, 8)
                            .contentShape(Rectangle())
                    }
                    .buttonStyle(.plain)
                    .disabled(disabled)
                    .opacity(disabled ? 0.35 : 1)
                }
                if item.canResume {
                    Button {
                        SessionHaptics.tap()
                        Task { await context.coreHost.resumeTrayItem(sessionID: sessionID, requestID: item.requestID) }
                    } label: {
                        Text(L10n.composerSend())
                            .font(.subheadline)
                            .foregroundStyle(DS.blue)
                            .padding(.horizontal, 6)
                            .padding(.vertical, 8)
                            .contentShape(Rectangle())
                    }
                    .buttonStyle(.plain)
                    .disabled(disabled)
                    .opacity(disabled ? 0.35 : 1)
                }
                if item.canRetry {
                    Button {
                        SessionHaptics.tap()
                        Task { await context.coreHost.retryTrayItem(sessionID: sessionID, requestID: item.requestID) }
                    } label: {
                        Text(L10n.commonRetry())
                            .font(.subheadline)
                            .foregroundStyle(DS.blue)
                            .padding(.horizontal, 6)
                            .padding(.vertical, 8)
                            .contentShape(Rectangle())
                    }
                    .buttonStyle(.plain)
                    .disabled(disabled)
                    .opacity(disabled ? 0.35 : 1)
                }
                if item.canCancel {
                    let cancelDisabled = item.serverBacked && disabled
                    Button {
                        SessionHaptics.tap()
                        Task { await context.coreHost.cancelTrayItem(sessionID: sessionID, requestID: item.requestID) }
                    } label: {
                        Image(systemName: "xmark")
                            .font(.system(size: 13, weight: .semibold))
                            .foregroundStyle(DS.ink2)
                            .frame(width: 36, height: expanded ? 28 : 38)
                            .contentShape(Rectangle())
                    }
                    .buttonStyle(.plain)
                    .accessibilityLabel(L10n.commonCancel())
                    .disabled(cancelDisabled)
                    .opacity(cancelDisabled ? 0.35 : 1)
                }
            }
        }
    }

    private var undoBar: some View {
        HStack(spacing: 8) {
            Text(tray.undo.label.rendered())
                .font(.footnote)
                .foregroundStyle(DS.ink2)
                .lineLimit(1)
            Spacer(minLength: 8)
            Button {
                SessionHaptics.tap()
                Task { await context.coreHost.undoCancel(sessionID: sessionID) }
            } label: {
                Text(L10n.queueUndo())
                    .font(.footnote)
                    .foregroundStyle(DS.blue)
                    .padding(.vertical, 4)
                    .contentShape(Rectangle())
            }
            .buttonStyle(.plain)
            .disabled(!tray.undo.canUndo)
            .opacity(tray.undo.canUndo ? 1 : 0.35)
            .accessibilityIdentifier("pending-tray-undo")
        }
        .padding(.horizontal, 14)
        .padding(.vertical, 8)
    }

    private func toneColor(_ tone: PBSessionTrayTone) -> Color {
        switch tone {
        case .red: return DS.red
        case .orange: return DS.orangeInk
        case .neutral, .unspecified, .UNRECOGNIZED: return DS.ink2
        }
    }
}

private struct MaxHeightProposal: Layout {
    let maxHeight: CGFloat

    func sizeThatFits(proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) -> CGSize {
        guard let child = subviews.first else { return .zero }
        let limit = max(0, maxHeight)
        let clamped = ProposedViewSize(
            width: proposal.width,
            height: proposal.height.map { min($0, limit) } ?? limit
        )
        let size = child.sizeThatFits(clamped)
        return CGSize(width: size.width, height: min(size.height, limit))
    }

    func placeSubviews(in bounds: CGRect, proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) {
        guard let child = subviews.first else { return }
        child.place(
            at: bounds.origin,
            anchor: .topLeading,
            proposal: ProposedViewSize(width: bounds.width, height: bounds.height)
        )
    }
}
