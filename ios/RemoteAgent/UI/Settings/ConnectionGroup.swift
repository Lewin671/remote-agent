import RACore
import RAProto
import SwiftUI

/// The state row: the graphic, the state word and under it what is added, all `ink2`.
struct DirectStatusRow: View {
    let stage: PBConnectionGroupStage
    let tone: PBConnectionGroupTone
    let status: String
    let detail: String
    var metric: String = ""

    var body: some View {
        let sub = [detail, metric].filter { !$0.isEmpty }.joined(separator: " · ")
        HStack(spacing: 10) {
            mark
            VStack(alignment: .leading, spacing: 1) {
                Text(status).font(.body).ink(DS.ink2)
                if !sub.isEmpty { Text(sub).font(.subheadline).ink(DS.ink2) }
            }
            .frame(maxWidth: .infinity, alignment: .leading)
        }
        .padding(.horizontal, 14).padding(.vertical, 11)
        .frame(minHeight: DS.minHit)
        .accessibilityElement(children: .combine)
    }

    @ViewBuilder private var mark: some View {
        switch stage {
        case .direct:
            Dot(DS.green)
        case .connecting, .measuring:
            Mark(Mark.progress, color: DS.ink, size: 13, relativeTo: .body)
        default:
            Dot(tone == .warn ? DS.orange : DS.faint)
        }
    }
}
