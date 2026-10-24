// The home page as a session timeline with a scope (client-v27,
// architecture §8 「主页的范围」): which sessions it lists, how the ended
// ones are grouped by day, what a row writes, and filtering by title.
import Foundation
import RAProto

/// What the home page lists: one project, or every project. Two scopes
/// name the same project when the code units of its path are the same.
public enum HomeScope: Hashable, Sendable {
    case all
    case of(key: String)

    private var id: JvmKey? { key.map { JvmKey($0) } }

    public static func == (a: HomeScope, b: HomeScope) -> Bool { a.id == b.id }

    public func hash(into hasher: inout Hasher) { hasher.combine(id) }

    /// The project the scope names, nil for every project.
    public var key: String? {
        if case .of(let key) = self { return key }
        return nil
    }
}

/// 需要处理 shows this many cards before 「还有 N 项待处理」.
public let attentionMax = 3

/// 进行中 shows this many rows before 「还有 N 个进行中」.
public let runningMax = 5

/// Ended sessions are rendered this many at a time.
public let endedPage = 30

/// With more sessions than this in the scope the home page offers the title filter; with more projects, the scope menu a search.
public let filterOver = 8

/// The projects on the lists: pinned in their order, then the others; removed ones are not among them.
public func listedProjects(_ state: RemoteState) -> [Project] {
    let g = projectGroups(state)
    return g.pinned + g.others
}

/// The scope before the user chose one: the only listed project, else every project.
public func defaultScope(_ state: RemoteState) -> HomeScope {
    let listed = listedProjects(state)
    return listed.count == 1 ? .of(key: listed[0].key) : .all
}

/// The scope the home page uses: the one remembered on this device, or the
/// default when none was chosen, when the remembered project was removed or
/// is not there, and when 全部项目 was remembered but there is no more than
/// one project to tell apart (the menu does not offer it then).
public func resolveScope(remembered: HomeScope?, state: RemoteState) -> HomeScope {
    let listed = listedProjects(state)
    switch remembered {
    case .of(let key): return listed.contains { $0.key.jvmEquals(key) } ? .of(key: key) : defaultScope(state)
    case .all: return listed.count > 1 ? .all : defaultScope(state)
    case nil: return defaultScope(state)
    }
}

/// The key the remembered scope's project is marked with: the account's, for this use alone.
public func scopeKey(root: Data) -> Data { Keys.deriveKey(root: root, purpose: "home-scope") }

/// The scope as this device's settings keep it. Those are plaintext on
/// disk, so a project is written as a mark only the account's key makes
/// (HMAC-SHA256 of its path under `key`), never as its path.
public func encodeScope(_ scope: HomeScope, key: Data) -> String {
    switch scope {
    case .all: return "all"
    case .of(let path): return "project:" + hmacSha256(key: key, Data(path.utf8)).hex
    }
}

/// The scope read back: the one of `listed` whose mark was stored. Nil for
/// nothing stored, something else, and a project that is not listed.
public func decodeScope(_ stored: String?, key: Data, listed: [Project]) -> HomeScope? {
    guard let stored else { return nil }
    if stored == "all" { return .all }
    return listed.first { encodeScope(.of(key: $0.key), key: key) == stored }.map { .of(key: $0.key) }
}

/// The scope's name where the page writes it: 全部项目, or the project's.
public func scopeName(_ scope: HomeScope) -> String {
    switch scope {
    case .all: return "全部项目"
    case .of(let key): return projectName(key)
    }
}

/// Which of 项目, CLI and 机器 a row's third line writes: only what differs between the rows of its list.
public struct RowMeta: Equatable, Sendable {
    public var project: Bool
    public var agent: Bool
    public var machine: Bool

    public init(project: Bool = false, agent: Bool = false, machine: Bool = false) {
        self.project = project
        self.agent = agent
        self.machine = machine
    }
}

/// What the rows of a list write: the project only where the list spans
/// projects, the CLI only when `sessions` (everything in the scope or the
/// list) used more than one, the machine only when the account has more than
/// one computer.
public func rowMeta(_ state: RemoteState, sessions: some Sequence<SessionView>, project: Bool) -> RowMeta {
    RowMeta(project: project, agent: Set(sessions.map(\.agent)).count > 1, machine: state.machines.count > 1)
}

