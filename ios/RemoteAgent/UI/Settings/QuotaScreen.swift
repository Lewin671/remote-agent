import RACore
import RAProto
import SwiftUI

/// 账户额度: each CLI's account usage on a computer, apart, as the daemon
/// reads it (architecture 「账户额度」, Android's QuotaScreen). What is not
/// known is said, never shown as 0%; an offline computer's or a failed
/// read's numbers are the last record, and a reset time that passed only
/// waits for an update. Nothing here switches CLI or model.
struct QuotaScreen: View {
    let machine: String
    @Environment(AppModel.self) private var model

    var body: some View {
        let state = model.state
        let m = state.machines[machine]
        let status = m.flatMap { machineStatus($0, state: state, now: nowMs()) }
        SettingsPage {
            if let m {
                Ticking { now in groups(m, state: state, now: now) }
            } else {
                PageNote("这台电脑已经移除了")
            }
        }
        .navBar(
            "账户额度",
            subtitle: m.map { _ in machineLabel(state, id: machine) + (status.map { " · " + ($0.online ? "在线" : "不在线") } ?? "") })
    }

    @ViewBuilder private func groups(_ m: MachineView, state: RemoteState, now: Int64) -> some View {
        let groups = quotaGroups(asKnown(m, state: state), now: now)
        // Not connected, the connection bar says so and the numbers are what was last heard.
        if !state.unlinked && !m.online { GroupNote("电脑不在线，显示上次记录", color: DS.orangeInk) }
        ForEach(Array(groups.enumerated()), id: \.offset) { _, g in
            if !g.subgroups.isEmpty {
                ForEach(Array(g.subgroups.enumerated()), id: \.offset) { index, sub in
                    GroupLabel(sub.title + (g.lastRecord && index == 0 ? " · 上次记录" : ""))
                    if index == 0 && g.status == .failed {
                        GroupNote("没能读取 \(agentName(g.agent)) 额度" + (g.detail.isEmpty ? "" : "：\(g.detail)"), color: DS.red)
                    }
                    GroupCard {
                        for row in sub.rows { QuotaWindow(row: row) }
                    }
                }
            } else {
                GroupLabel(g.title + (g.lastRecord ? " · 上次记录" : ""))
                // Under the label it belongs to: with no last record there is no card, and the failure stands in its place.
                if g.status == .failed {
                    GroupNote("没能读取 \(agentName(g.agent)) 额度" + (g.detail.isEmpty ? "" : "：\(g.detail)"), color: DS.red)
                }
                if g.status == .unsupported || g.status == .waiting || !g.rows.isEmpty {
                    GroupCard {
                        if g.status == .unsupported {
                            QuotaState(
                                "暂不支持读取额度",
                                g.detail.or("这台电脑上的 remote-agent 读不到 \(agentName(g.agent)) 的额度。")
                                    + (agentInfo(g.agent).usageHint ?? ""))
                        } else if g.status == .waiting {
                            QuotaState("暂未获取额度", "等待 \(agentName(g.agent)) 返回额度数据")
                        }
                        for row in g.rows { QuotaWindow(row: row) }
                    }
                }
            }
            // When it was heard, once for the group rather than on every row.
            if !g.rows.isEmpty && !g.received.isEmpty { GroupNote(g.received) }
            // Used up or close to it; nothing switches CLI or model by itself.
            let banner = quotaBanner(g)
            if !banner.isEmpty { GroupNote("\(banner)，不会自动切换 CLI 或模型", color: DS.orangeInk) }
        }
        if groups.isEmpty { GroupNote("这台电脑还没有报告它装了哪些 CLI") }
        GroupNote("时间按北京时间显示。额度属于 CLI 登录账户，该账户的其他使用也可能计入。")
    }
}

/// What a group says instead of windows: not supported, or not heard yet.
private struct QuotaState: View {
    private let title: String
    private let text: String

    init(_ title: String, _ text: String) {
        self.title = title
        self.text = text
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 2) {
            Text(title).font(.body.weight(.semibold)).ink(DS.ink)
            Text(text).font(.footnote).ink(DS.ink2)
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(.horizontal, 14).padding(.vertical, 10)
        .accessibilityElement(children: .combine)
    }
}

/// One usage window: its name, how much is used, a bar, and when it resets.
private struct QuotaWindow: View {
    let row: QuotaRow
    @Environment(\.dynamicTypeSize) private var typeSize

    var body: some View {
        let ink = row.tone == .full || row.tone == .warn ? DS.orangeInk : DS.ink2
        let fill: Color = switch row.tone {
        case .stale: DS.ink3
        case .normal: DS.blue
        default: DS.orange
        }
        let sub = [row.reset, row.note].filter { !$0.isEmpty }.joined(separator: " · ")
        // Side by side, or the amount under the name when large text leaves no room for both.
        let head = typeSize.isAccessibilitySize ? AnyLayout(VStackLayout(alignment: .leading, spacing: 2)) : AnyLayout(HStackLayout(spacing: 8))
        VStack(alignment: .leading, spacing: 0) {
            head {
                Text(row.label).font(.body).ink(DS.ink).frame(maxWidth: .infinity, alignment: .leading)
                Text(row.left).font(.subheadline).ink(ink)
            }
            if let percent = row.percent {
                GeometryReader { geo in
                    Capsule().fill(DS.fill2)
                        .overlay(alignment: .leading) {
                            Capsule().fill(fill).frame(width: geo.size.width * min(1, max(0, percent / 100)))
                        }
                }
                .frame(height: 6)
                .padding(.top, 6)
                .accessibilityHidden(true)
            }
            if !sub.isEmpty { Text(sub).font(.footnote).ink(DS.ink2).padding(.top, 8) }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(.horizontal, 14).padding(.vertical, 10)
        .accessibilityElement(children: .combine)
    }
}
