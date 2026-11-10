// What waits for you on the home page (design-system §6 「等你的事」「终端卡」):
// the most urgent approval as the dark card, decided right there, and the
// rest one to a row of the grey list, which open the session where its dock
// handles them.
//
// A decision is kept in one place for the account (`SessionStore.Resolve`):
// one made on the card is known on the session's page, and the other way
// round.
import Observation
import RACore
import RAProto
import SwiftUI

/// A waiting session's events reduced to what waits in it, kept up while its card or row is on screen.
@MainActor
@Observable
final class WaitingEvents {
    private(set) var derived = Derived()
    @ObservationIgnored private var projection = EventProjection()

    func follow(_ remote: Remote, _ sid: String, agent: PBAgent) async {
        for await snapshot in remote.events(sid).updates() {
            let events = snapshot.stateEvents
            derived = currentActions(projection.update(events, agent: agent), live: snapshot.live, events: events)
            // The feed keeps the latest for a reader that is behind; a frame passes between two.
            if (try? await Task.sleep(nanoseconds: 16_000_000)) == nil { return }
        }
    }
}

/// One row of the grey list: a question, a plan, a popup, or an approval after the first.
struct HomePendingRow: View {
    let state: RemoteState
    let session: SessionView
    let scope: HomeScope
    var first = true
    var last = true
    @Environment(Navigator.self) private var nav
    @Environment(AccountContext.self) private var context
    @State private var events = WaitingEvents()

    var body: some View {
        let d = events.derived
        let t = pendingRowText(
            state, session, scope: scope, approval: d.approvals.values.first,
            question: d.questions.values.first?.questions.first?.text)
        Button {
            nav.open(.session(id: session.id))
        } label: {
            PendingRowLayout(title: t.title, kind: t.kind, summary: t.summary, first: first, last: last)
        }
        .buttonStyle(RowHighlight())
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(t.spoken)
        .accessibilityAddTraits(.isButton)
        .task(id: session.id) { await events.follow(context.remote, session.id, agent: session.agent) }
    }
}

/// The most urgent approval as the dark card. Until the approval itself is read it is a row like the rest.
struct HomeApprovalCard: View {
    let state: RemoteState
    let session: SessionView
    let scope: HomeScope
    @Environment(AccountContext.self) private var context
    @State private var events = WaitingEvents()

    var body: some View {
        Group {
            if let a = events.derived.approvals.values.first {
                // Keyed by approval: the next one does not inherit this one's opened command or reason.
                ApprovalCardBody(state: state, session: session, approval: a).id(a.approvalID)
                    .padding(.horizontal, DS.Space.card)
            } else {
                HomePendingRow(state: state, session: session, scope: scope)
            }
        }
        .task(id: session.id) { await events.follow(context.remote, session.id, agent: session.agent) }
    }
}

/// The card itself (design-system §6 「终端卡」): the head, the title that
/// opens the session, 「项目 · 机器」, the command, why, and the buttons. A
/// command not shown whole cannot be allowed: until it is opened the card
/// offers 不允许 and 看完整命令. 不允许 asks for a reason in place. Once
/// decided the card is one line of where that stands.
private struct ApprovalCardBody: View {
    let state: RemoteState
    let session: SessionView
    let approval: PBApprovalRequested
    @Environment(Navigator.self) private var nav
    @Environment(AccountContext.self) private var context
    @Environment(AppModel.self) private var model
    @State private var full = HomeLaunch.value("-homeCard") == "full"
    @State private var denying = HomeLaunch.value("-homeCard") == "deny"
    @State private var more = 0
    @State private var total = 0
    @State private var chosen: Bool?
    @FocusState private var reasonFocus: Bool

    /// Whole lines of the command before 看完整命令; two while a reason is written under it.
    static let lines = 5

