// The home page as the list it draws (client-v27, architecture §8 「主页的范围」):
// which lines there are and in what order, what each row writes, what ends
// the list, and which pages of the listing it wants meanwhile. The part of
// Android's ui/Home.kt that decides and draws nothing, so that the screen
// only lays the lines out and the rules are tested here.
import Foundation
import RAProto

/// How much of each part of the timeline the page shows; reset with the scope.
public struct HomeShown: Equatable, Sendable {
    /// 「还有 N 项待处理」 was opened.
    public var allAttention: Bool
    /// 「还有 N 个进行中」 was opened.
    public var allRunning: Bool
    /// How many ended sessions are rendered; reaching the end adds `endedPage`.
    public var ended: Int

    public init(allAttention: Bool = false, allRunning: Bool = false, ended: Int = endedPage) {
        self.allAttention = allAttention
        self.allRunning = allRunning
        self.ended = ended
    }
}

/// Why the page has nothing to list.
public enum HomeEmpty: Equatable, Sendable {
    /// Nothing was read yet: grey bars.
    case loading
    /// The account has no session and no project.
    case account(AccountEmpty)
    /// The project chosen as the scope has no session.
    case scope
}

/// What an account without sessions is told to do next.
public enum AccountEmpty: Equatable, Sendable {
    /// No computer has joined: the phone app adds one.
    case noMachines
    /// Computers joined, none is online.
    case noneOnline
    /// A computer is online, by its name: 开始第一个会话.
    case ready(machine: String)
    /// The server is out of reach, so which computers are online is not known; the connection bar says why.
    case unlinked
}

/// What ends the list.
public enum HomeTail: Equatable, Sendable {
    /// A count: 「共 71 个」, 「3 个会话的标题里有「接口」」.
    case text(String)
    /// Earlier pages are wanted and on their way, waiting for the connection, or failed (`earlierTail`); `retry` when a tap tries again.
    case earlier(String, retry: Bool)
}

/// One line of the home page's list. A session keeps its ID in whichever
/// part it is, so one that moves (an approval answered, a session ended)
/// is the same line to the list and slides to its new place.
public enum HomeItem: Equatable, Sendable, Identifiable {
    case label(id: String, title: String, count: Int?)
    case attention(SessionView)
    case moreAttention(Int)
    case running(SessionView, last: Bool)
    case moreRunning(Int)
    case ended(SessionView)
    case tail(HomeTail)

    public var id: String {
        switch self {
        case .label(let id, _, _): return "h:" + id
        case .attention(let s), .running(let s, _), .ended(let s): return "s:" + s.id
        case .moreAttention: return "more:attention"
        case .moreRunning: return "more:running"
        case .tail: return "tail"
        }
    }
}

/// The home page for a scope and what is typed in the filter.
public struct HomeList: Equatable, Sendable {
    public var items: [HomeItem]
    public var empty: HomeEmpty?
    /// The title filter is offered: more than `filterOver` sessions in the scope, or something is typed.
    public var showFilter: Bool
    /// Every page left is to be fetched: a filter has to see every title.
    public var wantAll: Bool
    /// The next page is to be fetched: more ended sessions are to be shown than were.
    public var wantPage: Bool
    /// Reaching the end of the list shows the next `endedPage` ended sessions.
    public var grows: Bool
    /// Which of project, CLI and machine a row's third line writes.
    public var meta: RowMeta
    /// How many ended sessions are held for the scope, for the effect that grows the list.
    public var endedHeld: Int

    public init(
        items: [HomeItem], empty: HomeEmpty?, showFilter: Bool, wantAll: Bool, wantPage: Bool, grows: Bool, meta: RowMeta,
        endedHeld: Int
    ) {
        self.items = items
        self.empty = empty
        self.showFilter = showFilter
        self.wantAll = wantAll
        self.wantPage = wantPage
        self.grows = grows
        self.meta = meta
        self.endedHeld = endedHeld
    }
}

private func groupId(_ g: EndedGroup) -> String {
    switch g {
    case .today: return "today"
    case .yesterday: return "yesterday"
    case .week: return "week"
    case .month(let year, let month): return "month:\(year)-\(month)"
    }
}

/// What an account with no session and no project is told.
public func accountEmpty(_ state: RemoteState) -> AccountEmpty {
    if state.machines.isEmpty { return .noMachines }
    // Out of reach of the server every computer reads as offline, which says nothing about them (architecture §9 「断线时显示什么」).
    if state.unlinked { return .unlinked }
    let online = state.machines.values.filter(\.online).sorted { jvmLess($0.id, $1.id) }
    guard let first = online.first else { return .noneOnline }
    return .ready(machine: machineName(first, id: first.id))
}

