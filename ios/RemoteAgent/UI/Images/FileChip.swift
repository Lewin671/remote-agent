import RACore
import RAProto
import SwiftUI

/// The same file entry in an editable draft (driven by `PBAttachmentDraftItemView`),
/// an expanded outbox row, and recorded history (`PBSessionFileRefView`).
struct FileChip: View {
    let name: String
    let head: String
    let tail: String
    let subtitle: String
    let errorText: String
    let progress: Double?
    let removeLabel: String
    var pause: (() -> Void)? = nil
    var resume: (() -> Void)? = nil
    var retry: (() -> Void)? = nil
    var remove: (() -> Void)? = nil

    @Environment(\.dynamicTypeSize) private var typeSize

    init(
        name: String,
        head: String = "",
        tail: String = "",
        sizeText: String
    ) {
        self.name = name
        self.head = head
        self.tail = tail
        self.subtitle = sizeText
        self.errorText = ""
        self.progress = nil
        self.removeLabel = L10n.attachmentRemoveFile(name: name)
    }

    init(
        item: PBAttachmentDraftItemView,
        pause: (() -> Void)? = nil,
        resume: (() -> Void)? = nil,
        retry: (() -> Void)? = nil,
        remove: (() -> Void)? = nil
    ) {
        self.name = item.displayName
        self.head = item.nameHead
        self.tail = item.nameTail
        let size = item.hasSizeText ? item.sizeText.rendered() : ""
        let duration = item.durationText
        let status = item.hasStatusText ? item.statusText.rendered() : ""
        self.subtitle = [size, duration, status].filter { !$0.isEmpty }.joined(separator: " · ")
        self.errorText = (item.state == .failed && item.hasFileErrorText) ? item.fileErrorText.rendered() : ""
        switch item.state {
        case .uploading, .paused, .waitingReconnect:
            self.progress = min(max(item.progress, 0), 1)
        default:
            self.progress = nil
        }
        self.removeLabel = item.hasRemoveA11Y ? item.removeA11Y.rendered() : ""
        self.pause = pause
        self.resume = resume
        self.retry = retry
        self.remove = remove
    }

    var body: some View {
        HStack(spacing: 8) {
            SymbolImage("doc", size: 18, color: DS.ink2)
            VStack(alignment: .leading, spacing: 2) {
                if !head.isEmpty || !tail.isEmpty {
                    MiddleEllipsisName(name: name, head: head, tail: tail, large: typeSize >= .xxxLarge, font: .subheadline, tracking: 0)
                        .foregroundStyle(DS.ink2)
                        .accessibilityLabel(name)
                } else {
                    Text(name).font(.subheadline).ink(DS.ink2).lineLimit(1).truncationMode(.middle)
                        .accessibilityLabel(name)
                }
                if !subtitle.isEmpty {
                    Text(subtitle).font(.footnote).ink(DS.ink2)
                }
                if let progress {
                    ProgressView(value: progress).tint(DS.blue)
                        .accessibilityLabel(L10n.attachmentFileProgress())
                }
                if !errorText.isEmpty {
                    Text(errorText).font(.footnote).ink(DS.red).fixedSize(horizontal: false, vertical: true)
                }
            }
            .frame(maxWidth: .infinity, alignment: .leading)
            if let pause {
                Button(L10n.sharedFileActionPause(), action: pause)
                    .font(.footnote).ink(DS.blue).frame(minWidth: DS.minHit, minHeight: DS.minHit)
            }
            if let resume {
                Button(L10n.sharedFileActionResume(), action: resume)
                    .font(.footnote).ink(DS.blue).frame(minWidth: DS.minHit, minHeight: DS.minHit)
            }
            if let retry {
                Button(L10n.commonRetry(), action: retry)
                    .font(.footnote).ink(DS.blue).frame(minWidth: DS.minHit, minHeight: DS.minHit)
            }
            if let remove {
                Button(action: remove) { SymbolImage("xmark", size: 16, color: DS.ink2).hitTarget() }
                    .accessibilityLabel(removeLabel)
            }
        }
        .buttonStyle(PressStyle())
        .padding(.horizontal, 8).padding(.vertical, 4)
        .background(DS.fill, in: RoundedRectangle(cornerRadius: DS.Radius.small))
        .overlay { RoundedRectangle(cornerRadius: DS.Radius.small).strokeBorder(DS.sep, lineWidth: 1) }
        .accessibilityElement(children: .contain)
    }
}

struct FileChips: View {
    private let refs: [PBSessionFileRefView]

    init(refs: [PBSessionFileRefView]) {
        self.refs = refs
    }

    var body: some View {
        if !refs.isEmpty {
            VStack(alignment: .leading, spacing: 6) {
                ForEach(Array(refs.enumerated()), id: \.offset) { _, ref in
                    let name = ref.displayName.isEmpty ? ref.name : ref.displayName
                    let sizeText = ref.hasSizeText ? ref.sizeText.rendered() : ""
                    FileChip(name: name, head: ref.nameHead, tail: ref.nameTail, sizeText: sizeText)
                }
            }
            .padding(.bottom, 4)
        }
    }
}
