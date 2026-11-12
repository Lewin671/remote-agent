import RACore
import RAProto
import SwiftUI

/// The same file entry in an editable draft, an outgoing bubble and recorded history.
struct FileChip: View {
    let name: String
    let size: Int64
    var state: PendingImage.State? = nil
    var retry: (() -> Void)? = nil
    var remove: (() -> Void)? = nil

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
                Text(fileChipName(name, maxCharacters: 80)).font(.subheadline).ink(DS.ink2).lineLimit(1).truncationMode(.middle)
                    .accessibilityLabel(name)
                Text(fileSize(size) + status).font(.footnote).ink(DS.ink2)
                if case .uploading(let progress) = state {
                    ProgressView(value: Double(progress)).tint(DS.blue)
                        .accessibilityLabel(L10n.attachmentFileProgress())
                }
                if case .failed(let reason) = state {
                    Text(reason).font(.footnote).ink(DS.red).fixedSize(horizontal: false, vertical: true)
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
    let files: [PBSentFile]
    var body: some View {
        if !files.isEmpty {
            VStack(alignment: .leading, spacing: 6) {
                ForEach(Array(files.enumerated()), id: \.offset) { _, file in
                    FileChip(name: file.name, size: file.sizeBytes)
                }
            }
            .padding(.bottom, 4)
        }
    }
}