/// The home page's list (Android's HomeScreen): 需要处理 for the whole
/// account, at most `attentionMax` cards before 「还有 N 项待处理」; 进行中, at
/// most `runningMax` rows; the ended sessions by day, `shown.ended` of them;
/// and the line that ends the list. A filter shows every match.
public func homeList(
    _ state: RemoteState, scope: HomeScope, query: String = "", shown: HomeShown = HomeShown(), now: Int64,
    zone: TimeZone = .current
) -> HomeList {
    let filtering = !query.jvmIsBlank
    let tl = homeTimeline(state, scope: scope, query: query, zone: zone)
    // Without any project, removed ones included, there is nothing to scope by.
    let anyProject = !projects(state).isEmpty
    let wantAll = filtering && state.moreSessions
    let wantPage = !filtering && needsEndedPage(state, held: tl.ended, want: shown.ended, total: tl.endedTotal)

    var empty: HomeEmpty?
    if !state.loaded && state.sessions.isEmpty {
        empty = .loading
    } else if state.sessions.isEmpty && !anyProject {
        empty = .account(accountEmpty(state))
    } else if scope != .all && tl.total == 0 && !filtering {
        empty = .scope
    }

    var items = [HomeItem]()
    if !tl.attention.isEmpty {
        items.append(.label(id: "attention", title: "需要处理", count: tl.attention.count))
        let cards = shown.allAttention || filtering ? tl.attention : Array(tl.attention.prefix(attentionMax))
        items += cards.map { .attention($0) }
        if cards.count < tl.attention.count { items.append(.moreAttention(tl.attention.count - cards.count)) }
    }
    if !tl.running.isEmpty {
        items.append(.label(id: "running", title: "进行中", count: tl.running.count))
        let rows = shown.allRunning || filtering ? tl.running : Array(tl.running.prefix(runningMax))
        items += rows.enumerated().map { .running($1, last: $0 == rows.count - 1) }
        if rows.count < tl.running.count { items.append(.moreRunning(tl.running.count - rows.count)) }
    }
    let ended = Array(tl.ended.prefix(max(0, shown.ended)))
    for (group, sessions) in groupEnded(ended, now: now, zone: zone) {
        items.append(.label(id: groupId(group), title: groupTitle(group, now: now, zone: zone), count: nil))
        items += sessions.map { .ended($0) }
    }
    // More to show: held here and not rendered yet, or still on the server.
    let held = ended.count < tl.ended.count
    let more = held || (!filtering && state.moreSessions && tl.ended.count < tl.endedTotal)
    // What a filter matched is only known once every page is here.
    let tail: String? = filtering ? (wantAll ? nil : filterTail(count: tl.size, query: query)) : endedTail(total: tl.endedTotal, more: more)
    // The listing's first page could not be fetched: said here with its retry, whatever the list wants.
    let listFailed = !state.sessionsListed && !state.sessionsError.isEmpty
    if tail != nil || more || wantAll || wantPage || listFailed {
        // While pages are on their way the line says so, unless it already counts the scope's sessions;
        // a page that failed is said either way, with its retry.
        if listFailed || ((wantAll || wantPage) && (tail == nil || !state.sessionsError.isEmpty)) {
            items.append(.tail(.earlier(earlierTail(state), retry: state.conn == .online && !state.sessionsError.isEmpty)))
        } else {
            items.append(.tail(.text(tail ?? "")))
        }
    }
    return HomeList(
        items: items, empty: empty, showFilter: tl.total > filterOver || !query.isEmpty, wantAll: wantAll, wantPage: wantPage,
        // Once the ones before them are all rendered; those not fetched yet are asked for by `wantPage`.
        grows: more && shown.ended <= tl.ended.count, meta: tl.meta, endedHeld: tl.ended.count)
}

// --- what a row writes ---

/// A session that has not ended, as its row writes it (design-system §6 会话行).
public struct SessionRowText: Equatable, Sendable {
    public var title: String
    public var time: String
    /// The status word, in its tone's colour.
    public var status: Status
    /// After the status word; empty when the word already says it.
    public var preview: String
    /// The third line; empty for none.
    public var meta: String

