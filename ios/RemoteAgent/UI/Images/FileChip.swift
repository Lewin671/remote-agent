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
        case .processing: return " · 准备中"
        case .uploading(let progress): return " · 上传 \(Int((progress * 100).rounded()))%"
        case .failed: return " · 上传失败"
        default: return ""
        }
    }

    var body: some View {
        HStack(spacing: 8) {
            Ic(Lucide.file, size: 18, color: DS.ink2)
            VStack(alignment: .leading, spacing: 2) {
                Text(fileChipName(name, maxCharacters: 80)).type(T.sm, DS.ink2).lineLimit(1).truncationMode(.middle)
                    .accessibilityLabel(name)
                Text(fileSize(size) + status).type(T.meta, DS.ink2)
                if case .uploading(let progress) = state {
                    ProgressView(value: Double(progress)).tint(DS.blue)
                        .accessibilityLabel("文件上传进度")
                }
                if case .failed(let reason) = state {
                    Text(reason).type(T.meta, DS.red).fixedSize(horizontal: false, vertical: true)
                }
            }
            .frame(maxWidth: .infinity, alignment: .leading)
            if case .failed = state, let retry {
                Button("重试", action: retry).type(T.meta, DS.blue).frame(minWidth: DS.minHit, minHeight: DS.minHit)
            }
            if let remove {
                Button(action: remove) { Ic(Lucide.x, size: 16, color: DS.ink2).hitTarget() }
                    .accessibilityLabel("移除文件 \(name)")
            }
        }
        .buttonStyle(PressStyle())
        .padding(.horizontal, 8).padding(.vertical, 4)
        .background(DS.fill, in: RoundedRectangle(cornerRadius: Theme.Radius.sm))
        .overlay { RoundedRectangle(cornerRadius: Theme.Radius.sm).strokeBorder(DS.sep, lineWidth: 1) }
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
