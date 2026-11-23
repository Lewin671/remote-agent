import RACore
import RAProto
import SwiftUI

/// The same file entry in an editable draft, an outgoing bubble and recorded history.
struct FileChip: View {
    let name: String
    let head: String
    let tail: String
    let sizeText: String
    var state: PendingImage.State? = nil
    var retry: (() -> Void)? = nil
    var remove: (() -> Void)? = nil

    @Environment(\.dynamicTypeSize) private var typeSize

    init(
        name: String,
        head: String = "",
        tail: String = "",
        sizeText: String,
        state: PendingImage.State? = nil,
        retry: (() -> Void)? = nil,
        remove: (() -> Void)? = nil
    ) {
        self.name = name
        self.head = head
        self.tail = tail
        self.sizeText = sizeText
        self.state = state
        self.retry = retry
        self.remove = remove
    }

    init(
        name: String,
        size: Int64,
        state: PendingImage.State? = nil,
        retry: (() -> Void)? = nil,
        remove: (() -> Void)? = nil
    ) {
        self.init(name: name, sizeText: fileSize(size), state: state, retry: retry, remove: remove)
    }

    private var status: String {
        switch state {
        case .processing: return L10n.attachmentIosPreparing()
        case .uploading(let progress): return L10n.attachmentIosUploadProgress(percent: Int(Int((progress * 100).rounded())))
        case .failed: return L10n.attachmentIosUploadFailed()
        default: return ""
        }
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
                Text(sizeText + status).font(.footnote).ink(DS.ink2)
                if case .uploading(let progress) = state {
                    ProgressView(value: Double(progress)).tint(DS.blue)
                        .accessibilityLabel(L10n.attachmentFileProgress())
                }
                if case .failed(let reason) = state {
                    Text(reason.message).font(.footnote).ink(DS.red).fixedSize(horizontal: false, vertical: true)
                }
            }
            .frame(maxWidth: .infinity, alignment: .leading)
            if case .failed = state, let retry {
                Button(L10n.commonRetry(), action: retry).font(.footnote).ink(DS.blue).frame(minWidth: DS.minHit, minHeight: DS.minHit)
            }
            if let remove {
                Button(action: remove) { SymbolImage("xmark", size: 16, color: DS.ink2).hitTarget() }
                    .accessibilityLabel(L10n.attachmentRemoveFile(name: name))
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