    public init(title: String, time: String, status: Status, preview: String, meta: String) {
        self.title = title
        self.time = time
        self.status = status
        self.preview = preview
        self.meta = meta
    }

    /// The row as one sentence for VoiceOver: the title first, then its state, what it last said, where it runs and when.
    public var spoken: String {
        [title, status.text, preview, meta.replacingOccurrences(of: " · ", with: "，"), time].filter { !$0.isEmpty }.joined(separator: "，")
    }
}

public func sessionRowText(
    _ state: RemoteState, _ s: SessionView, meta: RowMeta, now: Int64, zone: TimeZone = .current
) -> SessionRowText {
    let st = shownStatus(state, s)
    let pv = previewOf(s, status: st)
    return SessionRowText(
        title: titleOf(s, zone: zone), time: whenText(s.updatedMs, now: now, zone: zone), status: st,
        // The status word already says a plan waits.
        preview: pv == st.text ? "" : pv, meta: metaLine(state, s, meta: meta))
}

/// An ended session on its one line: the title, and at the right when it ended, after the project where the list spans projects.
public struct EndedRowText: Equatable, Sendable {
    public var title: String
    public var project: String
    public var time: String

    public init(title: String, project: String, time: String) {
        self.title = title
        self.project = project
        self.time = time
    }

    /// 「blog · 7:50」, or either alone.
    public var right: String { [project, time].filter { !$0.isEmpty }.joined(separator: " · ") }

    public var spoken: String { [title, "已结束", project, time].filter { !$0.isEmpty }.joined(separator: "，") }
}

public func endedRowText(_ s: SessionView, project: Bool, now: Int64, zone: TimeZone = .current) -> EndedRowText {
    EndedRowText(
        title: titleOf(s, zone: zone), project: project ? projectName(projectKey(s)) : "",
        time: endedTime(endedMs(s), now: now, zone: zone))
}

/// A card of 需要处理 (Android's AttentionCard): what kind of thing waits, since when or until when, and where to go.
public struct AttentionCardText: Equatable, Sendable {
    /// 请求执行, 「请求权限 · Write」, 「Codex 在问你」, or the status word.
    public var kind: String
    /// 「剩 8 分钟，超时拒绝」 for an approval with a deadline, else when it last changed.
    public var right: String
    public var title: String
    /// 查看会话, 去回答, 去处理.
    public var action: String
    /// Project and machine where they say something, then the question; empty for no line.
    public var meta: String

    public init(kind: String, right: String, title: String, action: String, meta: String) {
        self.kind = kind
        self.right = right
        self.title = title
        self.action = action
        self.meta = meta
    }

    public var spoken: String { [title, kind, meta.replacingOccurrences(of: " · ", with: "，"), right].filter { !$0.isEmpty }.joined(separator: "，") }
}

/// `approval` and `question`: the first of each still waiting, from the session's events (`Derived`).
public func attentionCardText(
    _ state: RemoteState, _ s: SessionView, scope: HomeScope, approval: PBApprovalRequested?, question: String?,
    now: Int64, zone: TimeZone = .current
) -> AttentionCardText {
    // What it waits for stays true while the server is out of reach.
    let st = statusOf(s, machine: state.machines[s.machineId])
    let kind: String
    switch st {
    case .approval: kind = approval.map(approvalHead) ?? "请求权限"
    case .question: kind = "\(agentName(s.agent)) 在问你"
    default: kind = st.text
    }
    let right: String
    if st == .approval, let approval, approval.hasDeadline {
        right = "剩 \(minutesLeft(deadlineMs: approval.deadline.seconds * 1000, now: now)) 分钟，超时拒绝"
    } else {
        right = whenText(s.updatedMs, now: now, zone: zone)
    }
    // Not scoped: a card from another project than the scope's says which.
    let line = (cardMeta(state, s, scope: scope) + (question.map { [$0] } ?? [])).joined(separator: " · ")
    return AttentionCardText(
        kind: kind, right: right, title: titleOf(s, zone: zone),
        action: st == .approval ? "查看会话" : st == .question ? "去回答" : "去处理", meta: line)
}

/// The command that answers an approval.
public func resolveApprovalCommand(sessionId: String, approvalId: String, allow: Bool, message: String, always: Bool) -> PBCommand {
    var cmd = PBCommand()
    cmd.resolveApproval = .with {
        $0.sessionID = sessionId
        $0.approvalID = approvalId
        $0.allow = allow
        $0.message = message
        $0.always = always
    }
    return cmd
}
