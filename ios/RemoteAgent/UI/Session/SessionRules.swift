// Shell-level session rules that stay outside Go Core: scroll-to-bottom
// tracking (`ChatScroll`), composer send enablement (`composerCanSend`),
// slash-command warning, queue folding (`shownPending`), and VoiceOver
// arrival announcements (`LiveAnnouncer`).
import Foundation
import RACore
import RAProto

// MARK: Following the end of the chat

struct ChatScroll: Equatable, Sendable {
    private var follow = Follow()
    private(set) var atBottom = true
    private(set) var seen = 0

    var following: Bool { follow.on }

    mutating func observe(userDragging: Bool, atBottom: Bool, rows: Int) {
        self.atBottom = atBottom
        follow.observe(userDragging: userDragging, atBottom: atBottom)
        if atBottom { seen = rows }
    }

    mutating func contentChanged(rows: Int) -> Bool {
        if follow.on { seen = rows }
        return follow.on
    }

    mutating func prepended(_ n: Int) {
        if !follow.on { seen += n }
    }

    mutating func jumpToEnd(rows: Int) {
        follow = Follow()
        seen = rows
    }

    func fresh(rows: Int) -> Int { atBottom ? 0 : max(0, rows - seen) }
}

func shownPending<T>(_ all: [T], expanded: Bool) -> [T] {
    all.count > 3 && !expanded ? Array(all.prefix(2)) : all
}

// MARK: The message box

func composerCanSend(text: String, online: Bool, keeping: Bool, hasImages: Bool, imagesReady: Bool, refused: Bool) -> Bool {
    !keeping && online && !refused && (!text.jvmIsBlank || hasImages) && (!hasImages || imagesReady)
}

func slashWarning(_ text: String) -> Bool {
    text.drop { $0.isWhitespace }.hasPrefix("/")
}

// MARK: Telling VoiceOver what arrived

struct LiveAnnouncer: Equatable, Sendable {
    private var lastKey: String?
    private var lastAt: Int64 = 0
    private var lastDockKey = ""
    static let gapMs: Int64 = 4_000

    mutating func announce(last: PBSessionTimelineItem?, dock: PBSessionDockView?, agent: String, now: Int64, initial: Bool) -> String? {
        let dockKey = (dock?.hasItem_p == true) ? "\(dock!.kind.rawValue):\(dock!.itemID)" : ""
        let dockArrived = !dockKey.isEmpty && dockKey != lastDockKey
        lastDockKey = dockKey

        let key = last?.id
        let newReply = key != lastKey
        lastKey = key

        if initial { return nil }
        if dockArrived, let dock {
            lastAt = now
            return dock.title.rendered()
        }
        guard newReply, let last, case .agentMessage = last.kind, now - lastAt >= Self.gapMs else { return nil }
        lastAt = now
        return L10n.sessionIosReplyArrived(agent: agent)
    }
}