/// A row's third line: 分叉 · 项目 · CLI · 机器, each only when it says something; empty for no third line.
public func metaLine(_ state: RemoteState, _ s: SessionView, meta: RowMeta) -> String {
    var parts = [String]()
    if forked(s) { parts.append("分叉") }
    if meta.project { parts.append(projectName(projectKey(s))) }
    if meta.agent { parts.append(agentName(s.agent)) }
    if meta.machine { parts.append(machineName(state.machines[s.machineId], id: s.machineId)) }
    return parts.joined(separator: " · ")
}

/// Whether a title has `query` in it, ignoring case; a blank query matches everything.
public func titleMatches(_ s: SessionView, query: String, zone: TimeZone = .current) -> Bool {
    query.jvmIsBlank || titleOf(s, zone: zone).jvmContainsIgnoringCase(query.jvmTrim())
}

/// The home page's lists for a scope.
/// `attention` is not scoped: every session of the account that waits, so a
/// project chosen as the scope never hides another one's approval.
/// `running` and `ended` are the scope's own; under 全部项目 a removed
/// project's sessions are only among the first two.
/// `ended` holds the ended sessions fetched so far: the listing comes a
/// page at a time (architecture §8 「会话列表分页」). `endedTotal` is how many
/// the scope has by the machines' project summaries, and `total` the scope's
/// sessions: those that have not ended and `endedTotal`. Neither depends on
/// the query.
public struct HomeTimeline: Equatable, Sendable {
    public var attention: [SessionView]
    public var running: [SessionView]
    public var ended: [SessionView]
    public var total: Int
    public var meta: RowMeta
    public var endedTotal: Int

    public init(
        attention: [SessionView], running: [SessionView], ended: [SessionView], total: Int, meta: RowMeta,
        endedTotal: Int? = nil
    ) {
        self.attention = attention
        self.running = running
        self.ended = ended
        self.total = total
        self.meta = meta
        self.endedTotal = endedTotal ?? ended.count
    }

    /// How many sessions are listed, for the line under a filtered list.
    public var size: Int { attention.count + running.count + ended.count }
}

/// Whether a session belongs to another project than the one the scope names.
public func outsideScope(_ s: SessionView, scope: HomeScope) -> Bool {
    if case .of(let key) = scope { return !projectKey(s).jvmEquals(key) }
    return false
}

public func homeTimeline(_ state: RemoteState, scope: HomeScope, query: String = "", zone: TimeZone = .current) -> HomeTimeline {
    let all = state.sessions.values.filter { $0.info?.taskOwned != true }
    let waiting = all.filter { needsAttention($0, machine: state.machines[$0.machineId]) }.sorted(by: attentionOrder(state))
    let waitingIds = Set(waiting.map(\.id))
    let hidden = state.prefs?.hidden ?? []
    let inScope: (String) -> Bool = { key in
        if case .of(let k) = scope { return key.jvmEquals(k) }
        return !hidden.contains(JvmKey(key))
    }
    let running = all.filter { !exited($0) && !waitingIds.contains($0.id) && !outsideScope($0, scope: scope) }.sorted(by: liveOrder)
    let ended = all.filter { exited($0) && inScope(projectKey($0)) }.sorted(by: endedOrder)
    let live = waiting.filter { !outsideScope($0, scope: scope) } + running
    // How many ended sessions the scope has is the summaries' to say: only
    // some of them are held here. Never fewer than are.
    let summed = projects(state).filter { inScope($0.key) }.reduce(0) { $0 + $1.ended }
    let endedTotal = max(summed, ended.count)
    let match: (SessionView) -> Bool = { titleMatches($0, query: query, zone: zone) }
    return HomeTimeline(
        attention: waiting.filter(match),
        running: running.filter(match),
        ended: ended.filter(match),
        total: live.count + endedTotal,
        meta: rowMeta(state, sessions: live + ended, project: scope == .all),
        endedTotal: endedTotal
    )
}