    var body: some View {
        Ticking(every: 15) { now in
            let t = approvalCardText(state, session, approval: approval, timeoutNote: true, now: now)
            let r = context.sessionStore.resolve(approval.approvalID)
            if isSkeleton(approval) {
                DarkCard {
                    DarkCardHead(kind: t.kind, right: t.time, hot: t.hot)
                    title(t)
                    Text(skeletonMessage).font(.subheadline).foregroundStyle(DS.ink2).padding(.top, 12)
                }
            } else if r.decided || r.running || r.frozen || !r.error.isEmpty {
                submitted(t, r)
            } else {
                DarkCard { open(t, now: now) }
            }
        }
        .debugSignal("home.allow") { decide(true) }
    }

    private func title(_ t: ApprovalCardText) -> some View {
        Button {
            nav.open(.session(id: session.id))
        } label: {
            VStack(alignment: .leading, spacing: 2) {
                HStack(spacing: 4) {
                    Text(t.title).font(.title3.bold()).foregroundStyle(DS.ink).lineLimit(2)
                    Image(systemName: "chevron.right").font(.subheadline.weight(.semibold)).foregroundStyle(DS.ink3)
                }
                Text(t.place).font(.footnote).foregroundStyle(DS.ink2).lineLimit(1)
            }
            .frame(maxWidth: .infinity, alignment: .leading)
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .padding(.top, 12)
        .accessibilityElement(children: .ignore)
        .accessibilityLabel([t.title, t.place.replacingOccurrences(of: " · ", with: "，")].joined(separator: "，"))
        .accessibilityHint("打开会话")
        .accessibilityAddTraits(.isButton)
    }

    @ViewBuilder private func open(_ t: ApprovalCardText, now: Int64) -> some View {
        let truncated = approvalTruncated(approval)
        // Out of reach of the server nothing can be decided, nor once the time is up.
        let locked = state.unlinked || (approval.hasDeadline && approval.deadline.seconds * 1000 <= now)
        // Until the command is laid out it is not known to be whole: nothing is allowed before.
        let cut = more > 0 && !full
        let measured = total > 0
        DarkCardHead(kind: t.kind, right: t.time, hot: t.hot)
        title(t)
        CommandBlock(approvalBody(approval), lines: denying ? 2 : full ? nil : Self.lines, more: $more, total: $total)
            .padding(.top, 14)
            .accessibilityLabel("命令：" + approvalBody(approval))
        if !denying {
            if cut {
                Text("还有 \(more) 行没显示").font(.footnote).foregroundStyle(DS.ink2).padding(.top, 8)
            } else if full && total > Self.lines {
                HStack(spacing: 4) {
                    Text("共 \(total) 行").font(.footnote).foregroundStyle(DS.ink2)
                    LinkButton("收起") { withAnimation(.snappy) { full = false } }.font(.footnote)
                }
                .padding(.vertical, -8).padding(.top, 8)
            }
        }
        if truncated { Text(approvalTruncatedMessage).font(.footnote).foregroundStyle(DS.red).padding(.top, 8) }
        if !denying && !approval.description_p.isEmpty {
            Text(approvalDisplay(approval.description_p)).font(.subheadline).foregroundStyle(DS.ink2).lineLimit(2).padding(.top, 12)
        }
        if denying {
            TextField("说说该怎么做，可以不填", text: reason, axis: .vertical)
                .lineLimit(1...4)
                .font(.body)
                .foregroundStyle(DS.ink)
                .focused($reasonFocus)
                .padding(.horizontal, 14).padding(.vertical, 13)
                .background(Color.white.opacity(0.1), in: RoundedRectangle(cornerRadius: DS.Radius.command, style: .continuous))
                .padding(.top, 12)
                .accessibilityLabel("不允许的理由")
                .onAppear { reasonFocus = true }
        }
        buttons(cut: cut, locked: locked, truncated: truncated || !measured)
    }

    private var reason: Binding<String> {
        let store = context.sessionStore
        let id = approval.approvalID
        return Binding(get: { store.reason(id) }, set: { store.setReason(id, $0) })
    }

    @ViewBuilder private func buttons(cut: Bool, locked: Bool, truncated: Bool) -> some View {
        let always = capabilities(state.machines[session.machineId])?.approvalAlways == true
        ViewThatFits(in: .horizontal) {
            HStack(spacing: 8) { buttonRow(cut: cut, locked: locked, truncated: truncated) }
            VStack(spacing: 8) { buttonRow(cut: cut, locked: locked, truncated: truncated) }
        }
        .padding(.top, 16)
        if always && !denying && !cut && !truncated {
            LinkButton("允许，这类请求以后不再问") { decide(true, always: true) }
                .font(.subheadline)
                .tint(Color(red: 0.42, green: 0.71, blue: 1))
                .disabled(locked)
                .frame(maxWidth: .infinity)
                .padding(.bottom, -8)
        }
    }

    @ViewBuilder private func buttonRow(cut: Bool, locked: Bool, truncated: Bool) -> some View {
        if denying {
            Button("取消") { denying = false }.buttonStyle(.darkCard())
            Button("不允许") { decide(false, message: context.sessionStore.reason(approval.approvalID).jvmTrim()) }
                .buttonStyle(.darkCard(primary: true)).disabled(locked)
        } else {
            Button("不允许") { denying = true }.buttonStyle(.darkCard()).disabled(locked)
            if cut {
                Button("看完整命令") { withAnimation(.snappy) { full = true } }.buttonStyle(.darkCard(primary: true))
            } else {
                Button("允许") { decide(true) }.buttonStyle(.darkCard(primary: true)).disabled(locked || truncated)
            }
        }
    }

    /// Submitted: one line of where the decision stands; the next thing that waits comes up once this one is gone from the session.
    private func submitted(_ t: ApprovalCardText, _ r: SessionStore.Resolve) -> some View {
        let allow = (r.running ? chosen : nil) ?? r.allow
        let failed = !r.running && (r.frozen || !r.error.isEmpty)
        let why: String = r.expired
            ? "不确定「\(allow ? "允许" : "拒绝")」有没有送到，它已过期，不会再重发。审批超时后会自动拒绝"
            : "没送到电脑：\(r.error.isEmpty ? "结果未知" : r.error)"
        return DarkCard {
            HStack(alignment: .firstTextBaseline, spacing: 8) {
                StatusMark(.waiting)
                Text(t.kind).fontWeight(.semibold).foregroundStyle(DS.orange).lineLimit(1).layoutPriority(1)
                Text(failed ? why : allow ? "已允许" : "已拒绝")
                    .foregroundStyle(failed ? DS.red : DS.ink2)
                    .frame(maxWidth: .infinity, alignment: .trailing)
                    .multilineTextAlignment(.trailing)
                if failed && !r.expired {
                    LinkButton("重试") { retry() }.fontWeight(.semibold).tint(Color(red: 0.42, green: 0.71, blue: 1))
                }
            }
            .font(.footnote)
            .accessibilityElement(children: .combine)
        }
    }

    private func decide(_ allow: Bool, message: String = "", always: Bool = false) {
        guard !isSkeleton(approval), !allow || !approvalTruncated(approval) else { return }
        chosen = allow
        if allow { SessionHaptics.decide() } else { Haptics.destructive() }
        let sent = context.sessionStore.decide(
            context.remote, machineId: session.machineId, sid: session.id, approvalId: approval.approvalID, allow: allow,
            message: message, always: always)
        Task { if await sent.value == true { Haptics.confirm() } }
    }

    private func retry() {
        context.sessionStore.retryDecision(context.remote, machineId: session.machineId, sid: session.id, approvalId: approval.approvalID)
    }
}

/// How a row answers a touch: its ground darkens under the finger, as a system list row's does.
struct RowHighlight: ButtonStyle {
    func makeBody(configuration: Configuration) -> some View {
        configuration.label
            .opacity(configuration.isPressed ? 0.6 : 1)
            .animation(.easeOut(duration: 0.12), value: configuration.isPressed)
    }
}
