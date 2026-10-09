import Foundation
import RAProto
import Testing

@testable import RACore

/// Android's app ApprovalDisplayTest.
@Suite struct ApprovalDisplayTests {
    @Test func revealsControlsAndPreservesOrdinaryLanguage() {
        let normal = "العربية 中文 👩‍💻\n\tcode"
        #expect(approvalDisplay(normal) == normal)
        #expect(
            approvalDisplay("\u{061c}\u{200e}\u{200f}\u{202e}\u{2066}\u{2069}\r\u{001b}\u{009b}")
                == "\\u061c\\u200e\\u200f\\u202e\\u2066\\u2069\\u000d\\u001b\\u009b")
    }

    @Test func theDockShowsTheCommandWithoutRewritingTheApproval() throws {
        let raw = "printf '\u{202e}'; dangerous"
        let a = PBApprovalRequested.with { $0.approvalID = "approval"; $0.command = raw }
        let before = try a.serializedData()
        let shown = approvalDisplay(raw)
        #expect(dockSummary(.approval(a), agent: "Codex") == ("请求执行", shown))
        #expect(a.command == raw)
        #expect(try a.serializedData() == before)
    }

    // --- not in Android's tests ---

    @Test func theDockSaysOneLineOfWhateverWaits() {
        #expect(dockSummary(.approval(.with { $0.toolName = "Write" }), agent: "Codex") == ("请求权限", "Write"))
        #expect(dockSummary(.approval(.with { $0.command = "a\r\nb" }), agent: "Codex") == ("请求执行", "a\\u000d"))
        let q = PBQuestionAsked.with { $0.questionID = "q1"; $0.questions = [.with { $0.text = "用哪个数据库？\n第二行" }] }
        #expect(dockSummary(.question(q), agent: "Claude Code") == ("Claude Code 在问你", "用哪个数据库？"))
        #expect(dockSummary(.plan(text: "x"), agent: "Codex") == ("计划待批准", ""))
        #expect(dockSummary(.popup(title: "trust folder"), agent: "Codex") == ("终端里有弹窗", "trust folder"))
        #expect(DockItem.question(q).id == "q:q1")
        #expect(!longQuestion(.with { $0.text = String(repeating: "字", count: 120) }))
        #expect(longQuestion(.with { $0.text = String(repeating: "字", count: 121) }))
        // An emoji is two of Kotlin's characters.
        #expect(longQuestion(.with { $0.text = String(repeating: "😀", count: 61) }))
        #expect(longQuestion(.with { $0.options = (0..<5).map { i in .with { $0.label = "\(i)" } } }))
    }
}

/// Android's app AccountCacheTest.
@Suite struct AccountCacheTests {
    @Test func reusedImageIdsAndLateLoadsCannotCrossAccounts() {
        let cache = AccountCache<String>(limit: 6)
        let old = NSObject()
        let next = NSObject()
        cache.use(old)
        cache.put(old, key: "session/image", value: "private old image")
        #expect(cache.get(old, key: "session/image") == "private old image")
        cache.clear()
        cache.put(old, key: "session/image", value: "late old image")
        #expect(cache.get(old, key: "session/image") == nil)
        cache.use(next)
        cache.put(old, key: "session/image", value: "late old image")
        #expect(cache.get(next, key: "session/image") == nil)
        cache.put(next, key: "session/image", value: "new image")
        #expect(cache.get(next, key: "session/image") == "new image")
        #expect(cache.get(old, key: "session/image") == nil)
        cache.use(old)
        #expect(cache.get(old, key: "session/image") == nil)
    }

    @Test func cacheStillEvictsLeastRecentlyUsedImage() {
        let cache = AccountCache<String>(limit: 2)
        let owner = NSObject()
        cache.use(owner)
        cache.put(owner, key: "a", value: "a")
        cache.put(owner, key: "b", value: "b")
        _ = cache.get(owner, key: "a")
        cache.put(owner, key: "c", value: "c")
        #expect(cache.get(owner, key: "b") == nil)
        #expect(cache.get(owner, key: "a") == "a")
    }
}

/// Android's app FollowTest.
@Suite struct FollowTests {
    @Test func onlyTheUserStopsFollowing() {
        var f = Follow()
        // Opening a long history: the app's own jump ends before the list
        // reaches the bottom. That must not stop following.
        f.observe(userDragging: false, atBottom: false)
        #expect(f.on)
        f.observe(userDragging: true, atBottom: false)
        #expect(!f.on)
        // More messages arrive while scrolled up: still not following.
        f.observe(userDragging: false, atBottom: false)
        #expect(!f.on)
        f.observe(userDragging: false, atBottom: true)
        #expect(f.on)
    }
}

/// Words the screens share; Android has no tests of these.
@Suite struct CommonTests {
    @Test func theLinkAndThePreferencesAreSaidInAWord() {
        var st = RemoteState()
        #expect(connWord(st) == "连接中")
        #expect(prefsBlocked(st) == "连上服务器后才能修改")
        st.link = .noServer
        #expect(connWord(st) == "连不上")
        st.synced = true
        #expect(connWord(st) == "重连中")
        st.conn = .online
        #expect(connWord(st) == "已连接")
        #expect(prefsBlocked(st) == "正在读取项目列表…")
        st.prefs = .empty
        #expect(prefsBlocked(st) == nil)
        #expect(fileName("/tmp/a/shot.png") == "shot.png")
        #expect(fileName("/tmp/a/") == "图片")
        #expect(aspect(.with { $0.width = 400; $0.height = 100 }) == 2.5)
        #expect(aspect(.with { $0.previewWidth = 30; $0.previewHeight = 40; $0.width = 400; $0.height = 100 }) == 0.75)
        #expect(aspect(PBImageRef()) == 4.0 / 3.0)
        #expect(modeHelp(.full) == "所有操作都不审批")
    }
}
