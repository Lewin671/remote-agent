// The home page as the list it draws (client-v58 #home, architecture §8
// 「主页的范围」): the sentence at its top, which lines there are and in what
// order, what each row writes, what ends the list, and which pages of the
// listing it wants meanwhile. The part of the home page that decides and
// draws nothing, so that the screen only lays the lines out and the rules
// are tested here.
import Foundation
import RAProto

/// How much of each part of the timeline the page shows; reset with the scope.
public struct HomeShown: Equatable, Sendable {
    /// 「还有 N 件」 was opened.
    public var allAttention: Bool
    /// 「还有 N 个」 under 进行中 was opened.
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
    /// The most urgent of what waits, when it is an approval: the dark card, decided right there.
    case attentionCard(SessionView)
    /// Another thing that waits, one row of the grey list: `first` and `last` say where the list's corners are.
    case attention(SessionView, first: Bool, last: Bool)
    /// 「还有 N 件」, the last row of the grey list.
    case moreAttention(Int)
    case running(SessionView, last: Bool)
    /// 「还有 N 个」 under 进行中.
    case moreRunning(Int)
    case ended(SessionView)
    case tail(HomeTail)

    public var id: String {
        switch self {
        case .label(let id, _, _): return "h:" + id
        case .attentionCard(let s), .attention(let s, _, _), .running(let s, _), .ended(let s): return "s:" + s.id
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

/// The home page's list (design-system §6 「等你的事」「会话行」): what waits,
/// for the whole account and first, its most urgent approval as the dark
/// card and the rest at most `attentionMax` rows before 「还有 N 件」; 进行中,
/// at most `runningMax` rows before 「还有 N 个」; the ended sessions by day,
/// `shown.ended` of them; and the line that ends the list. A filter shows
/// every match.
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
    // Only an approval is decided on the card; a question, a plan or a popup is a row like the rest.
    let card = tl.attention.first.flatMap { statusOf($0, machine: state.machines[$0.machineId]) == .approval ? $0 : nil }
    if let card { items.append(.attentionCard(card)) }
    let waiting = card == nil ? tl.attention : Array(tl.attention.dropFirst())
    let rows = shown.allAttention || filtering ? waiting : Array(waiting.prefix(attentionMax))
    let hidden = waiting.count - rows.count
    for (i, s) in rows.enumerated() {
        items.append(.attention(s, first: i == 0, last: i == rows.count - 1 && hidden == 0))
    }
    if hidden > 0 { items.append(.moreAttention(hidden)) }
    if !tl.running.isEmpty {
        items.append(.label(id: "running", title: "进行中", count: nil))
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

/// One row of what waits (design-system §6 「等你的事」): the title, then
/// 「类型 · 摘要」 with the type in orange. Another project's row names its
/// project before the summary when the scope is one project.
public struct PendingRowText: Equatable, Sendable {
    public var title: String
    /// 请求执行, 「Codex 在问你」, 计划待批准, 终端里有弹窗.
    public var kind: String
    /// After the type; empty for nothing.
    public var summary: String

    public init(title: String, kind: String, summary: String) {
        self.title = title
        self.kind = kind
        self.summary = summary
    }

    public var spoken: String { [title, kind, summary.replacingOccurrences(of: " · ", with: "，")].filter { !$0.isEmpty }.joined(separator: "，") }
}

/// `approval` and `question`: the first of each still waiting, from the session's events (`Derived`).
public func pendingRowText(
    _ state: RemoteState, _ s: SessionView, scope: HomeScope, approval: PBApprovalRequested?, question: String?,
    zone: TimeZone = .current
) -> PendingRowText {
    // What it waits for stays true while the server is out of reach.
    let st = statusOf(s, machine: state.machines[s.machineId])
    let kind: String
    let about: String
    switch st {
    case .approval:
        kind = approval.map(approvalHead) ?? st.text
        // One line: the row is a summary, the card and the dock show the command whole.
        about = approval.map { approvalBody($0).split(whereSeparator: \.isWhitespace).joined(separator: " ") } ?? ""
    case .question:
        kind = "\(agentName(s.agent)) 在问你"
        about = question ?? ""
    case .popup:
        kind = st.text
        about = s.info?.statusDetail ?? ""
    default:
        kind = st.text
        about = ""
    }
    let other = outsideScope(s, scope: scope) ? projectName(projectKey(s)) : ""
    return PendingRowText(
        title: titleOf(s, zone: zone), kind: kind, summary: [other, about].filter { !$0.isEmpty }.joined(separator: " · "))
}

/// The head and the place of the dark card of an approval (design-system §6 「终端卡」).
public struct ApprovalCardText: Equatable, Sendable {
    /// 请求执行, 「请求权限 · Write」.
    public var kind: String
    /// 「剩 8 分钟，超时拒绝」, 未连接 out of reach of the server, or empty for no deadline.
    public var time: String
    /// The deadline is less than a minute away: the time turns orange.
    public var hot: Bool
    public var title: String
    /// 「项目 · 机器」, the computer only where the account has more than one.
    public var place: String

    public init(kind: String, time: String, hot: Bool, title: String, place: String) {
        self.kind = kind
        self.time = time
        self.hot = hot
        self.title = title
        self.place = place
    }

    public var spoken: String { [title, kind, place.replacingOccurrences(of: " · ", with: "，"), time].filter { !$0.isEmpty }.joined(separator: "，") }
}

/// `timeoutNote`: the home page's card says what the end of the time does (「，超时拒绝」); a docked one does not.
public func approvalCardText(
    _ state: RemoteState, _ s: SessionView, approval: PBApprovalRequested, timeoutNote: Bool, now: Int64,
    zone: TimeZone = .current
) -> ApprovalCardText {
    let deadline = approval.hasDeadline ? approval.deadline.seconds * 1000 : 0
    let left = deadline - now
    let time: String
    if state.unlinked {
        time = "未连接"
    } else if deadline > 0 {
        time = "剩 \(minutesLeft(deadlineMs: deadline, now: now)) 分钟" + (timeoutNote && left > 0 ? "，超时拒绝" : "")
    } else {
        time = ""
    }
    return ApprovalCardText(
        kind: approvalHead(approval), time: time, hot: !state.unlinked && deadline > 0 && left < 60_000, title: titleOf(s, zone: zone),
        // The computer is named only where the account has more than one (design-system §6 终端卡).
        place: ([projectName(projectKey(s))] + (state.machines.count > 1 ? [machineName(state.machines[s.machineId], id: s.machineId)] : [])).joined(separator: " · "))
}

// --- the sentence at the top ---

/// The sentence at the top of the home page (design-system §6 「大标题」):
/// how things stand, and the line under it.
public struct HomeHeadline: Equatable, Sendable {
    /// Which sentence it is: the stretch of the way that is down, or how the sessions stand.
    public enum Kind: Equatable, Sendable {
        case noNetwork, connecting, reconnecting, noServer
        /// 「N 件事等你」: `count` is the number, drawn orange.
        case waiting(count: Int)
        case running, nothingWaits, noSessions, unreadable, emptyProject
    }

    public var kind: Kind
    /// The whole sentence.
    public var title: String
    /// The line under it; empty for none.
    public var sub: String
    /// The line under it is the reason the server cannot be reached, with nothing kept to show: red.
    public var subError: Bool
    /// The line under it is a path, in mono.
    public var subPath: Bool
    /// 详情 at the end of the line under it opens why the server cannot be reached.
    public var details: Bool

    public init(kind: Kind, title: String, sub: String = "", subError: Bool = false, subPath: Bool = false, details: Bool = false) {
        self.kind = kind
        self.title = title
        self.sub = sub
        self.subError = subError
        self.subPath = subPath
        self.details = details
    }

    /// Whether the sentence is about the connection; the page's buttons wait for it then.
    public var unlinked: Bool {
        switch kind {
        case .noNetwork, .connecting, .reconnecting, .noServer: return true
        default: return false
        }
    }
}

/// The first of these that holds: the connection when it is down (the
/// nearest stretch, as 「断线时显示什么」 says it), what waits for the whole
/// account, what runs in the scope, that no session was ever started, that
/// the project chosen has none yet, and else that nothing waits. The line
/// under it counts the scope's running and idle sessions apart. Nil while
/// nothing was read yet.
public func homeHeadline(_ state: RemoteState, scope: HomeScope) -> HomeHeadline? {
    // Something from before to show under the sentence: what was cached or listed.
    let kept = state.loaded || !state.sessions.isEmpty
    let before = "下面是上次的内容"
    switch state.link {
    case .noNetwork:
        return HomeHeadline(kind: .noNetwork, title: "本机网络不通", sub: kept ? before : "联网后才能读取会话")
    case .waiting:
        return state.synced
            ? HomeHeadline(kind: .reconnecting, title: "正在重连…", sub: kept ? before : "")
            : HomeHeadline(kind: .connecting, title: "连接中…", sub: kept ? before : "")
    case .noServer:
        return kept
            ? HomeHeadline(kind: .noServer, title: "连不上服务器", sub: "正在重试，" + before, details: true)
            : HomeHeadline(kind: .noServer, title: "连不上服务器", sub: state.error.ifEmpty("服务器没有回应"), subError: true, details: true)
    case .online:
        break
    }
    if !state.loaded && state.sessions.isEmpty { return nil }
    let tl = homeTimeline(state, scope: scope)
    var running = 0
    var idle = 0
    for s in tl.running {
        switch statusOf(s, machine: state.machines[s.machineId]).tone {
        case .run: running += 1
        case .idle: idle += 1
        default: break
        }
    }
    if !tl.attention.isEmpty {
        let n = tl.attention.count
        let parts = [running > 0 ? "\(running) 个会话在运行" : "", idle > 0 ? (running > 0 ? "\(idle) 个空闲" : "\(idle) 个会话空闲") : ""]
        return HomeHeadline(kind: .waiting(count: n), title: "\(n) 件事等你", sub: parts.filter { !$0.isEmpty }.joined(separator: "，"))
    }
    if running > 0 {
        return HomeHeadline(kind: .running, title: "\(running) 个会话在运行", sub: idle > 0 ? "另有 \(idle) 个空闲" : "")
    }
    if case .of(let key) = scope, tl.total == 0 {
        return HomeHeadline(kind: .emptyProject, title: "这个项目还没有会话", sub: shortPath(key), subPath: true)
    }
    if state.sessions.isEmpty {
        // The listing failed before anything came: that there are none is not known.
        return !state.sessionsListed && !state.sessionsError.isEmpty
            ? HomeHeadline(kind: .unreadable, title: "暂时读不到会话")
            : HomeHeadline(kind: .noSessions, title: "还没有会话")
    }
    return HomeHeadline(kind: .nothingWaits, title: "没有等你的事", sub: idle > 0 ? "\(idle) 个会话空闲" : "")
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