/// Whether showing the first `want` of a scope's ended sessions takes another
/// page of the listing. `held` are the ones fetched so far, newest first, and
/// `total` how many the scope has by the summaries. A page is due while fewer
/// are held than are to be shown, and while what would be shown reaches past
/// the pages fetched: sessions kept from before a reconnect, or fetched on
/// their own, with others not fetched yet before them. A page may hold none
/// of a project's sessions; asking again goes on until enough are held or no
/// page is left. Before the listing's first page came, that page is the one
/// due.
public func needsEndedPage(_ state: RemoteState, held: [SessionView], want: Int, total: Int) -> Bool {
    if !state.moreSessions { return false }
    return held.count < min(want, total) || held.prefix(max(0, want)).contains { pastFetched(state, $0) }
}

/// Whether a session held here lies past the pages fetched: kept from before
/// a reconnect, or fetched by its ID, with sessions not fetched yet before
/// it in the listing. One that has not ended never does.
public func pastFetched(_ state: RemoteState, _ s: SessionView) -> Bool {
    guard let next = state.sessionsNext else { return false }
    return afterPlace(endedMs: s.listedEndedMs, sessionId: s.id, place: next)
}

/// The line under a card of 需要处理: the project where the list spans projects or the card is from outside the scope, and the machine when there are several.
public func cardMeta(_ state: RemoteState, _ s: SessionView, scope: HomeScope) -> [String] {
    var parts = [String]()
    if scope == .all || outsideScope(s, scope: scope) { parts.append(projectName(projectKey(s))) }
    if state.machines.count > 1 { parts.append(machineName(state.machines[s.machineId], id: s.machineId)) }
    return parts
}

// --- ended sessions by day ---

/// The groups ended sessions fall in, by the local calendar day they ended on.
public enum EndedGroup: Hashable, Sendable {
    case today
    case yesterday
    /// Two to six days ago.
    case week
    case month(year: Int, month: Int)
}

/// The group of a session that ended at `ms`; a time ahead of this device's clock counts as today.
public func endedGroup(_ ms: Int64, now: Int64, zone: TimeZone = .current) -> EndedGroup {
    let day = LocalDay(ms: ms, zone: zone)
    switch LocalDay(ms: now, zone: zone).epochDay - day.epochDay {
    case ...0: return .today
    case 1: return .yesterday
    case 2...6: return .week
    default: return .month(year: day.year, month: day.month)
    }
}

/// 今天, 昨天, 近 7 天, 9 月, and 2025 年 12 月 for a month of another year.
public func groupTitle(_ g: EndedGroup, now: Int64, zone: TimeZone = .current) -> String {
    switch g {
    case .today: return "今天"
    case .yesterday: return "昨天"
    case .week: return "近 7 天"
    case .month(let year, let month):
        return year == LocalDay(ms: now, zone: zone).year ? "\(month) 月" : "\(year) 年 \(month) 月"
    }
}

/// When an ended session ended, as its row writes it: the time of day under
/// 今天 and 昨天, the weekday under 近 7 天, the date before that. Empty when
/// the time is not known.
public func endedTime(_ ms: Int64, now: Int64, zone: TimeZone = .current) -> String {
    if ms <= 0 { return "" }
    let day = LocalDay(ms: ms, zone: zone)
    switch endedGroup(ms, now: now, zone: zone) {
    case .today, .yesterday: return day.hhmm
    case .week: return weekdayNames[day.weekdayIndex]
    case .month: return "\(day.month)月\(day.day)日"
    }
}

/// Ended sessions under their groups, both in the order given (newest first).
public func groupEnded(_ ended: [SessionView], now: Int64, zone: TimeZone = .current) -> [(group: EndedGroup, sessions: [SessionView])] {
    var groups = OrderedMap<EndedGroup, [SessionView]>()
    for s in ended {
        let g = endedGroup(endedMs(s), now: now, zone: zone)
        groups[g] = (groups[g] ?? []) + [s]
    }
    return groups.entries.map { (group: $0.key, sessions: $0.value) }
}

