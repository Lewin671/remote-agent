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
            Ic(Lucide.file, size: 18, color: Theme.fg2)
            VStack(alignment: .leading, spacing: 2) {
                Text(fileChipName(name, maxCharacters: 80)).type(T.sm, Theme.fg2).lineLimit(1).truncationMode(.middle)
                    .accessibilityLabel(name)
                Text(fileSize(size) + status).type(T.meta, Theme.muted)
                if case .uploading(let progress) = state {
                    ProgressView(value: Double(progress)).tint(Theme.blue)
                        .accessibilityLabel("文件上传进度")
                }
                if case .failed(let reason) = state {
                    Text(reason).type(T.meta, Theme.red).fixedSize(horizontal: false, vertical: true)
                }
            }
            .frame(maxWidth: .infinity, alignment: .leading)
            if case .failed = state, let retry {
                Button("重试", action: retry).type(T.meta, Theme.blue).frame(minWidth: Theme.minHit, minHeight: Theme.minHit)
            }
            if let remove {
                Button(action: remove) { Ic(Lucide.x, size: 16, color: Theme.muted).hitTarget() }
                    .accessibilityLabel("移除文件 \(name)")
            }
        }
        .buttonStyle(PressStyle())
        .padding(.horizontal, 8).padding(.vertical, 4)
        .background(Theme.soft, in: RoundedRectangle(cornerRadius: Theme.Radius.sm))
        .overlay { RoundedRectangle(cornerRadius: Theme.Radius.sm).strokeBorder(Theme.line, lineWidth: 1) }
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