/// The line that ends the list: how many ended sessions the scope has
/// (`total`, by the summaries), once they do not fit one page; nil before
/// that. `more`: not all of them are shown yet.
public func endedTail(total: Int, more: Bool) -> String? {
    if total <= endedPage { return nil }
    return more ? "共 \(total) 个，滚到底加载更早的" : "共 \(total) 个"
}

/// The line that ends a list while earlier pages of the listing are on their way.
public let loadingEarlier = "正在加载更早的会话…"

/// The same place while there is no connection to fetch them over.
public let earlierOffline = "连上服务器后加载更早的会话"

/// The line that ends a list whose earlier sessions are not all fetched and
/// are wanted now: on their way, waiting for the connection, or why the last
/// page failed (a tap tries again).
public func earlierTail(_ state: RemoteState) -> String {
    if state.conn != .online { return earlierOffline }
    if !state.sessionsError.isEmpty && !state.sessionsListed { return "没能读取会话列表：\(state.sessionsError) · 点这里重试" }
    if !state.sessionsError.isEmpty { return "没能加载更早的会话：\(state.sessionsError) · 点这里重试" }
    return loadingEarlier
}

/// The line that ends a filtered list.
public func filterTail(count: Int, query: String) -> String {
    count == 0 ? "没有标题里有「\(query.jvmTrim())」的会话" : "\(count) 个会话的标题里有「\(query.jvmTrim())」"
}

// --- the scope menu ---

/// A row of 看哪个项目.
public struct ScopeRow: Equatable, Sendable, Identifiable {
    public var scope: HomeScope
    public var name: String
    public var detail: String
    public var current: Bool

    public var id: HomeScope { scope }

    public init(scope: HomeScope, name: String, detail: String, current: Bool) {
        self.scope = scope
        self.name = name
        self.detail = detail
        self.current = current
    }
}

/// How many of some sessions wait for the user and how many run, counted
/// as the home page's 需要处理 and 进行中 are: 「2 个待处理 · 3 个进行中」,
/// a part that is 0 left out, "" when both are.
public func sessionCounts(_ state: RemoteState, _ sessions: some Sequence<SessionView>) -> String {
    var waiting = 0
    var live = 0
    for s in sessions {
        if needsAttention(s, machine: state.machines[s.machineId]) {
            waiting += 1
        } else if !exited(s) {
            live += 1
        }
    }
    return [waiting > 0 ? "\(waiting) 个待处理" : "", live > 0 ? "\(live) 个进行中" : ""].filter { !$0.isEmpty }.joined(separator: " · ")
}

/// What a project's row says under its name, the same wherever projects are listed (client-v34):
/// the path, then how many sessions wait and run there, or that none of its computers is online.
public func projectDetail(_ state: RemoteState, _ pr: Project) -> String {
    let counts = sessionCounts(state, pr.sessions)
    let offline = !pr.machineIds.isEmpty && !pr.machineIds.contains { state.machines[$0]?.online == true }
    return shortPath(pr.key) + (!counts.isEmpty ? " · \(counts)" : offline ? " · 电脑不在线" : "")
}

/// The scope menu: 全部项目 when there is more than one project to choose
/// between, then the pinned projects, then the others in the lists' order.
/// `search` keeps the projects whose name or path has it.
public func scopeRows(_ state: RemoteState, current: HomeScope, search: String = "") -> [ScopeRow] {
    let listed = listedProjects(state)
    let q = search.jvmTrim()
    var rows = [ScopeRow]()
    if listed.count > 1 && q.isEmpty {
        let counts = sessionCounts(state, state.sessions.values)
        rows.append(ScopeRow(scope: .all, name: "全部项目", detail: counts.ifEmpty("\(listed.count) 个项目"), current: current == .all))
    }
    for pr in listed {
        if !q.isEmpty && !pr.name.jvmContainsIgnoringCase(q) && !pr.key.jvmContainsIgnoringCase(q) { continue }
        rows.append(ScopeRow(scope: .of(key: pr.key), name: pr.name, detail: projectDetail(state, pr), current: current == .of(key: pr.key)))
    }
    return rows
}
